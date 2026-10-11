package metrics

import (
	"math"
	"testing"
	"time"
)

func TestRTTSmoothing(t *testing.T) {
	var r RTT
	r.Update(100 * time.Millisecond)
	if r.SRTT != 100*time.Millisecond || r.Min != 100*time.Millisecond {
		t.Fatalf("first sample: %+v", r)
	}
	for i := 0; i < 50; i++ {
		r.Update(20 * time.Millisecond)
	}
	if r.SRTT > 22*time.Millisecond || r.Min != 20*time.Millisecond {
		t.Fatalf("did not converge: %+v", r)
	}
	if r.RTTVar > 2*time.Millisecond {
		t.Fatalf("jitter should settle: %v", r.RTTVar)
	}
}

func TestProbeTrackerRTTAndLoss(t *testing.T) {
	var p ProbeTracker
	t0 := time.Now()
	for i := uint32(1); i <= 20; i++ {
		p.Sent(i, t0.Add(time.Duration(i)*100*time.Millisecond))
	}
	// Ack all odd probes after 30ms.
	for i := uint32(1); i <= 20; i += 2 {
		rtt, ok := p.Acked(i, t0.Add(time.Duration(i)*100*time.Millisecond+30*time.Millisecond))
		if !ok || rtt != 30*time.Millisecond {
			t.Fatalf("probe %d rtt=%v ok=%v", i, rtt, ok)
		}
	}
	if _, ok := p.Acked(1, t0.Add(5*time.Second)); ok {
		t.Fatal("duplicate ack accepted")
	}
	if _, ok := p.Acked(999, t0); ok {
		t.Fatal("unknown ack accepted")
	}
	now := t0.Add(3 * time.Second)
	loss := p.Loss(now, time.Second, 100)
	if math.Abs(loss-0.5) > 0.01 {
		t.Fatalf("loss=%v want 0.5", loss)
	}
	// Probes younger than the timeout don't count as lost yet.
	if l := p.Loss(t0.Add(1050*time.Millisecond), time.Second, 100); l != 0 {
		t.Fatalf("young probes counted: %v", l)
	}
}

func TestRateAndPeak(t *testing.T) {
	var r Rate
	t0 := time.Now()
	r.Sample(0, t0)
	r.Sample(1_250_000, t0.Add(time.Second)) // 10 Mbit in 1s
	if math.Abs(r.Peak-10e6) > 1 {
		t.Fatalf("peak %v", r.Peak)
	}
	for i := 2; i < 12; i++ {
		r.Sample(1_250_000, t0.Add(time.Duration(i)*time.Second))
	}
	if r.BPS > 1e4 {
		t.Fatalf("rate should decay to ~0, got %v", r.BPS)
	}
	if r.Peak < 8.9e6 || r.Peak >= 10e6 {
		t.Fatalf("peak should decay slowly, got %v", r.Peak)
	}
}

func TestHistoryRing(t *testing.T) {
	h := NewHistory(3)
	for i := 1; i <= 5; i++ {
		h.Add(Sample{T: int64(i)})
	}
	s := h.Snapshot(0)
	if len(s) != 3 || s[0].T != 3 || s[2].T != 5 {
		t.Fatalf("got %+v", s)
	}
	if s := h.Snapshot(2); len(s) != 2 || s[0].T != 4 {
		t.Fatalf("last 2: %+v", s)
	}
	h.Resize(5)
	h.Add(Sample{T: 6})
	if s := h.Snapshot(0); len(s) != 4 || s[0].T != 3 || s[3].T != 6 {
		t.Fatalf("after grow: %+v", s)
	}
	h.Resize(2)
	if s := h.Snapshot(0); len(s) != 2 || s[0].T != 5 {
		t.Fatalf("after shrink: %+v", s)
	}
}

func TestQualityOrdering(t *testing.T) {
	good := Quality(20*time.Millisecond, 2*time.Millisecond, 0)
	meh := Quality(150*time.Millisecond, 20*time.Millisecond, 0.01)
	bad := Quality(400*time.Millisecond, 80*time.Millisecond, 0.2)
	if !(good > meh && meh > bad) {
		t.Fatalf("good=%v meh=%v bad=%v", good, meh, bad)
	}
	if good > 100 || bad < 0 {
		t.Fatal("out of range")
	}
}
