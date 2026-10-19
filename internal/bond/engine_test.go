package bond

import (
	"encoding/binary"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
	"github.com/rayzorben/net-bond-007/internal/sched"
)

// simPath delivers frames from one engine to another with latency and loss.
type simPath struct {
	delay time.Duration
	loss  float64
	dead  bool
	mu    sync.Mutex
	rng   *rand.Rand
}

func (p *simPath) sender(dst *Engine, dstLink func() *Link) func([]byte) error {
	return func(f []byte) error {
		p.mu.Lock()
		drop := p.dead || p.rng.Float64() < p.loss
		p.mu.Unlock()
		if drop {
			return nil
		}
		c := append([]byte(nil), f...)
		time.AfterFunc(p.delay, func() { dst.Receive(dstLink(), c) })
		return nil
	}
}

type collector struct {
	mu   sync.Mutex
	seqs []uint32
}

func (c *collector) deliver(p []byte) {
	c.mu.Lock()
	c.seqs = append(c.seqs, binary.BigEndian.Uint32(p[20:]))
	c.mu.Unlock()
}

func (c *collector) snapshot() []uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]uint32(nil), c.seqs...)
}

// fakePkt builds a 1200-byte IPv4/TCP-ish packet carrying an id at offset 20.
func fakePkt(id uint32) []byte {
	p := make([]byte, 1200)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[9] = 6
	binary.BigEndian.PutUint32(p[20:], id) // lands in TCP src/dst port bytes
	p[20+13] = 0x10                        // ACK only: not priority
	return p
}

func pair(t *testing.T, paths []*simPath) (*Engine, *Engine, *collector) {
	t.Helper()
	got := &collector{}
	b := New(1, got.deliver)
	a := New(1, func([]byte) {})
	for i, p := range paths {
		id := uint8(i + 1)
		p.rng = rand.New(rand.NewSource(int64(i)))
		bl := b.AddLink(id, "l", "sim", func([]byte) error { return nil })
		bl.SetState(StateUp)
		al := a.AddLink(id, "l", "sim", p.sender(b, func() *Link { return bl }))
		al.SetState(StateUp)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, got
}

func waitFor(t *testing.T, c *collector, n int) []uint32 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := c.snapshot(); len(s) >= n {
			time.Sleep(50 * time.Millisecond)
			return c.snapshot()
		}
		time.Sleep(5 * time.Millisecond)
	}
	s := c.snapshot()
	t.Fatalf("only %d/%d delivered", len(s), n)
	return nil
}

func TestMirrorDeliversExactlyOnceAndFastestWins(t *testing.T) {
	paths := []*simPath{{delay: 2 * time.Millisecond}, {delay: 15 * time.Millisecond}}
	a, b, got := pair(t, paths)
	b.SetReorderHold(30 * time.Millisecond)
	const n = 400
	for i := uint32(1); i <= n; i++ {
		if c := a.Send(fakePkt(i)); c != 2 {
			t.Fatalf("smart mode with 2 links should mirror, sent %d copies", c)
		}
	}
	seqs := waitFor(t, got, n)
	if len(seqs) != n {
		t.Fatalf("delivered %d want %d (duplicates leaked?)", len(seqs), n)
	}
	for i, s := range seqs {
		if s != uint32(i+1) {
			t.Fatalf("out of order at %d: %d", i, s)
		}
	}
	fast, slow := b.Link(1), b.Link(2)
	if fast.Wins.Load() < n*9/10 {
		t.Fatalf("fast link should win nearly every race: fast=%d slow=%d", fast.Wins.Load(), slow.Wins.Load())
	}
	if slow.Dups.Load() < n*9/10 {
		t.Fatalf("slow link copies should be discarded: dups=%d", slow.Dups.Load())
	}
}

// Three links, redundancy 2, 10% random loss on each: every packet survives
// unless both of its copies are lost (1%), and the reorder buffer keeps order.
func TestStripedRedundancySurvivesLoss(t *testing.T) {
	paths := []*simPath{
		{delay: 3 * time.Millisecond, loss: 0.10},
		{delay: 6 * time.Millisecond, loss: 0.10},
		{delay: 12 * time.Millisecond, loss: 0.10},
	}
	a, b, got := pair(t, paths)
	b.SetReorderHold(40 * time.Millisecond)
	a.SetPolicy(sched.Policy{Mode: sched.ModeSmart, Redundancy: 2})
	const n = 3000
	copies := 0
	for i := uint32(1); i <= n; i++ {
		copies += a.Send(fakePkt(i))
		if i%100 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	if copies != 2*n {
		t.Fatalf("expected 2 copies per packet, got %d", copies)
	}
	time.Sleep(300 * time.Millisecond)
	seqs := got.snapshot()
	lossRate := 1 - float64(len(seqs))/n
	if lossRate > 0.03 {
		t.Fatalf("effective loss %.2f%% too high (expected ~1%%)", lossRate*100)
	}
	seen := map[uint32]bool{}
	last := uint32(0)
	for _, s := range seqs {
		if seen[s] {
			t.Fatalf("duplicate delivery of %d", s)
		}
		seen[s] = true
		if s < last {
			t.Fatalf("reordering leaked: %d after %d", s, last)
		}
		last = s
	}
}

func TestFailoverWhenLinkDies(t *testing.T) {
	paths := []*simPath{{delay: 2 * time.Millisecond}, {delay: 4 * time.Millisecond}, {delay: 6 * time.Millisecond}}
	a, _, got := pair(t, paths)
	a.SetPolicy(sched.Policy{Mode: sched.ModeSpeed})
	for i := uint32(1); i <= 100; i++ {
		a.Send(fakePkt(i))
	}
	// Link 1 dies; health monitoring marks it down.
	paths[0].mu.Lock()
	paths[0].dead = true
	paths[0].mu.Unlock()
	a.Link(1).SetState(StateDown)
	for i := uint32(101); i <= 200; i++ {
		a.Send(fakePkt(i))
	}
	seqs := waitFor(t, got, 160)
	// Packets 1..100 that were striped onto link 1 before it died are lost
	// (speed mode has no redundancy) but all of 101..200 must arrive.
	have := map[uint32]bool{}
	for _, s := range seqs {
		have[s] = true
	}
	for i := uint32(101); i <= 200; i++ {
		if !have[i] {
			t.Fatalf("packet %d sent after failover was lost", i)
		}
	}
}

func TestBackupOnlyUsedWhenPrimariesDown(t *testing.T) {
	paths := []*simPath{{delay: time.Millisecond}, {delay: time.Millisecond}}
	a, b, _ := pair(t, paths)
	a.Link(2).SetBackup(true)
	for i := uint32(1); i <= 50; i++ {
		a.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if b.Link(2).Rx.Packets.Load() != 0 {
		t.Fatal("backup link used while primary is up")
	}
	a.Link(1).SetState(StateDown)
	for i := uint32(51); i <= 100; i++ {
		a.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if b.Link(2).Rx.Packets.Load() != 50 {
		t.Fatalf("backup should carry traffic now, got %d", b.Link(2).Rx.Packets.Load())
	}
}

func TestNoUsableLinkCountsDrop(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	e.AddLink(1, "x", "sim", func([]byte) error { return nil }) // connecting
	if e.Send(fakePkt(1)) != 0 || e.NoLink.Load() != 1 {
		t.Fatal("expected drop with no usable link")
	}
}

func TestClassify(t *testing.T) {
	p := HealthParams{DegradedRTT: 200 * time.Millisecond, DegradedLoss: 0.03, DownLoss: 0.5, DownAfter: 2 * time.Second}
	cases := []struct {
		acked bool
		since time.Duration
		rtt   time.Duration
		loss  float64
		want  State
	}{
		{false, time.Second, 0, 0, StateConnecting},
		{false, 3 * time.Second, 0, 0, StateDown},
		{true, 100 * time.Millisecond, 30 * time.Millisecond, 0, StateUp},
		{true, 100 * time.Millisecond, 300 * time.Millisecond, 0, StateDegraded},
		{true, 100 * time.Millisecond, 30 * time.Millisecond, 0.05, StateDegraded},
		{true, 100 * time.Millisecond, 30 * time.Millisecond, 0.6, StateDown},
		{true, 3 * time.Second, 30 * time.Millisecond, 0, StateDown},
	}
	for i, c := range cases {
		if got := Classify(p, c.acked, c.since, c.rtt, c.loss); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
	}
}

func TestAutoWeight(t *testing.T) {
	fast := AutoWeight(120e6, 26*time.Millisecond, 26*time.Millisecond, true)
	slow := AutoWeight(30e6, 99*time.Millisecond, 26*time.Millisecond, true)
	if fast != 120 {
		t.Fatalf("fast=%v", fast)
	}
	if slow >= 30*0.3+0.01 || slow < 30*0.26 {
		t.Fatalf("slow=%v", slow)
	}
	if AutoWeight(0, 0, 0, true) != 1 {
		t.Fatal("unknown capacity should get weight 1")
	}
}

func TestAutoReorderHold(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	for i, r := range []time.Duration{26, 99} {
		l := e.AddLink(uint8(i+1), "x", "sim", func([]byte) error { return nil })
		l.SetState(StateUp)
		l.Health(func(h *Health) { h.RTT.Update(r * time.Millisecond) })
	}
	d := e.AutoReorderHold(time.Second)
	// RTTVar after one sample is rtt/2, so spread = (99+99)-(26+26) = 146ms, half = 73 + 5.
	if d < 70*time.Millisecond || d > 85*time.Millisecond {
		t.Fatalf("hold %v", d)
	}
	if e.AutoReorderHold(20*time.Millisecond) != 20*time.Millisecond {
		t.Fatal("cap not applied")
	}
}

func TestTickRecordsHistoryAndWinShare(t *testing.T) {
	paths := []*simPath{{delay: time.Millisecond}, {delay: 8 * time.Millisecond}}
	a, b, got := pair(t, paths)
	now := time.Now()
	b.Tick(now)
	for i := uint32(1); i <= 100; i++ {
		a.Send(fakePkt(i))
	}
	waitFor(t, got, 100)
	time.Sleep(20 * time.Millisecond)
	b.Tick(now.Add(time.Second))
	var share float64
	var hist int
	b.Link(1).Health(func(h *Health) { share = h.WinShare; hist = len(h.History.Snapshot(0)) })
	if share < 0.9 || hist != 2 {
		t.Fatalf("share=%v hist=%d", share, hist)
	}
	if b.RxBps.Load() == 0 {
		t.Fatal("rx rate not computed")
	}
}

func TestSilentLinkIsAvoidedWhileBondIsBusy(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var sent [3]atomic.Int64
	for i := 0; i < 3; i++ {
		i := i
		l := e.AddLink(uint8(i+1), "l", "sim", func([]byte) error { sent[i].Add(1); return nil })
		l.SetState(StateUp)
		l.SetOneWayDelay(10 * time.Millisecond)
		l.rxBusy.Store(true)
	}
	now := time.Now().UnixNano()
	e.Link(1).LastRx.Store(now - int64(400*time.Millisecond)) // cable pulled 400ms ago
	e.Link(2).LastRx.Store(now)
	e.Link(3).LastRx.Store(now)
	e.SetPolicy(sched.Policy{Mode: sched.ModeSpeed})
	for i := uint32(0); i < 50; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(20 * time.Millisecond)
	if sent[0].Load() != 0 || sent[1].Load()+sent[2].Load() != 50 {
		t.Fatalf("silent link should lose the race: %d %d %d", sent[0].Load(), sent[1].Load(), sent[2].Load())
	}
	if !e.Link(1).Silent(now) {
		t.Fatal("link 1 should read silent")
	}
}

// Field regression (Starlink): a 300ms handover gap on the good link must not
// push traffic onto a link with 1.5s one-way latency, and a brief moment
// where every link is quiet must not drop packets.
func TestBriefSilenceKeepsTrafficOnGoodLink(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var sent [2]atomic.Int64
	for i := 0; i < 2; i++ {
		i := i
		l := e.AddLink(uint8(i+1), "l", "sim", func([]byte) error { sent[i].Add(1); return nil })
		l.SetState(StateUp)
		l.rxBusy.Store(true)
	}
	e.Link(1).SetOneWayDelay(34 * time.Millisecond)   // Starlink
	e.Link(2).SetOneWayDelay(1600 * time.Millisecond) // campground Wi-Fi
	now := time.Now().UnixNano()
	e.Link(1).LastRx.Store(now - int64(300*time.Millisecond)) // handover blip
	e.Link(2).LastRx.Store(now - int64(10*time.Millisecond))
	e.SetPolicy(sched.Policy{Mode: sched.ModeSpeed})
	for i := uint32(0); i < 100; i++ {
		e.Send(fakePkt(i))
	}
	// Now both links quiet for a moment: nothing may be dropped.
	e.Link(2).LastRx.Store(now - int64(2*time.Second))
	for i := uint32(100); i < 150; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if sent[0].Load() != 150 || sent[1].Load() != 0 {
		t.Fatalf("traffic left the good link during a blip: good=%d bad=%d", sent[0].Load(), sent[1].Load())
	}
	if e.NoLink.Load() != 0 {
		t.Fatalf("%d packets dropped while links were briefly quiet", e.NoLink.Load())
	}
}

// Regression (field report): a healthy link whose pacer was deep in debt
// looked "slower" than a 2-second link, so traffic moved to the bad link.
// The primary copy must always take the genuinely fastest path.
func TestPrimaryCopyIgnoresPacingDebt(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var sent [2]atomic.Int64
	for i := 0; i < 2; i++ {
		i := i
		l := e.AddLink(uint8(i+1), "l", "sim", func([]byte) error { sent[i].Add(1); return nil })
		l.SetState(StateUp)
		l.Pacer.Configure(true, 1e6, 0)
	}
	e.Link(1).SetOneWayDelay(20 * time.Millisecond)   // Starlink
	e.Link(2).SetOneWayDelay(1000 * time.Millisecond) // awful hotspot
	e.Link(1).Pacer.Force(10<<20, time.Now())         // 10MB of pacing debt
	e.SetPolicy(sched.Policy{Mode: sched.ModeSpeed})
	for i := uint32(0); i < 200; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if sent[0].Load() != 200 || sent[1].Load() != 0 {
		t.Fatalf("primary copies went to the slow link: fast=%d slow=%d", sent[0].Load(), sent[1].Load())
	}
}

// duplex wires two engines with one lossy, delayed path in each direction
// and dispatches DATA and NACK frames like the real client/server do.
func duplex(t *testing.T, delay time.Duration, loss float64) (a, b *Engine, got *collector) {
	t.Helper()
	got = &collector{}
	a = New(1, func([]byte) {})
	b = New(1, got.deliver)
	rng := rand.New(rand.NewSource(11))
	var mu sync.Mutex
	var la, lb *Link
	path := func(dst *Engine, dstLink func() *Link) func([]byte) error {
		return func(f []byte) error {
			mu.Lock()
			drop := rng.Float64() < loss
			mu.Unlock()
			if drop {
				return nil
			}
			c := append([]byte(nil), f...)
			time.AfterFunc(delay, func() {
				h, err := proto.ParseHeader(c)
				if err != nil {
					return
				}
				switch h.Type {
				case proto.TypeData:
					dst.Receive(dstLink(), c)
				case proto.TypeNack:
					dst.HandleNack(c)
				case proto.TypeFEC:
					dst.ReceiveFEC(dstLink(), c)
				}
			})
			return nil
		}
	}
	la = a.AddLink(1, "l", "sim", path(b, func() *Link { return lb }))
	lb = b.AddLink(1, "l", "sim", path(a, func() *Link { return la }))
	for _, l := range []*Link{la, lb} {
		l.SetState(StateUp)
		l.SetOneWayDelay(delay)
		l.Health(func(h *Health) { h.RTT.Update(2 * delay) })
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, got
}

func TestARQRecoversLossTransparently(t *testing.T) {
	a, b, got := duplex(t, 20*time.Millisecond, 0.05)
	a.SetARQ(true)
	b.SetARQ(true)
	b.SetReorderHold(b.ARQHold(400 * time.Millisecond))
	const n = 2000
	for i := uint32(1); i <= n; i++ {
		a.Send(fakePkt(i))
		if i%50 == 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(got.snapshot()) < n {
		time.Sleep(20 * time.Millisecond)
	}
	seqs := got.snapshot()
	if missing := n - len(seqs); missing > n/200 { // <0.5% left vs 5% raw loss
		t.Fatalf("ARQ left %d of %d packets missing (raw loss 5%%); retransmits=%d nacks=%d",
			missing, n, a.Retransmits.Load(), b.NacksSent.Load())
	}
	// A repair that itself needed a second NACK round can land after the
	// reorder hold gave up: delivered slightly out of order, which TCP
	// tolerates. It must stay rare.
	inversions := 0
	for i := 1; i < len(seqs); i++ {
		if seqs[i] < seqs[i-1] {
			inversions++
		}
	}
	if inversions > n/100 {
		t.Fatalf("%d out-of-order deliveries (>1%%)", inversions)
	}
	if a.Retransmits.Load() == 0 || b.NacksSent.Load() == 0 {
		t.Fatal("no retransmissions happened")
	}
	t.Logf("delivered %d/%d, out-of-order %d, retransmits=%d, nacks=%d", len(seqs), n, inversions, a.Retransmits.Load(), b.NacksSent.Load())
}

func TestARQOffMeansNoNacks(t *testing.T) {
	a, b, got := duplex(t, 5*time.Millisecond, 0.05)
	for i := uint32(1); i <= 500; i++ {
		a.Send(fakePkt(i))
	}
	time.Sleep(200 * time.Millisecond)
	if b.NacksSent.Load() != 0 || a.Retransmits.Load() != 0 {
		t.Fatal("ARQ activity while disabled")
	}
	if l := len(got.snapshot()); l > 490 {
		t.Fatalf("expected visible loss without ARQ, delivered %d/500", l)
	}
}

// Field regression: a slow link that only carries duplicates must not set the
// reorder hold (it made every gap wait ~300ms and triggered spurious TCP
// timeouts at the far end).
func TestReorderHoldIgnoresDuplicateOnlyLinks(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	fast := e.AddLink(1, "starlink", "sim", func([]byte) error { return nil })
	slow := e.AddLink(2, "campground", "sim", func([]byte) error { return nil })
	for _, l := range []*Link{fast, slow} {
		l.SetState(StateUp)
	}
	fast.Health(func(h *Health) { h.RTT.Update(80 * time.Millisecond); h.WinShare = 0.99 })
	slow.Health(func(h *Health) { h.RTT.Update(990 * time.Millisecond); h.WinShare = 0.0 })
	if d := e.AutoReorderHold(300 * time.Millisecond); d > 50*time.Millisecond {
		t.Fatalf("hold %v driven by a duplicate-only link", d)
	}
	fast.SetOneWayDelay(40 * time.Millisecond)
	slow.SetOneWayDelay(495 * time.Millisecond)
	// ARQ hold covers ~2.5 RTTs of the fast path (repair incl. a retry),
	// independent of the slow link, and respects the configured cap.
	if d := e.ARQHold(2 * time.Second); d < 200*time.Millisecond || d > 320*time.Millisecond {
		t.Fatalf("ARQ hold %v, want ~2.5 x 80ms RTT + margins", d)
	}
	if d := e.ARQHold(150 * time.Millisecond); d != 150*time.Millisecond {
		t.Fatalf("cap not applied: %v", d)
	}
}

// Field regression (Starlink + 5s campground Wi-Fi): the Wi-Fi wins a few
// races only when Starlink drops a packet. It must not stretch the NACK wait
// (it pushed the wait to ~2.5s, so every repair arrived after the hold).
func TestNackWaitIgnoresSlowBackupWinners(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	star := e.AddLink(1, "eth0", "sim", func([]byte) error { return nil })
	wifi := e.AddLink(2, "sta0", "sim", func([]byte) error { return nil })
	star.SetState(StateUp)
	wifi.SetState(StateDegraded)
	star.SetOneWayDelay(47 * time.Millisecond)
	wifi.SetOneWayDelay(2500 * time.Millisecond)
	star.Health(func(h *Health) {
		h.RTT.Update(95 * time.Millisecond)
		h.RTT.Update(90 * time.Millisecond)
		h.WinShare = 0.95
	})
	wifi.Health(func(h *Health) { h.RTT.Update(5 * time.Second); h.WinShare = 0.05 })
	wait, _ := e.nackTiming(time.Now())
	if wait > 60*time.Millisecond {
		t.Fatalf("NACK wait %v stretched by the slow backup link", wait)
	}
	if hold := e.ARQHold(400 * time.Millisecond); wait+95*time.Millisecond > hold {
		t.Fatalf("repair (wait %v + RTT) cannot arrive within hold %v", wait, hold)
	}
}

// Metered phone link: in mirror mode it carries only its share (10%) of
// traffic; when the main link dies it carries everything (failover first).
func TestMeteredLinkShareAndFailover(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var sent [2]atomic.Int64
	star := e.AddLink(1, "eth0", "sim", func([]byte) error { sent[0].Add(1); return nil })
	phone := e.AddLink(2, "eth3", "sim", func([]byte) error { sent[1].Add(1); return nil })
	star.SetState(StateUp)
	phone.SetState(StateUp)
	star.SetOneWayDelay(40 * time.Millisecond)
	phone.SetOneWayDelay(55 * time.Millisecond)
	phone.SetMetered(true, 0.10)
	e.SetPolicy(sched.Policy{Mode: sched.ModeMirror})
	const n = 3000
	for i := uint32(0); i < n; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(50 * time.Millisecond)
	share := float64(sent[1].Load()) / n
	if sent[0].Load() != n || share < 0.07 || share > 0.13 {
		t.Fatalf("mirror with metered phone: starlink %d/%d, phone share %.2f (want ~0.10)", sent[0].Load(), n, share)
	}
	// Cap reached: failover only (share 0) -> no copies while Starlink works.
	phone.SetMetered(true, 0)
	before := sent[1].Load()
	for i := uint32(0); i < 500; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if sent[1].Load() != before {
		t.Fatalf("over-cap phone still got %d copies", sent[1].Load()-before)
	}
	// Starlink barely working (a trickle): not a real alternative, so the
	// phone carries traffic regardless of its share.
	star.Pacer.Configure(true, 1e6, 1e6)
	before = sent[1].Load()
	for i := uint32(0); i < 500; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if got := sent[1].Load() - before; got < 400 {
		t.Fatalf("trickling starlink: phone carried only %d/500", got)
	}
	star.Pacer.Configure(false, 0, 0)
	// Starlink dies: the phone carries everything despite cap.
	star.SetState(StateDown)
	before = sent[1].Load()
	for i := uint32(0); i < 500; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(30 * time.Millisecond)
	if got := sent[1].Load() - before; got != 500 || e.NoLink.Load() != 0 {
		t.Fatalf("failover: phone carried %d/500, dropped %d", got, e.NoLink.Load())
	}
}

// A call is mirrored on a benched link too (the campground Wi-Fi flapping
// next to Starlink): bulk traffic stays off it.
func TestCallMirroredOnBenchedLink(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var sent [2]atomic.Int64
	star := e.AddLink(1, "eth0", "sim", func([]byte) error { sent[0].Add(1); return nil })
	wifi := e.AddLink(2, "sta1", "sim", func([]byte) error { sent[1].Add(1); return nil })
	star.SetState(StateUp)
	wifi.SetState(StateUp)
	wifi.SetBenched(true)
	ports, _ := sched.ParsePorts("3478-3497")
	e.SetPolicy(sched.Policy{Mode: sched.ModeSmart, RealtimeMirror: true, RealtimePorts: ports})
	if !e.RealtimeAt().IsZero() {
		t.Fatal("call seen before any call packet")
	}
	for i := uint32(0); i < 200; i++ {
		e.Send(fakePkt(i)) // TCP bulk
	}
	time.Sleep(30 * time.Millisecond)
	if sent[1].Load() != 0 {
		t.Fatalf("bulk traffic went over the benched link: %d", sent[1].Load())
	}
	voice := make([]byte, 200)
	voice[0] = 0x45
	binary.BigEndian.PutUint16(voice[2:], uint16(len(voice)))
	voice[9] = 17
	binary.BigEndian.PutUint16(voice[20:], 50012)
	binary.BigEndian.PutUint16(voice[22:], 3479)
	for i := 0; i < 100; i++ {
		e.Send(voice)
	}
	time.Sleep(30 * time.Millisecond)
	if got := sent[1].Load(); got < 95 {
		t.Fatalf("call copies on the benched link: %d/100", got)
	}
	if time.Since(e.RealtimeAt()) > time.Second {
		t.Fatal("call not noticed")
	}
}
