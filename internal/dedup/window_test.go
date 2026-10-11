package dedup

import (
	"math/rand"
	"testing"
)

func TestFirstCopyWins(t *testing.T) {
	var w Window
	// The same packet arriving over four links: ethernet first.
	if w.Check(1) != Fresh {
		t.Fatal("first copy should be fresh")
	}
	for i := 0; i < 3; i++ {
		if w.Check(1) != Duplicate {
			t.Fatal("later copies should be duplicates")
		}
	}
}

func TestOutOfOrderWithinWindow(t *testing.T) {
	var w Window
	for _, s := range []uint64{5, 3, 4, 1, 2} {
		if w.Check(s) != Fresh {
			t.Fatalf("seq %d should be fresh", s)
		}
	}
	for _, s := range []uint64{1, 2, 3, 4, 5} {
		if w.Check(s) != Duplicate {
			t.Fatalf("seq %d should be dup", s)
		}
	}
}

func TestTooOld(t *testing.T) {
	var w Window
	w.Check(WindowBits + 10)
	if w.Check(5) != TooOld {
		t.Fatal("expected TooOld")
	}
	if w.Check(0) != TooOld {
		t.Fatal("seq 0 is invalid")
	}
}

func TestBigJumpClearsWindow(t *testing.T) {
	var w Window
	w.Check(100)
	w.Check(100 + 3*WindowBits)
	// 100+3W-1 was never seen and is within the window.
	if w.Check(100+3*WindowBits-1) != Fresh {
		t.Fatal("expected fresh after jump")
	}
}

func TestWrapAroundBitsAreCleared(t *testing.T) {
	var w Window
	// Fill one full window, then slide by exactly a window: every slot reused.
	for s := uint64(1); s <= WindowBits; s++ {
		w.Check(s)
	}
	for s := uint64(WindowBits + 1); s <= 2*WindowBits; s++ {
		if r := w.Check(s); r != Fresh {
			t.Fatalf("seq %d: got %v want Fresh (stale bit not cleared)", s, r)
		}
	}
}

// Fuzz-ish: random interleaving of 4 links each delivering every packet with
// random delay; exactly one copy per sequence number must be Fresh.
func TestRandomMultiLink(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	var w Window
	const n = 50000
	type arr struct{ seq uint64 }
	var arrivals []arr
	for s := uint64(1); s <= n; s++ {
		for l := 0; l < 4; l++ {
			arrivals = append(arrivals, arr{s})
		}
	}
	// Local shuffle: displace each arrival by up to 2000 positions.
	for i := range arrivals {
		j := i + r.Intn(2000)
		if j < len(arrivals) {
			arrivals[i], arrivals[j] = arrivals[j], arrivals[i]
		}
	}
	fresh := make(map[uint64]int)
	for _, a := range arrivals {
		if w.Check(a.seq) == Fresh {
			fresh[a.seq]++
		}
	}
	if len(fresh) != n {
		t.Fatalf("accepted %d unique packets, want %d", len(fresh), n)
	}
	for s, c := range fresh {
		if c != 1 {
			t.Fatalf("seq %d accepted %d times", s, c)
		}
	}
}

func BenchmarkCheck(b *testing.B) {
	var w Window
	for i := 0; i < b.N; i++ {
		w.Check(uint64(i + 1))
	}
}
