package bond

import (
	"sync"
	"time"
)

// Shaper paces what a link sends at the rate the far end confirms it
// receives, from the first packet on (BBR-style rate pacing).
//
// Why: a fast host emits line-rate bursts, and the inner TCP's ACK-clocked
// bursts (bunched further by the reorder buffer) arrive at a Starlink
// downlink that drops what it can't buffer. Measured in the field: from the
// same Ashburn server over the same dish, an unpaced sender got 4-12 Mbit/s
// and a paced one 54-79. An earlier version only started pacing after the
// link had dropped part of what it was sent, so every transfer began by
// overflowing the buffer, and the losses it caused were then repaired at
// the cost of round-trip stalls.
//
// The rate comes from delivery reports (what the far end received in each
// ~250 ms probe interval):
//   - startup: double the rate each interval until delivery stops growing
//     (or the link starts dropping), from a capacity hint if there is one;
//   - drain: one interval below the measured rate to empty the queue
//     startup built;
//   - probe: cruise at the measured rate, now and then probing 25% above it
//     and draining after. A probe lasts one round trip, not the whole
//     interval, so it fits in a shallow buffer; a probe that makes the link
//     drop is cut short.
//
// The shaper never drops: packets wait in the link's queue, and the queue's
// drain time feeds the scheduler's arrival estimate.
type Shaper struct {
	mu      sync.Mutex
	enabled bool

	mode  shapeMode
	rate  float64 // pacing rate in bits/s, gain x bw (0 until enabled)
	gain  float64
	cycle int

	// Windowed max of delivery samples taken while the link was loaded
	// (BBR's bandwidth filter). Idle intervals don't erode it.
	win  [shapeWindow]float64
	wpos int
	bw   float64

	hint float64       // capacity hint (speed test), bits/s; 0 = none
	rtt  time.Duration // round trip, for how long a probe or drain lasts

	fullBW    float64 // startup: best delivery so far ...
	fullCount int     // ... and intervals since it last grew by 25%
	overruns  int     // startup: consecutive fully loaded intervals with a shortfall

	// lastShort is when the link last delivered clearly less than it was
	// sent (it was at capacity, or overflowing). Until shapeCalmFor after
	// that, cruise is exact; after it, cruise keeps headroom, so a link that
	// has more to give is never held at what it happened to carry.
	lastShort time.Time
	headroom  bool // the current interval cruises with headroom (whole interval)

	// The interval in progress and the previous one (a delivery report
	// describes the previous interval).
	sent, prevSent     float64 // bytes sent in it
	prevRate, prevGain float64
	intervalStart      time.Time
	lastObs            time.Time

	tokens float64 // bytes
	last   time.Time

	waitedFor time.Duration // total pacing delay (stats)
}

type shapeMode uint8

const (
	shapeStartup shapeMode = iota
	shapeDrain
	shapeProbe
)

const (
	shapeWindow    = 10                     // delivery samples (~2.5 s at 250 ms probes)
	shapeStartRate = 10e6                   // bits/s before anything is known
	shapeMinRate   = 1e6                    // bits/s floor
	shapeStartGain = 2.0                    // startup: double per interval
	shapeDrainGain = 0.75                   // after startup, and after each probe
	shapeProbeGain = 1.25                   // now and then, for one round trip
	shapeBurstTime = 4 * time.Millisecond   // token bucket depth at the current rate
	shapeMinBurst  = 16 << 10               // bytes
	shapeShortfall = 0.85                   // delivered/sent below this: the link dropped part
	shapeSleepMin  = 2 * time.Millisecond   // sleep only once this much is owed
	shapeDefRTT    = 100 * time.Millisecond // when no RTT is known
	shapeHeadroom  = 1.25                   // cruise gain on a link not shown to be full
	shapeCalmFor   = 2 * time.Second        // since the last shortfall, before cruising with headroom
)

// steadyCycle is the per-interval gain cycle once the link is measured:
// probe, drain, then cruise (mean 1.0: no standing queue builds).
var steadyCycle = [...]float64{shapeProbeGain, shapeDrainGain, 1, 1, 1, 1, 1, 1}

// Configure turns pacing on or off. Turning it on starts a fresh startup.
func (s *Shaper) Configure(enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if enabled && !s.enabled {
		s.mode, s.gain, s.fullBW, s.fullCount, s.overruns = shapeStartup, shapeStartGain, 0, 0, 0
		s.setRateLocked()
	}
	s.enabled = enabled
}

// SetHint gives the shaper what is known about the link before it has
// measured anything: its capacity (e.g. from a speed test; 0 = unknown) and
// its round trip.
func (s *Shaper) SetHint(capBps float64, rtt time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rtt > 0 {
		s.rtt = rtt
	}
	if capBps > 0 && capBps != s.hint {
		s.hint = capBps
		if s.bw == 0 {
			s.setRateLocked()
		}
	}
}

// base is the rate gains apply to: the measured bandwidth, or before that
// half the hint (startup's doubling then sends at about the hint), or a
// conservative start.
func (s *Shaper) baseLocked() float64 {
	switch {
	case s.bw > 0:
		return s.bw
	case s.hint > 0:
		// Hints carry headroom (the client's estimate adds 25%): start
		// startup's doubling at 80% of it, not above the link.
		return 0.8 * s.hint / shapeStartGain
	default:
		return shapeStartRate / shapeStartGain
	}
}

func (s *Shaper) setRateLocked() {
	s.rate = max(shapeMinRate, s.gain*s.baseLocked())
}

func (s *Shaper) windowMax() float64 {
	var m float64
	for _, v := range s.win {
		m = max(m, v)
	}
	return m
}

// Observe feeds the delivery rate the far end reports for this link (bits/s
// received during its last interval). Call it for every report.
func (s *Shaper) Observe(now time.Time, deliveredBps float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sentBps float64
	if !s.lastObs.IsZero() {
		if dt := now.Sub(s.lastObs).Seconds(); dt > 0 {
			sentBps = s.sent * 8 / dt
		}
	}
	rate, gain := s.rate, s.gain
	s.sent, s.lastObs, s.intervalStart = 0, now, now
	if !s.enabled {
		return
	}
	// The report describes the previous interval: judge it against what was
	// sent then.
	prevSent, prevRate, prevGain := s.prevSent, s.prevRate, s.prevGain
	s.prevSent, s.prevRate, s.prevGain = sentBps, rate, gain
	if deliveredBps <= 0 || prevRate <= 0 {
		return
	}
	// Loaded: we sent near the pacing rate, so delivery shows what the link
	// can do. App-limited intervals must not lower the estimate.
	loaded := prevSent >= 0.5*prevRate
	before := s.bw
	if loaded || deliveredBps > s.bw {
		s.win[s.wpos%shapeWindow] = deliveredBps
		s.wpos++
		s.bw = s.windowMax()
	}
	shortfall := prevSent > 2e6 && deliveredBps < shapeShortfall*prevSent
	if shortfall || s.lastShort.IsZero() {
		s.lastShort = now
	}

	switch s.mode {
	case shapeStartup:
		full := prevSent >= 0.9*prevRate
		if shortfall && full {
			s.overruns++
		} else {
			s.overruns = 0
		}
		switch {
		case s.overruns >= 2:
			// Doubling overran the link twice running (one short interval
			// can be a partial one as a transfer starts): what got
			// through is its capacity.
			s.mode = shapeDrain
		case shortfall:
			// Wait for a second reading before trusting it.
		case !loaded:
			// App-limited: no evidence either way.
		case s.bw >= s.fullBW*1.25:
			s.fullBW, s.fullCount = s.bw, 0
		default:
			if s.fullCount++; s.fullCount >= 3 {
				s.mode = shapeDrain
			}
		}
	case shapeDrain:
		s.mode, s.cycle = shapeProbe, 0
	case shapeProbe:
		switch {
		case shortfall && prevGain > 1:
			// The probe made the link drop: skip to the drain phase.
			s.cycle = 1
		case prevGain > 1 && loaded && !shortfall && before > 0 && s.bw >= 1.03*before:
			// The probe got more through than ever: the link has grown
			// (Starlink's capacity swings). Find the new rate quickly
			// instead of creeping up a probe at a time. (A probe lasts one
			// round trip of the ~250 ms interval, so even a link with room
			// to spare shows only a few percent more over the interval.)
			s.mode, s.fullBW, s.fullCount, s.overruns = shapeStartup, s.bw, 0, 0
		}
	}
	s.headroom = false
	switch s.mode {
	case shapeStartup:
		s.gain = shapeStartGain
	case shapeDrain:
		s.gain = shapeDrainGain
	default:
		s.gain = steadyCycle[s.cycle%len(steadyCycle)]
		s.cycle++
		// A link that hasn't been full for a while: cruise with headroom.
		// Mirroring, for one, can leave a fast link carrying only what the
		// slowest link could; at exact pacing it would stay locked there.
		// If the link really is full, the next report shows a shortfall
		// and cruise goes back to exact.
		if s.gain >= 1 && now.Sub(s.lastShort) >= shapeCalmFor {
			s.gain, s.headroom = max(s.gain, shapeHeadroom), true
		}
	}
	s.setRateLocked()
}

// currentRateLocked is the rate to pace at right now: a probe or drain
// gain applies for one round trip at the start of the interval, then the
// link cruises at the measured rate (startup keeps its gain all interval).
func (s *Shaper) currentRateLocked(now time.Time) float64 {
	if s.mode != shapeProbe || s.gain == 1 || s.headroom || s.bw == 0 {
		return s.rate
	}
	rtt := s.rtt
	if rtt <= 0 {
		rtt = shapeDefRTT
	}
	if now.Sub(s.intervalStart) > rtt {
		return max(shapeMinRate, s.bw)
	}
	return s.rate
}

// Delay reserves n bytes and returns how long the sender should wait before
// putting them on the wire (0 if it can send now).
func (s *Shaper) Delay(n int, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent += float64(n)
	if !s.enabled || s.rate <= 0 {
		return 0
	}
	rate := s.currentRateLocked(now)
	burst := max(float64(shapeMinBurst), rate/8*shapeBurstTime.Seconds())
	if s.last.IsZero() {
		s.last, s.tokens = now, burst
	}
	if dt := now.Sub(s.last).Seconds(); dt > 0 {
		s.tokens = min(burst, s.tokens+rate/8*dt)
		s.last = now
	}
	s.tokens -= float64(n)
	if s.tokens >= 0 {
		return 0
	}
	d := time.Duration(-s.tokens * 8 / rate * float64(time.Second))
	if d < shapeSleepMin {
		return 0 // small debt: keep sending, pay it back in one larger wait
	}
	s.waitedFor += d
	return d
}

// Rate is the pacing rate in bits/s (0 = disabled).
func (s *Shaper) Rate() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0
	}
	return s.rate
}

// ActiveRate is the rate being enforced now (0 = not pacing).
func (s *Shaper) ActiveRate(now time.Time) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return 0
	}
	return s.currentRateLocked(now)
}

// Bandwidth is the measured delivery rate in bits/s (0 = not measured yet).
func (s *Shaper) Bandwidth() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bw
}
