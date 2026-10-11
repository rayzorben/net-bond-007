package reorder

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func p(seq uint64) []byte { return []byte{byte(seq), byte(seq >> 8)} }

func seqOf(b []byte) uint64 { return uint64(b[0]) | uint64(b[1])<<8 }

type sink struct{ got []uint64 }

func (s *sink) deliver(b []byte) { s.got = append(s.got, seqOf(b)) }

func TestPassThroughWhenDisabled(t *testing.T) {
	var b Buffer
	var s sink
	now := time.Now()
	for _, q := range []uint64{1, 3, 2} {
		b.Push(q, p(q), now, s.deliver)
	}
	if !reflect.DeepEqual(s.got, []uint64{1, 3, 2}) {
		t.Fatalf("got %v", s.got)
	}
}

func TestReordersWithinHold(t *testing.T) {
	b := Buffer{MaxHold: 50 * time.Millisecond}
	var s sink
	now := time.Now()
	for _, q := range []uint64{1, 3, 4, 2, 5} {
		b.Push(q, p(q), now, s.deliver)
	}
	if !reflect.DeepEqual(s.got, []uint64{1, 2, 3, 4, 5}) {
		t.Fatalf("got %v", s.got)
	}
	if b.Held() != 0 || b.Stats.Held != 2 {
		t.Fatalf("held=%d stats=%+v", b.Held(), b.Stats)
	}
}

func TestSkipsGapAfterTimeout(t *testing.T) {
	b := Buffer{MaxHold: 20 * time.Millisecond}
	var s sink
	t0 := time.Now()
	b.Push(1, p(1), t0, s.deliver)
	b.Push(3, p(3), t0, s.deliver) // 2 lost on every link
	b.Push(4, p(4), t0.Add(5*time.Millisecond), s.deliver)
	if len(s.got) != 1 {
		t.Fatalf("should be holding, got %v", s.got)
	}
	if d := b.NextDeadline(); !d.Equal(t0.Add(20 * time.Millisecond)) {
		t.Fatalf("deadline %v", d)
	}
	b.Expire(t0.Add(10*time.Millisecond), s.deliver)
	if len(s.got) != 1 {
		t.Fatal("expired too early")
	}
	b.Expire(t0.Add(21*time.Millisecond), s.deliver)
	if !reflect.DeepEqual(s.got, []uint64{1, 3, 4}) {
		t.Fatalf("got %v", s.got)
	}
	if b.Stats.Skipped != 1 {
		t.Fatalf("stats %+v", b.Stats)
	}
	// The lost packet shows up late after all: deliver immediately.
	b.Push(2, p(2), t0.Add(30*time.Millisecond), s.deliver)
	if s.got[len(s.got)-1] != 2 || b.Stats.Late != 1 {
		t.Fatalf("late handling: %v %+v", s.got, b.Stats)
	}
}

func TestFarAheadForcesSkip(t *testing.T) {
	b := Buffer{MaxHold: time.Second}
	var s sink
	now := time.Now()
	b.Push(1, p(1), now, s.deliver)
	b.Push(10, p(10), now, s.deliver)
	b.Push(10+slots+5, p(10+slots+5), now, s.deliver)
	// 10 must have been released (gap 2..9 skipped) to make room.
	if len(s.got) < 2 || s.got[1] != 10 {
		t.Fatalf("got %v", s.got)
	}
	if b.Held() != 1 {
		t.Fatalf("held %d", b.Held())
	}
}

func TestDisablingFlushesHeld(t *testing.T) {
	b := Buffer{MaxHold: time.Second}
	var s sink
	now := time.Now()
	b.Push(1, p(1), now, s.deliver)
	b.Push(3, p(3), now, s.deliver)
	b.MaxHold = 0
	b.Push(4, p(4), now, s.deliver)
	if !reflect.DeepEqual(s.got, []uint64{1, 3, 4}) {
		t.Fatalf("got %v", s.got)
	}
}

// Random interleaving from three links of different latency must come out
// in perfect order when nothing is lost.
func TestRandomStripingComesOutInOrder(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	b := Buffer{MaxHold: 100 * time.Millisecond}
	var s sink
	type ev struct {
		at  time.Duration
		seq uint64
	}
	lat := []time.Duration{13, 23, 50}
	var evs []ev
	for q := uint64(1); q <= 5000; q++ {
		l := lat[r.Intn(3)] * time.Millisecond
		evs = append(evs, ev{time.Duration(q)*100*time.Microsecond + l, q})
	}
	// sort by arrival time
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && evs[j].at < evs[j-1].at; j-- {
			evs[j], evs[j-1] = evs[j-1], evs[j]
		}
	}
	t0 := time.Now()
	for _, e := range evs {
		now := t0.Add(e.at)
		b.Expire(now, s.deliver)
		b.Push(e.seq, p(e.seq), now, s.deliver)
	}
	if len(s.got) != 5000 {
		t.Fatalf("delivered %d", len(s.got))
	}
	for i, q := range s.got {
		if q&0xffff != uint64(i+1)&0xffff {
			t.Fatalf("out of order at %d: %d", i, q)
		}
	}
	if b.Stats.Skipped != 0 {
		t.Fatalf("unexpected skips %+v", b.Stats)
	}
}
