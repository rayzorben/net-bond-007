package sched

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func cands(ws ...float64) []Candidate {
	var cs []Candidate
	for i, w := range ws {
		cs = append(cs, Candidate{ID: uint8(i + 1), Weight: w, Usable: true})
	}
	return cs
}

func TestCopiesSmart(t *testing.T) {
	p := Policy{Mode: ModeSmart, Redundancy: 2}
	big := PacketInfo{Len: 1400}
	for n, want := range map[int]int{0: 0, 1: 1, 2: 2, 3: 2, 4: 2, 6: 2} {
		if got := p.Copies(n, big); got != want {
			t.Errorf("n=%d: got %d want %d", n, got, want)
		}
	}
}

func TestCopiesModes(t *testing.T) {
	big := PacketInfo{Len: 1400}
	if got := (Policy{Mode: ModeMirror}).Copies(4, big); got != 4 {
		t.Errorf("mirror: %d", got)
	}
	if got := (Policy{Mode: ModeSpeed}).Copies(4, big); got != 1 {
		t.Errorf("speed: %d", got)
	}
	if got := (Policy{Mode: ModeCustom, Redundancy: 3}).Copies(4, big); got != 3 {
		t.Errorf("custom: %d", got)
	}
	if got := (Policy{Mode: ModeCustom, Redundancy: 9}).Copies(4, big); got != 4 {
		t.Errorf("custom clamp: %d", got)
	}
}

func TestCopiesMirrorsSmallAndRealtime(t *testing.T) {
	ports, _ := ParsePorts("3478-3481")
	p := Policy{Mode: ModeSpeed, SmallPacketMirrorBytes: 200, RealtimeMirror: true, RealtimePorts: ports}
	if got := p.Copies(4, PacketInfo{Len: 80}); got != 4 {
		t.Errorf("small packet should mirror, got %d", got)
	}
	if got := p.Copies(4, PacketInfo{Len: 1200, UDP: true, DstPort: 3479}); got != 4 {
		t.Errorf("teams media should mirror, got %d", got)
	}
	if got := p.Copies(4, PacketInfo{Len: 1200, UDP: true, DstPort: 443}); got != 1 {
		t.Errorf("quic bulk should not mirror, got %d", got)
	}
	if got := p.Copies(4, PacketInfo{Len: 1200, Priority: true}); got != 4 {
		t.Errorf("priority should mirror, got %d", got)
	}
}

// The example from the design brief: 3 healthy equal links, redundancy 2.
func TestPickRotatesPairsLikeTheBrief(t *testing.T) {
	s := New()
	cs := cands(1, 1, 1)
	var got []string
	for i := 0; i < 6; i++ {
		got = append(got, fmt.Sprint(s.Pick(cs, 2, nil)))
	}
	want := []string{"[1 2]", "[1 3]", "[2 3]", "[1 2]", "[1 3]", "[2 3]"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round %d: got %v want %v (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestPickDistinctAndProportional(t *testing.T) {
	s := New()
	// 120 / 80 / 30 Mbps links.
	cs := cands(120, 80, 30)
	counts := map[uint8]int{}
	const rounds = 23000
	for i := 0; i < rounds; i++ {
		ids := s.Pick(cs, 2, nil)
		if len(ids) != 2 || ids[0] == ids[1] {
			t.Fatalf("bad pick %v", ids)
		}
		for _, id := range ids {
			counts[id]++
		}
	}
	// Link 1 (120/230*2 > 1) is capped at one copy per packet; the remaining
	// copy per packet is split 80:30 between links 2 and 3.
	wants := []float64{rounds, rounds * 80 / 110, rounds * 30 / 110}
	for i := range wants {
		want := wants[i]
		got := float64(counts[uint8(i+1)])
		if math.Abs(got-want)/want > 0.06 {
			t.Errorf("link %d: got %v copies want ~%v", i+1, got, want)
		}
	}
}

func TestPickAllWhenRGreaterThanN(t *testing.T) {
	s := New()
	if got := s.Pick(cands(1, 1), 5, nil); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got := s.Pick(nil, 2, nil); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEligibleFallsBackToBackup(t *testing.T) {
	cs := []Candidate{
		{ID: 1, Usable: false},
		{ID: 2, Usable: true, Backup: true},
		{ID: 3, Usable: false, Backup: true},
	}
	el := Eligible(cs, nil)
	if len(el) != 1 || el[0].ID != 2 {
		t.Fatalf("expected backup link 2, got %+v", el)
	}
	cs[0].Usable = true
	el = Eligible(cs, nil)
	if len(el) != 1 || el[0].ID != 1 {
		t.Fatalf("expected primary link 1 only, got %+v", el)
	}
}

func TestDegradedGetsLessTraffic(t *testing.T) {
	s := New()
	cs := cands(1, 1)
	cs[1].Degrade = true
	counts := map[uint8]int{}
	for i := 0; i < 1000; i++ {
		for _, id := range s.Pick(cs, 1, nil) {
			counts[id]++
		}
	}
	if counts[2] >= counts[1]/2 {
		t.Fatalf("degraded link carried too much: %v", counts)
	}
}

func TestParsePorts(t *testing.T) {
	ps, err := ParsePorts(" 3478-3481, 5004 ,50010-50000")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []uint16{3478, 3481, 5004, 50000, 50005} {
		if !ps.Has(p) {
			t.Errorf("missing %d", p)
		}
	}
	for _, p := range []uint16{3477, 3482, 443} {
		if ps.Has(p) {
			t.Errorf("unexpected %d", p)
		}
	}
	if _, err := ParsePorts("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestInspect(t *testing.T) {
	// Minimal IPv4/UDP packet 10.0.0.1:5000 -> 1.1.1.1:3478
	pkt := make([]byte, 28)
	pkt[0] = 0x45
	pkt[9] = 17
	pkt[20], pkt[21] = 0x13, 0x88
	pkt[22], pkt[23] = 0x0d, 0x96
	info := Inspect(pkt)
	if !info.UDP || info.SrcPort != 5000 || info.DstPort != 3478 {
		t.Fatalf("bad udp inspect %+v", info)
	}
	// TCP SYN
	tcp := make([]byte, 40)
	tcp[0] = 0x45
	tcp[9] = 6
	tcp[20+13] = 0x02
	if !Inspect(tcp).Priority {
		t.Fatal("SYN should be priority")
	}
	tcp[20+13] = 0x10 // plain ACK
	if Inspect(tcp).Priority {
		t.Fatal("ACK should not be priority")
	}
	if Inspect([]byte{0x45}).UDP {
		t.Fatal("short packet")
	}
}

func TestPickFastestFillsFastLinksFirst(t *testing.T) {
	s := New()
	cs := []Candidate{
		{ID: 1, Usable: true, Delay: 13 * time.Millisecond},
		{ID: 2, Usable: true, Delay: 23 * time.Millisecond},
		{ID: 3, Usable: true, Delay: 50 * time.Millisecond},
	}
	if got := fmt.Sprint(s.PickFastest(cs, 1, nil)); got != "[1]" {
		t.Fatalf("speed: got %s", got)
	}
	if got := fmt.Sprint(s.PickFastest(cs, 2, nil)); got != "[1 2]" {
		t.Fatalf("redundant: got %s", got)
	}
	// Link 1 builds a 60ms backlog: link 2 is now the fastest way.
	cs[0].Delay = 73 * time.Millisecond
	if got := fmt.Sprint(s.PickFastest(cs, 1, nil)); got != "[2]" {
		t.Fatalf("after backlog: got %s", got)
	}
	// Degraded links are avoided unless much faster.
	cs[1].Degrade = true
	if got := fmt.Sprint(s.PickFastest(cs, 1, nil)); got != "[3]" {
		t.Fatalf("degraded: got %s", got)
	}
}

func TestChooseDispatchesByStrategy(t *testing.T) {
	s := New()
	cs := cands(1, 1, 1)
	cs[2].Delay = -1 // would always win under latency strategy
	p := Policy{Strategy: StrategyWeighted}
	seen := map[uint8]bool{}
	for i := 0; i < 3; i++ {
		for _, id := range s.Choose(&p, cs, 1, nil) {
			seen[id] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("weighted should rotate over all links, saw %v", seen)
	}
	p.Strategy = StrategyLatency
	if got := s.Choose(&p, cs, 1, nil); got[0] != 3 {
		t.Fatalf("latency should pick link 3, got %v", got)
	}
}
