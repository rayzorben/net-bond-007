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

func TestShaperUnlimitedUntilLearned(t *testing.T) {
	var s Shaper
	s.Configure(true)
	if d := s.Delay(1<<20, time.Now()); d != 0 {
		t.Fatalf("shaped before any delivery report: %v", d)
	}
	var off Shaper
	off.Configure(false)
	off.Observe(time.Now(), 1e6)
	if off.Rate() != 0 || off.Delay(1<<20, time.Now()) != 0 {
		t.Fatal("disabled shaper limited traffic")
	}
}

func TestShaperSmoothsToDeliveryRate(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	// The link keeps delivering 40 Mbit/s; startup ramps then settles.
	for i := 0; i < 40; i++ {
		now, _ = drive(&s, now, 250*time.Millisecond, 200e6)
		s.Observe(now, 40e6)
	}
	// Over a full gain cycle the average sending rate equals delivery
	// (probe 1.25x, drain 0.75x, cruise 1x): no standing queue.
	var released float64
	for i := 0; i < len(steadyCycle); i++ {
		var rel float64
		now, rel = drive(&s, now, 250*time.Millisecond, 200e6)
		released += rel
		s.Observe(now, 40e6)
	}
	avg := released * 8 / (float64(len(steadyCycle)) * 0.25)
	if avg < 36e6 || avg > 46e6 {
		t.Fatalf("average sending rate over a cycle %.1f Mbit, want ~40", avg/1e6)
	}
	if peak := 1.25 * 40e6; s.Rate() > peak*1.01 {
		t.Fatalf("rate above the probe gain: %.1f", s.Rate()/1e6)
	}
}

func TestShaperLearnsCapacityAndKeepsItThroughIdle(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	// A sender offering 200 Mbit into a 40 Mbit link: the link delivers 40,
	// so shaping engages around that capacity.
	for i := 0; i < 12; i++ {
		var rel float64
		now, rel = drive(&s, now, 250*time.Millisecond, 200e6)
		s.Observe(now, min(40e6, rel*8/0.25))
	}
	// The gain cycle moves the rate between 0.75x and 1.25x of capacity.
	if r := s.ActiveRate(now); r < 0.74*40e6 || r > 1.26*40e6 {
		t.Fatalf("shaping rate %.1f Mbit, want within the 30-50 Mbit gain band", r/1e6)
	}
	learned := s.Rate()
	for i := 0; i < 40; i++ { // 10s idle: only probes trickle
		now = now.Add(250 * time.Millisecond)
		s.Delay(100, now)
		s.Observe(now, 50e3)
	}
	if s.Rate() < learned*0.75 {
		t.Fatalf("idle eroded the learned rate: %.1f -> %.1f", learned/1e6, s.Rate()/1e6)
	}
}

func TestShaperFollowsCapacityDrop(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	for i := 0; i < 20; i++ {
		now, _ = drive(&s, now, 250*time.Millisecond, 200e6)
		s.Observe(now, 40e6)
	}
	for i := 0; i < 40; i++ { // satellite gets congested: 10 Mbit
		now, _ = drive(&s, now, 250*time.Millisecond, 200e6)
		s.Observe(now, 10e6)
	}
	if r := s.Rate(); r > 20e6 {
		t.Fatalf("rate should follow capacity down (~12.5), got %.1f", r/1e6)
	}
}

// The drain phase of the gain cycle must not erode the estimate.
func TestShaperGainCycleDoesNotRatchetDown(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	for i := 0; i < 200; i++ { // 50s on a steady 40 Mbit link
		now, _ = drive(&s, now, 250*time.Millisecond, 200e6)
		// The link delivers what was sent, up to its 40 Mbit capacity.
		s.Observe(now, min(40e6, max(s.Rate(), 1e6)))
	}
	if s.maxD < 36e6 {
		t.Fatalf("estimate drifted down to %.1f Mbit on a steady 40 Mbit link", s.maxD/1e6)
	}
}

// A fast clean link must not be held back by the shaper's own probing: when
// everything sent is delivered, ramp up quickly.
func TestShaperRampsWhenLinkHasHeadroom(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	for i := 0; i < 12; i++ { // learn a 20 Mbit plateau (startup exits)
		now, _ = drive(&s, now, 250*time.Millisecond, 20e6)
		s.Observe(now, 20e6)
	}
	// Demand jumps; the link could do 120 and delivers whatever we send.
	for i := 0; i < 16; i++ {
		var rel float64
		now, rel = drive(&s, now, 250*time.Millisecond, 400e6)
		sent := rel * 8 / 0.25
		s.Observe(now, min(120e6, sent))
	}
	if s.Rate() < 100e6 {
		t.Fatalf("shaper held a clean link back: %.1f Mbit after 4s", s.Rate()/1e6)
	}
}

// A clean link that delivers everything is never shaped; a link that starts
// dropping part of what is sent gets shaped, then released again.
func TestShaperEngagesOnlyWhileLossy(t *testing.T) {
	var s Shaper
	s.Configure(true)
	now := time.Now()
	for i := 0; i < 20; i++ { // clean 100 Mbit link: delivered == sent
		var rel float64
		now, rel = drive(&s, now, 250*time.Millisecond, 100e6)
		s.Observe(now, rel*8/0.25)
	}
	if s.ActiveRate(now) != 0 || s.Delay(1<<20, now) != 0 {
		t.Fatal("clean link was shaped")
	}
	for i := 0; i < 8; i++ { // bursts overflow: only 30 of ~100 arrives
		now, _ = drive(&s, now, 250*time.Millisecond, 100e6)
		s.Observe(now, 30e6)
	}
	if r := s.ActiveRate(now); r == 0 || r > 60e6 {
		t.Fatalf("lossy link not shaped near delivery: %.1f", r/1e6)
	}
	now = now.Add(shapeEngageFor + time.Second)
	if s.ActiveRate(now) != 0 {
		t.Fatal("shaping did not release after the link recovered")
	}
}
