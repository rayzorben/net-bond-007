package bond

import (
	"testing"
	"time"
)

// drive sends at offeredBps through the shaper for d and returns the bytes
// actually released (honouring the delays it asks for).
func drive(s *Shaper, now time.Time, d time.Duration, offeredBps float64) (time.Time, float64) {
	var released float64
	end := now.Add(d)
	next := now
	per := offeredBps / 8 / 1000 // bytes per ms
	for t := now; t.Before(end); t = t.Add(time.Millisecond) {
		for b := 0.0; b+1400 <= per; b += 1400 {
			if t.Before(next) {
				break // sender is waiting on the shaper
			}
			if w := s.Delay(1400, t); w > 0 {
				next = t.Add(w)
			}
			released += 1400
		}
	}
	return end, released
}

// link simulates a bottleneck of capBps fed by the shaper: each 250 ms the
// far end reports what it received (what was sent, up to capacity; more
// than capacity overflows a shallow buffer and loses the excess and more).
type shapeLink struct {
	s      *Shaper
	now    time.Time
	capBps float64
	last   float64 // what the far end reports next (lags one interval)
}

func (k *shapeLink) interval(offered float64) float64 {
	var rel float64
	k.now, rel = drive(k.s, k.now, 250*time.Millisecond, offered)
	sent := rel * 8 / 0.25
	k.s.Observe(k.now, k.last)
	k.last = min(sent, k.capBps)
	if sent > k.capBps*1.1 {
		k.last = k.capBps * 0.9 // shallow buffer: bursts overflow
	}
	return sent
}

func newShapeLink(capBps float64) *shapeLink {
	s := &Shaper{}
	s.Configure(true)
	s.SetHint(0, 80*time.Millisecond)
	return &shapeLink{s: s, now: time.Now(), capBps: capBps}
}

func TestShaperPacesFromTheFirstPacket(t *testing.T) {
	var s Shaper
	s.Configure(true)
	_, rel := drive(&s, time.Now(), time.Second, 500e6)
	if got := rel * 8; got > 2*shapeStartRate {
		t.Fatalf("before any report it sent %.1f Mbit/s, want about the start rate %.0f", got/1e6, shapeStartRate/1e6)
	}
	// A capacity hint (a speed test) starts it just under that rate (hints
	// carry headroom).
	var h Shaper
	h.Configure(true)
	h.SetHint(80e6, 50*time.Millisecond)
	_, rel = drive(&h, time.Now(), time.Second, 500e6)
	if got := rel * 8; got < 60e6 || got > 70e6 {
		t.Fatalf("with an 80 Mbit/s hint it sent %.1f Mbit/s", got/1e6)
	}
	var off Shaper
	off.Configure(false)
	if off.Delay(1<<20, time.Now()) != 0 || off.Rate() != 0 {
		t.Fatal("disabled shaper paced")
	}
}

// Startup finds the link's capacity within a couple of seconds, and the
// steady state then sends at it on average without overrunning it.
func TestShaperFindsCapacityAndCruisesAtIt(t *testing.T) {
	k := newShapeLink(40e6)
	for i := 0; i < 10; i++ { // 2.5 s
		k.interval(500e6)
	}
	if k.s.mode == shapeStartup {
		t.Fatal("still in startup after 2.5 s")
	}
	if bw := k.s.Bandwidth(); bw < 0.85*40e6 || bw > 1.05*40e6 {
		t.Fatalf("measured %.1f Mbit/s on a 40 Mbit/s link", bw/1e6)
	}
	var total float64
	for i := 0; i < 2*len(steadyCycle); i++ {
		total += k.interval(500e6)
	}
	avg := total / float64(2*len(steadyCycle))
	if avg < 0.85*40e6 || avg > 1.02*40e6 {
		t.Fatalf("steady sending %.1f Mbit/s on a 40 Mbit/s link", avg/1e6)
	}
}

// A probe lasts one round trip, not the whole interval: the excess it sends
// is small enough for a shallow buffer.
func TestShaperProbeLastsOneRoundTrip(t *testing.T) {
	k := newShapeLink(40e6)
	for i := 0; i < 20; i++ {
		k.interval(500e6)
	}
	for k.s.gain <= 1 { // advance to a probe interval
		k.interval(500e6)
	}
	sent := k.interval(500e6) // the probe interval
	// 1.25x for 80 ms of 250 ms: ~1.08x on average, not 1.25x.
	if r := sent / k.s.Bandwidth(); r > 1.12 {
		t.Fatalf("probe interval sent %.2fx the measured rate", r)
	}
}

func TestShaperKeepsEstimateThroughIdle(t *testing.T) {
	k := newShapeLink(40e6)
	for i := 0; i < 20; i++ {
		k.interval(500e6)
	}
	bw := k.s.Bandwidth()
	for i := 0; i < 40; i++ { // 10 s of only probes
		k.interval(50e3)
	}
	if k.s.Bandwidth() < 0.9*bw {
		t.Fatalf("idle eroded the estimate %.1f -> %.1f", bw/1e6, k.s.Bandwidth()/1e6)
	}
}

func TestShaperFollowsCapacityDownAndUp(t *testing.T) {
	k := newShapeLink(40e6)
	for i := 0; i < 20; i++ {
		k.interval(500e6)
	}
	k.capBps = 10e6 // the satellite gets congested
	for i := 0; i < 20; i++ {
		k.interval(500e6)
	}
	if bw := k.s.Bandwidth(); bw > 13e6 {
		t.Fatalf("estimate stuck at %.1f Mbit/s after the link fell to 10", bw/1e6)
	}
	k.capBps = 120e6          // and recovers, with room to spare
	for i := 0; i < 32; i++ { // 8 s
		k.interval(500e6)
	}
	if bw := k.s.Bandwidth(); bw < 100e6 {
		t.Fatalf("estimate %.1f Mbit/s 8 s after the link grew to 120", bw/1e6)
	}
}

// A hint above the link's real capacity, behind a shallow buffer: startup
// overruns, but one short interval (or a partial first one) must not freeze
// the estimate low; it settles near capacity within a few seconds.
func TestShaperRecoversFromAnOptimisticHint(t *testing.T) {
	k := newShapeLink(116e6)
	k.s.SetHint(130e6, 76*time.Millisecond)
	k.interval(20e6)          // the transfer starts part-way through an interval
	for i := 0; i < 16; i++ { // 4 s
		k.interval(500e6)
	}
	if bw := k.s.Bandwidth(); bw < 0.85*116e6 {
		t.Fatalf("settled at %.1f Mbit/s on a 116 Mbit/s link", bw/1e6)
	}
}

// A fast link that only carried a slow link's worth for a while (mirroring
// at the slowest link's pace) must not stay locked at that rate once more
// is offered.
func TestShaperDoesNotLockAtLowDemand(t *testing.T) {
	k := newShapeLink(120e6)
	for i := 0; i < 16; i++ { // 4 s of only 30 Mbit/s offered
		k.interval(30e6)
	}
	for i := 0; i < 12; i++ { // then 3 s of everything
		k.interval(500e6)
	}
	if bw := k.s.Bandwidth(); bw < 100e6 {
		t.Fatalf("stuck at %.1f Mbit/s on a 120 Mbit/s link after demand rose", bw/1e6)
	}
}
