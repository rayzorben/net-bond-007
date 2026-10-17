package bond

import (
	"bytes"
	"crypto/subtle"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
)

type fecFrame struct {
	seqs   []uint64
	lenXor uint16
	parity []byte
}

func capture(out *[]fecFrame) func([]uint64, uint16, []byte) {
	return func(seqs []uint64, lx uint16, parity []byte) {
		*out = append(*out, fecFrame{append([]uint64(nil), seqs...), lx, append([]byte(nil), parity...)})
	}
}

func ipPkt(size int, fill byte) []byte {
	p := bytes.Repeat([]byte{fill}, size)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(size))
	return p
}

func TestFECEncoderGroupsAndInterleaves(t *testing.T) {
	var f fecEncoder
	var got []fecFrame
	now := time.Now()
	f.add(1, ipPkt(100, 1), now, capture(&got))
	if len(got) != 0 {
		t.Fatal("FEC emitted while off")
	}
	f.setK(3)
	pkts := map[uint64][]byte{}
	for seq := uint64(1); seq <= 6; seq++ {
		pkts[seq] = ipPkt(60+int(seq)*10, byte(seq))
		f.add(seq, pkts[seq], now, capture(&got))
	}
	// Interleaved: odd seqs in one group, even in the other.
	if len(got) != 2 || !equalSeqs(got[0].seqs, []uint64{1, 3, 5}) || !equalSeqs(got[1].seqs, []uint64{2, 4, 6}) {
		t.Fatalf("groups %+v", got)
	}
	for _, g := range got {
		// Parity is as long as the longest member; XOR-ing the members back
		// out leaves zeros, and the lengths cancel.
		p := append([]byte(nil), g.parity...)
		lx := g.lenXor
		for _, s := range g.seqs {
			subtle.XORBytes(p, p[:len(pkts[s])], pkts[s])
			lx ^= uint16(len(pkts[s]))
		}
		if lx != 0 || !bytes.Equal(p, make([]byte, len(p))) || len(p) != len(pkts[g.seqs[2]]) {
			t.Fatalf("bad parity for %v", g.seqs)
		}
	}
}

func TestFECEncoderFlushesOldGroupsAndSkipsSingles(t *testing.T) {
	var f fecEncoder
	var got []fecFrame
	f.setK(16)
	now := time.Now()
	f.add(10, ipPkt(80, 1), now, capture(&got))
	f.add(12, ipPkt(80, 2), now, capture(&got)) // other group
	f.add(11, ipPkt(80, 3), now, capture(&got)) // first group, out of order
	f.flushOld(now.Add(fecMaxAge/2), capture(&got))
	if len(got) != 0 {
		t.Fatal("flushed a young group")
	}
	f.flushOld(now.Add(fecMaxAge), capture(&got))
	// The group of two goes out (sorted); the lone packet is left to ARQ.
	if len(got) != 1 || !equalSeqs(got[0].seqs, []uint64{10, 11}) {
		t.Fatalf("flushed %+v", got)
	}
	f.setK(0)
	f.add(13, ipPkt(80, 4), now, capture(&got))
	f.flushOld(now.Add(time.Second), capture(&got))
	if len(got) != 1 {
		t.Fatal("FEC still active after setK(0)")
	}
}

func equalSeqs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A receiver missing one packet of a group rebuilds it from the parity at
// once; with two missing it does nothing (ARQ's job).
func TestReceiveFECRebuildsSingleLoss(t *testing.T) {
	var mu sync.Mutex
	var delivered [][]byte
	b := New(1, func(p []byte) {
		mu.Lock()
		delivered = append(delivered, append([]byte(nil), p...))
		mu.Unlock()
	})
	defer b.Close()
	b.SetFEC(true) // negotiated: keep received payloads for rebuilding
	bl := b.AddLink(1, "l", "sim", func([]byte) error { return nil })
	bl.SetState(StateUp)

	pkts := [][]byte{ipPkt(100, 1), ipPkt(300, 2), ipPkt(200, 3), ipPkt(250, 4)}
	var enc fecEncoder
	var groups []fecFrame
	enc.setK(len(pkts))
	for i, p := range pkts {
		enc.add(uint64(i+1), p, time.Now(), capture(&groups))
		enc.add(uint64(i+1), p, time.Now(), func([]uint64, uint16, []byte) {}) // keep both interleave slots in step
	}
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	g := groups[0]
	// Seq 2 (the longest) is lost; the rest arrive.
	for _, i := range []int{0, 2, 3} {
		b.Receive(bl, proto.AppendData(nil, 1, 1, uint64(i+1), pkts[i]))
	}
	b.ReceiveFEC(bl, proto.AppendFEC(nil, 1, 1, g.seqs, g.lenXor, g.parity))
	mu.Lock()
	n := len(delivered)
	var last []byte
	if n > 0 {
		last = delivered[n-1]
	}
	mu.Unlock()
	if b.FECRecovered.Load() != 1 || bl.FECRecovered.Load() != 1 {
		t.Fatalf("not rebuilt: recovered=%d bad=%d", b.FECRecovered.Load(), b.FECBad.Load())
	}
	if n != 4 || !bytes.Equal(last, pkts[1]) && !containsPkt(delivered, pkts[1]) {
		t.Fatalf("rebuilt packet not delivered intact (%d delivered)", n)
	}
	// The real packet turning up later is just a duplicate.
	b.Receive(bl, proto.AppendData(nil, 1, 1, 2, pkts[1]))
	if bl.Dups.Load() != 1 {
		t.Fatalf("late original not treated as duplicate: dups=%d", bl.Dups.Load())
	}

	// Two missing: nothing can be rebuilt.
	var groups2 []fecFrame
	var enc2 fecEncoder
	enc2.setK(4)
	for i, p := range pkts {
		enc2.add(uint64(i+11), p, time.Now(), capture(&groups2))
		enc2.add(uint64(i+11), p, time.Now(), func([]uint64, uint16, []byte) {})
	}
	b.Receive(bl, proto.AppendData(nil, 1, 1, 11, pkts[0]))
	b.Receive(bl, proto.AppendData(nil, 1, 1, 12, pkts[1]))
	g2 := groups2[0]
	b.ReceiveFEC(bl, proto.AppendFEC(nil, 1, 1, g2.seqs, g2.lenXor, g2.parity))
	if b.FECRecovered.Load() != 1 {
		t.Fatal("rebuilt from a group missing two packets")
	}
}

func containsPkt(list [][]byte, p []byte) bool {
	for _, q := range list {
		if bytes.Equal(q, p) {
			return true
		}
	}
	return false
}

// A parity that doesn't match the stored packets must never inject garbage.
func TestReceiveFECRejectsMismatchedGroup(t *testing.T) {
	b := New(1, func([]byte) {})
	defer b.Close()
	b.SetFEC(true)
	bl := b.AddLink(1, "l", "sim", func([]byte) error { return nil })
	bl.SetState(StateUp)
	b.Receive(bl, proto.AppendData(nil, 1, 1, 1, ipPkt(200, 7)))
	b.ReceiveFEC(bl, proto.AppendFEC(nil, 1, 1, []uint64{1, 2}, 0x1234, bytes.Repeat([]byte{0x99}, 200)))
	if b.FECRecovered.Load() != 0 || b.FECBad.Load() != 1 {
		t.Fatalf("garbage accepted: recovered=%d bad=%d", b.FECRecovered.Load(), b.FECBad.Load())
	}
	if plausibleIP([]byte{0x45, 0, 0, 20}) || !plausibleIP(ipPkt(20, 0)) {
		t.Fatal("plausibleIP")
	}
}

// End to end over a lossy path with ARQ off: FEC alone rebuilds most losses.
func TestFECRecoversRandomLossWithoutARQ(t *testing.T) {
	a, b, got := duplex(t, 10*time.Millisecond, 0.01)
	a.SetFEC(true)
	b.SetFEC(true)
	a.Links()[0].fec.setK(16)
	const n = 3000
	for i := uint32(1); i <= n; i++ {
		a.Send(fakePkt(i))
		if i%50 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(200 * time.Millisecond)
	delivered := len(got.snapshot())
	// ~1% raw loss (30 packets) plus parity loss; FEC should leave < 0.3%.
	if missing := n - delivered; missing > n*3/1000 {
		t.Fatalf("%d of %d missing with FEC (recovered %d, sender drops %d, parity sent %d, pending %d)", missing, n, b.FECRecovered.Load(), a.Links()[0].Drops.Load(), a.Links()[0].FECSent.Load(), len(b.fecRx.pending))
	}
	if b.FECRecovered.Load() == 0 || a.Links()[0].FECSent.Load() == 0 {
		t.Fatal("no FEC happened")
	}
	t.Logf("delivered %d/%d, FEC rebuilt %d", delivered, n, b.FECRecovered.Load())
}

func TestFECSizingFollowsLoss(t *testing.T) {
	if fecGroupFor(0) != 0 || fecGroupFor(0.002) != 32 || fecGroupFor(0.005) != 16 || fecGroupFor(0.05) != 8 {
		t.Fatalf("fecGroupFor: %d %d %d", fecGroupFor(0.002), fecGroupFor(0.005), fecGroupFor(0.05))
	}
	e := New(1, func([]byte) {})
	defer e.Close()
	l := e.AddLink(1, "l", "sim", func([]byte) error { return nil })
	now := time.Now()
	tick := func(pkts, lost uint64) {
		for i := uint64(0); i < pkts; i++ {
			l.Tx.Add(1400)
		}
		l.lossLost.Add(lost)
		now = now.Add(time.Second)
		e.updateFEC(l, now)
	}
	tick(10000, 0)
	if l.FECGroup() != 0 {
		t.Fatal("FEC on for a clean link")
	}
	for i := 0; i < 5; i++ {
		tick(10000, 20) // 0.2% loss
	}
	if k := l.FECGroup(); k < 24 {
		t.Fatalf("0.2%% loss: group %d, want ~32", k)
	}
	// FEC hides the loss from ARQ, but the peer's reported rebuilds keep it on.
	for i := 0; i < 40; i++ {
		l.SetPeerFECRecovered(uint32(20 * (i + 1)))
		tick(10000, 0)
	}
	if l.FECGroup() == 0 {
		t.Fatal("FEC switched itself off while it was repairing loss")
	}
	tick(50, 50) // too few packets to judge: unchanged
	k := l.FECGroup()
	for i := 0; i < 5; i++ {
		tick(10, 0)
	}
	if l.FECGroup() != k {
		t.Fatal("idle seconds changed the FEC size")
	}
	for i := 0; i < int(fecOffAfter/time.Second)+10; i++ {
		tick(10000, 0) // clean again (peer counter unchanged)
	}
	if l.FECGroup() != 0 {
		t.Fatalf("FEC still on after the link stayed clean: %d", l.FECGroup())
	}
}

// Repairs are sent ahead of data already waiting in the link's queue.
func TestRepairsJumpTheQueue(t *testing.T) {
	gate := make(chan struct{})
	var mu sync.Mutex
	var order []byte
	e := New(1, func([]byte) {})
	defer e.Close()
	l := e.AddLink(1, "l", "sim", func(f []byte) error {
		<-gate
		mu.Lock()
		order = append(order, f[len(f)-1])
		mu.Unlock()
		return nil
	})
	frame := func(tag byte) []byte {
		f := e.pool.Get().([]byte)[:0]
		return append(f, 0xB7, tag)
	}
	l.enqueue(frame('a')) // the writer takes this one and blocks on the gate
	time.Sleep(20 * time.Millisecond)
	l.enqueue(frame('b'))
	l.enqueue(frame('c'))
	l.enqueueRepair(frame('R'))
	close(gate)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(order)
		mu.Unlock()
		if n == 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(order) != "aRbc" {
		t.Fatalf("send order %q, want aRbc", order)
	}
}

func TestStormMeter(t *testing.T) {
	var m stormMeter
	now := time.Now().UnixNano()
	// Steady random loss (one every 50 ms) is not a storm.
	for i := 0; i < 40; i++ {
		m.loss(now + int64(i)*int64(50*time.Millisecond))
	}
	if m.active(now + int64(2*time.Second)) {
		t.Fatal("steady random loss read as a storm")
	}
	// 16 losses within 100 ms are.
	at := now + int64(10*time.Second)
	for i := 0; i < stormLosses; i++ {
		m.loss(at + int64(i)*int64(6*time.Millisecond))
	}
	if !m.active(at + int64(100*time.Millisecond)) {
		t.Fatal("burst not detected")
	}
	if m.active(at + int64(100*time.Millisecond) + int64(stormHold) + 1) {
		t.Fatal("storm never ended")
	}
}

// Repairs avoid a link in a loss storm while a calm link can carry them,
// even when the stormy link is faster.
func TestRepairsAvoidStormingLink(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	fast := e.AddLink(1, "starlink", "sim", func([]byte) error { return nil })
	slow := e.AddLink(2, "phone", "sim", func([]byte) error { return nil })
	fast.SetOneWayDelay(20 * time.Millisecond)
	slow.SetOneWayDelay(45 * time.Millisecond)
	for _, l := range []*Link{fast, slow} {
		l.SetState(StateUp)
	}
	now := time.Now()
	nowN := now.UnixNano()
	cands := []*Link{fast, slow}
	both := linkBit(fast.ID) | linkBit(slow.ID)
	if got := repairLinks(cands, cands, both, 0, 1400, nowN, now); len(got) == 0 || got[0] != fast {
		t.Fatalf("calm links: repair should go on the fastest first, got %v", names(got))
	}
	for i := stormLosses; i > 0; i-- {
		fast.storm.loss(nowN - int64(i)*int64(time.Millisecond))
	}
	got := repairLinks(cands, cands, both, 0, 1400, nowN, now)
	if len(got) == 0 || got[0] != slow {
		t.Fatalf("storming fastest link: repair should go on the calm one first, got %v", names(got))
	}
	// Only stormy links left: still repaired.
	for i := stormLosses; i > 0; i-- {
		slow.storm.loss(nowN - int64(i)*int64(time.Millisecond))
	}
	if got := repairLinks(cands, cands, both, 0, 1400, nowN, now); len(got) == 0 {
		t.Fatal("no repair when every link storms")
	}
}

func names(ls []*Link) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}
