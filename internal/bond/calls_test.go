package bond

import (
	"encoding/binary"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/sched"
)

// voicePkt builds a small UDP packet to a call port carrying its send time.
func voicePkt(at time.Time) []byte {
	p := make([]byte, 200)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[9] = 17
	binary.BigEndian.PutUint16(p[20:], 50012)
	binary.BigEndian.PutUint16(p[22:], 3479)
	binary.BigEndian.PutUint64(p[28:], uint64(at.UnixNano()))
	return p
}

type callStats struct {
	mu    sync.Mutex
	n     int
	worst time.Duration
}

func (c *callStats) deliver(p []byte) {
	if p[9] != 17 {
		return
	}
	d := time.Since(time.Unix(0, int64(binary.BigEndian.Uint64(p[28:]))))
	c.mu.Lock()
	c.n++
	c.worst = max(c.worst, d)
	c.mu.Unlock()
}

// The field failure (2026-10-20): bulk traffic lost on the campground Wi-Fi
// leaves gaps, and call audio that arrived safely on Starlink waits behind
// them for the whole reorder hold. On the call lane it must not wait.
func TestCallNotHeldBehindBulkGaps(t *testing.T) {
	for _, lane := range []bool{false, true} {
		got := &callStats{}
		b := New(1, got.deliver)
		a := New(1, func([]byte) {})
		paths := []*simPath{
			{delay: 10 * time.Millisecond, loss: 0.4}, // campground Wi-Fi
			{delay: 10 * time.Millisecond},            // Starlink
		}
		for i, p := range paths {
			id := uint8(i + 1)
			p.rng = rand.New(rand.NewSource(int64(i)))
			bl := b.AddLink(id, "l", "sim", func([]byte) error { return nil })
			bl.SetState(StateUp)
			al := a.AddLink(id, "l", "sim", p.sender(b, func() *Link { return bl }))
			al.SetState(StateUp)
		}
		ports, _ := sched.ParsePorts("3478-3497")
		a.SetPolicy(sched.Policy{Mode: sched.ModeSpeed, RealtimeMirror: true, RealtimePorts: ports})
		a.SetCallLane(lane)
		b.SetReorderHold(300 * time.Millisecond)

		for i := uint32(0); i < 200; i++ {
			a.Send(fakePkt(i)) // bulk: one copy, some on the lossy link
			a.Send(voicePkt(time.Now()))
			time.Sleep(time.Millisecond)
		}
		time.Sleep(500 * time.Millisecond)
		got.mu.Lock()
		n, worst := got.n, got.worst
		got.mu.Unlock()
		a.Close()
		b.Close()
		t.Logf("call lane %v: %d/200 delivered, worst %v", lane, n, worst)

		if !lane {
			// Control: without the lane the test must show the problem.
			if worst < 200*time.Millisecond {
				t.Fatalf("control: calls waited at most %v; the test doesn't reproduce the hold", worst)
			}
			continue
		}
		if n != 200 {
			t.Fatalf("call packets delivered %d times, want exactly 200", n)
		}
		if worst > 60*time.Millisecond {
			t.Fatalf("a call packet took %v (path delay 10ms): held behind bulk gaps", worst)
		}
		if a.calls.Tx.Load() != 200 {
			t.Fatalf("calls sent on the lane: %d/200", a.calls.Tx.Load())
		}
	}
}

// A call sent on the lane leaves no hole in the ordered stream even when
// every copy is lost, so bulk traffic never waits for it.
func TestLostCallLeavesNoGap(t *testing.T) {
	a, b, got := pair(t, []*simPath{{delay: 5 * time.Millisecond}})
	ports, _ := sched.ParsePorts("3478-3497")
	a.SetPolicy(sched.Policy{Mode: sched.ModeSpeed, RealtimeMirror: true, RealtimePorts: ports})
	a.SetCallLane(true)
	b.SetReorderHold(300 * time.Millisecond)
	a.Send(fakePkt(1))
	a.calls.seq.Add(1)               // a call whose every copy was lost
	time.Sleep(2 * time.Millisecond) // keep the sim timers in order
	a.Send(fakePkt(2))
	start := time.Now()
	waitFor(t, got, 2)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("bulk waited %v after a lost call", d)
	}
	if s, _, _ := b.ReorderStats(); s.Held != 0 || s.Skipped != 0 {
		t.Fatalf("ordered stream saw a gap: %+v", s)
	}
}

// Call frames jump bulk data already queued on a link.
func TestCallJumpsLinkQueue(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	release := make(chan struct{})
	var mu sync.Mutex
	var order []int // length of each frame sent: calls are the short ones
	l := e.AddLink(1, "eth0", "sim", func(f []byte) error {
		<-release
		mu.Lock()
		order = append(order, len(f))
		mu.Unlock()
		return nil
	})
	l.SetState(StateUp)
	ports, _ := sched.ParsePorts("3478-3497")
	e.SetPolicy(sched.Policy{Mode: sched.ModeSpeed, RealtimeMirror: true, RealtimePorts: ports})
	e.SetCallLane(true)
	for i := uint32(0); i < 20; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(10 * time.Millisecond) // the writer holds frame 0, 19 wait
	e.Send(voicePkt(time.Now()))
	close(release)
	time.Sleep(30 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 21 {
		t.Fatalf("sent %d frames, want 21", len(order))
	}
	// Frame 0 was already in the writer's hands; the call goes next.
	if order[1] >= order[0] {
		t.Fatalf("call frame was not next after the in-flight one: %v", order)
	}
}
