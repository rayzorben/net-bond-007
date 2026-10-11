package bond

import (
	"encoding/binary"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayzorben/bond007/internal/sched"
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
