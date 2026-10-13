package bond

import (
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
)

func TestRtxBufferStoresAndExpires(t *testing.T) {
	var b rtxBuffer
	b.put(5, []byte("hello"), 0, 0)
	got, ok := b.get(5)
	if !ok || string(got) != "hello" {
		t.Fatalf("got %q %v", got, ok)
	}
	got[0] = 'X' // caller gets a copy
	if again, _ := b.get(5); string(again) != "hello" {
		t.Fatal("history was mutated through returned slice")
	}
	b.put(5+rtxSlots, []byte("newer"), 0, 0) // same slot, overwritten
	if _, ok := b.get(5); ok {
		t.Fatal("overwritten entry still returned")
	}
	if _, ok := b.get(999); ok {
		t.Fatal("unknown seq returned")
	}
}

func TestGapTrackerDetectsAndNacks(t *testing.T) {
	var g gapTracker
	t0 := time.Now()
	for _, s := range []uint64{1, 2, 3, 7, 8} { // 4,5,6 lost
		g.seen(s, t0)
	}
	wait, retry := 20*time.Millisecond, 100*time.Millisecond
	if r := g.due(t0.Add(10*time.Millisecond), wait, retry); r != nil {
		t.Fatalf("NACKed before the jitter wait: %v", r)
	}
	r := g.due(t0.Add(25*time.Millisecond), wait, retry)
	if len(r) != 1 || r[0] != (proto.SeqRange{Start: 4, Count: 3}) {
		t.Fatalf("want one range 4..6, got %v", r)
	}
	// 5 arrives via retransmission; 4 and 6 still missing -> retried later.
	g.seen(5, t0.Add(60*time.Millisecond))
	if r := g.due(t0.Add(70*time.Millisecond), wait, retry); r != nil {
		t.Fatalf("retried too early: %v", r)
	}
	r = g.due(t0.Add(130*time.Millisecond), wait, retry)
	if len(r) != 2 || r[0].Start != 4 || r[1].Start != 6 {
		t.Fatalf("want retries for 4 and 6, got %v", r)
	}
	if g.Recovered != 1 || g.Detected != 3 {
		t.Fatalf("stats: recovered=%d detected=%d", g.Recovered, g.Detected)
	}
}

func TestGapTrackerGivesUpAndIgnoresHugeJumps(t *testing.T) {
	var g gapTracker
	t0 := time.Now()
	g.seen(1, t0)
	g.seen(3, t0)
	at := t0
	for i := 0; i < 10; i++ {
		at = at.Add(200 * time.Millisecond)
		g.due(at, 10*time.Millisecond, 100*time.Millisecond)
	}
	if g.pendingCount() != 0 || g.GaveUp != 1 {
		t.Fatalf("gap not abandoned: pending=%d gaveUp=%d", g.pendingCount(), g.GaveUp)
	}
	g.seen(3+maxGapJump+10, at) // peer restarted its sequence space
	if g.pendingCount() != 0 {
		t.Fatalf("huge jump created %d bogus gaps", g.pendingCount())
	}
	// Reordered (not lost) packets fill their own gap without a NACK.
	var h gapTracker
	h.seen(10, t0)
	h.seen(12, t0)
	h.seen(11, t0.Add(5*time.Millisecond))
	if r := h.due(t0.Add(time.Second), 20*time.Millisecond, 100*time.Millisecond); r != nil {
		t.Fatalf("reordered packet was NACKed: %v", r)
	}
}
