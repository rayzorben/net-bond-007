package bond

import (
	"sync"
	"time"
)

// Shaper smooths what a link sends to slightly above the rate the far end
// confirms it is actually receiving (BBR-style delivery-rate pacing).
//
// Why: a fast host (gigabit LAN, data centre) emits line-rate bursts. When the
// last mile is a ~40 Mbit satellite downlink with a modest buffer, those
// bursts overflow and drop even though average throughput is far below
// capacity. The inner TCP reads the drops as congestion and collapses. CDNs
// avoid this by pacing; the bond has to do the same for the traffic it relays.
//
// The shaper never drops: packets wait in the link's queue, and the queue's
// drain time feeds the scheduler's arrival estimate.
type Shaper struct {
	mu      sync.Mutex
	enabled bool
	rate    float64 // bits/s; 0 = unlimited (nothing learned yet)
	maxD    float64 // learned delivery rate, bits/s
	tokens  float64 // bytes
	last    time.Time

	sent      float64 // bytes since last Observe
	lastObs   time.Time
	startup   bool
	stalls    int // consecutive observations without meaningful growth
	gain      float64
	cycle     int           // position in the steady-state gain cycle
	waitedFor time.Duration // total shaping delay (stats)

	// Windowed max of delivery samples taken while the link was loaded
	// (BBR's bandwidth filter). No decay: a real capacity drop shows once
	// the old peak ages out of the window.
	win  [shapeWindow]float64
	wpos int

	// The previous interval, to judge a probe once its delivery report
	// arrives (reports lag sending by about one interval).
	prevSent, prevRate, prevGain float64

	// Shaping engages only while the link drops part of what is sent (the
	// burst-overflow signature) and for a while after. A link that delivers
	// everything is never slowed: TCP ramps far faster than any shaper can.
	engagedUntil time.Time
	shortStreak  int // consecutive observations with a delivery shortfall
}

// shapeSleepMin batches waiting: sleeping per packet for microseconds costs
// more than it shapes (timer granularity), so the sender only sleeps once it
// owes at least this much, then pays it back in one go.
const shapeSleepMin = 2 * time.Millisecond

const shapeEngageFor = 10 * time.Second

func (s *Shaper) engaged(now time.Time) bool { return now.Before(s.engagedUntil) }

const shapeWindow = 12 // observations (~3s at 250ms probes)

func (s *Shaper) windowMax() float64 {
	var m float64
	for _, v := range s.win {
		m = max(m, v)
	}
	return m
}

// steadyCycle is BBR's ProbeBW gain cycle: one interval probing above the
// delivery rate, one draining the queue that built, then cruising. Its mean is
// 1.0, so no standing queue forms at the bottleneck.
var steadyCycle = [...]float64{1.25, 0.75, 1, 1, 1, 1, 1, 1}

const (
	shapeMinRate   = 1e6 // bits/s floor once shaping is active
	shapeBurstTime = 4 * time.Millisecond
	shapeMinBurst  = 16 << 10
	shapeStartGain = 2.0 // ramp fast until delivery stops growing
)

func (s *Shaper) Configure(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled && !s.enabled {
		s.startup = true
		s.gain = shapeStartGain
	}
	s.enabled = enabled
}

// Observe feeds the delivery rate the far end reports for this link.
func (s *Shaper) Observe(now time.Time, deliveredBps float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sentBps float64
	if !s.lastObs.IsZero() {
		if dt := now.Sub(s.lastObs).Seconds(); dt > 0 {
			sentBps = s.sent * 8 / dt
		}
	}
	s.sent, s.lastObs = 0, now
	if !s.enabled || deliveredBps <= 0 {
		return
	}
	// The link was loaded if we sent at a good fraction of the shaped rate
	// (or nothing is learned yet). Idle periods must not erode the estimate.
	loaded := s.rate == 0 || sentBps >= 0.5*s.rate
	if loaded || deliveredBps > s.maxD {
		prev := s.maxD
		s.win[s.wpos%shapeWindow] = deliveredBps
		s.wpos++
		s.maxD = s.windowMax()
		if s.startup {
			if prev > 0 && s.maxD < prev*1.25 {
				s.stalls++
			} else {
				s.stalls = 0
			}
		}
	}
	if s.startup && s.stalls >= 3 {
		s.startup = false
	}
	// Judge the last probe (gain > 1): if we sent at the probe rate and the far
	// end received nearly all of it, the link has headroom. Ramp fast again
	// instead of creeping up one probe cycle at a time. A saturated link
	// delivers only ~1/1.25 of a probe, so this does not trigger there.
	if !s.startup && s.prevGain > 1 && s.prevRate > 0 &&
		s.prevSent >= 0.85*s.prevRate && deliveredBps >= 0.92*s.prevSent {
		s.startup, s.stalls = true, 0
	}
	// Delivery reports describe the previous interval: compare like with like.
	if s.prevSent > 2e6 && deliveredBps < 0.88*s.prevSent {
		s.shortStreak++
	} else {
		s.shortStreak = 0
	}
	// Measurement windows at the two ends don't line up exactly, so one short
	// reading is noise. Burst overflow (e.g. Starlink) is persistent.
	if s.shortStreak >= 3 {
		if !s.engaged(now) {
			// Newly lossy: earlier peaks no longer describe this link. Start
			// from what it delivers now; probing grows it back if it can.
			s.win = [shapeWindow]float64{}
			s.win[0], s.wpos = deliveredBps, 1
			s.maxD, s.startup = deliveredBps, false
		}
		s.engagedUntil = now.Add(shapeEngageFor)
	}
	// Shaping is what stops the loss, so the shortfall signal fades once it
	// works. Stay engaged while the link stays busy; release after idling.
	if s.engaged(now) && loaded {
		s.engagedUntil = now.Add(shapeEngageFor)
	}
	s.prevSent, s.prevRate, s.prevGain = sentBps, s.rate, s.gain
	switch {
	case s.startup:
		s.gain = shapeStartGain
	default:
		s.gain = steadyCycle[s.cycle%len(steadyCycle)]
		s.cycle++
	}
	if s.maxD > 0 {
		s.rate = max(shapeMinRate, s.gain*s.maxD)
	}
}

// Delay reserves n bytes and returns how long the sender should wait before
// putting them on the wire (0 if it can send now).
func (s *Shaper) Delay(n int, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent += float64(n)
	if !s.enabled || s.rate <= 0 || !s.engaged(now) {
		return 0
	}
	burst := max(float64(shapeMinBurst), s.rate/8*shapeBurstTime.Seconds())
	if s.last.IsZero() {
		s.last, s.tokens = now, burst
	}
	if dt := now.Sub(s.last).Seconds(); dt > 0 {
		s.tokens = min(burst, s.tokens+s.rate/8*dt)
		s.last = now
	}
	s.tokens -= float64(n)
	if s.tokens >= 0 {
		return 0
	}
	d := time.Duration(-s.tokens * 8 / s.rate * float64(time.Second))
	if d < shapeSleepMin {
		return 0 // small debt: keep sending, pay it back in one larger wait
	}
	s.waitedFor += d
	return d
}

// Rate is the learned shaping rate in bits/s (0 = disabled / unknown).
func (s *Shaper) Rate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0
	}
	return s.rate
}

// ActiveRate is the rate actually being enforced now (0 = not shaping).
func (s *Shaper) ActiveRate(now time.Time) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || !s.engaged(now) {
		return 0
	}
	return s.rate
}
