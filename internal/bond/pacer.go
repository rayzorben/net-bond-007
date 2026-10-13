package bond

import (
	"sync"
	"time"
)

// Pacer is a per-link adaptive rate limiter (token bucket + AIMD).
//
// Problem it solves: with redundancy, the sender's TCP only feels the fastest
// path, because the first copy always wins. It ramps up to that path's speed
// and overflows the slower links with duplicates. Their queues bloat (seconds
// of delay) and drop everything, so when the fast path loses a packet, the
// "backup" copy is useless and TCP stalls in retransmit timeouts.
//
// The pacer discovers what each link can carry: it raises the rate while the
// link is in demand and its RTT stays near the minimum, and backs off on queue
// inflation or loss (a delay-based scheme in the spirit of LEDBAT/BBR). Copies
// beyond a link's paced rate are not sent on that link. The discovered rate
// doubles as the link's capacity estimate for scheduling weights.
type Pacer struct {
	mu        sync.Mutex
	rate      float64 // bits/s
	tokens    float64 // bytes
	last      time.Time
	limited   bool // ran out of tokens since the last Govern
	lastDown  time.Time
	enabled   bool
	maxRate   float64
	startRate float64
	used      float64   // bytes sent since the last Govern
	lastGov   time.Time // time of the last Govern
	useRate   float64   // smoothed bits/s actually sent

	delayThresh time.Duration // queue delay that counts as congestion
	lossThresh  float64       // probe loss that counts as congestion

	shortSince time.Time // when delivery started falling short of sending
	learned    bool      // rate has been adjusted from real measurements

	// Baseline RTT is the minimum over a sliding window (two 10s buckets),
	// not all-time: paths like Starlink change base latency every few
	// seconds as satellites hand over.
	minCur, minPrev time.Duration
	minStart        time.Time
	jitter          time.Duration // EWMA of RTT change between samples
	lastRTT         time.Duration
	congStreak      int
}

const minRTTWindow = 10 * time.Second

func (p *Pacer) baseRTT(now time.Time, rtt time.Duration) time.Duration {
	if p.minStart.IsZero() || now.Sub(p.minStart) > minRTTWindow {
		p.minPrev, p.minCur, p.minStart = p.minCur, rtt, now
	}
	if rtt < p.minCur {
		p.minCur = rtt
	}
	base := p.minCur
	if p.minPrev > 0 && p.minPrev < base {
		base = p.minPrev
	}
	if p.lastRTT > 0 {
		d := rtt - p.lastRTT
		if d < 0 {
			d = -d
		}
		p.jitter = (3*p.jitter + d) / 4
	}
	p.lastRTT = rtt
	return base
}

// Tuning sets the congestion thresholds (zero values keep the defaults).
func (p *Pacer) Tuning(delay time.Duration, loss float64) {
	p.mu.Lock()
	p.delayThresh, p.lossThresh = delay, loss
	p.mu.Unlock()
}

const (
	minPaceRate = 500e3 // bits/s
	burstWindow = 50 * time.Millisecond
)

func (p *Pacer) Configure(enabled bool, startBps, maxBps float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enabled = enabled
	p.maxRate = maxBps
	if startBps <= 0 {
		startBps = 20e6
	}
	p.startRate = startBps
	// Until measurements have moved it, the rate follows the configured start
	// rate (the policy can arrive after the link was first configured).
	if !p.learned || p.rate == 0 {
		p.rate = startBps
	}
	if p.maxRate > 0 && p.rate > p.maxRate {
		p.rate = p.maxRate
	}
}

// Allow reports whether n bytes may be sent now, consuming tokens if so.
func (p *Pacer) Allow(n int, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return true
	}
	p.refill(now)
	if p.tokens >= float64(n) {
		p.tokens -= float64(n)
		p.used += float64(n)
		return true
	}
	p.limited = true
	return false
}

// Has reports whether n bytes fit in the bucket right now (without consuming).
func (p *Pacer) Has(n int, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return true
	}
	p.refill(now)
	if p.tokens >= float64(n) {
		return true
	}
	p.limited = true
	return false
}

// Backlog estimates how long already-committed bytes (bucket debt) take to
// drain at the paced rate.
func (p *Pacer) Backlog(now time.Time) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled || p.rate <= 0 {
		return 0
	}
	p.refill(now)
	if p.tokens >= 0 {
		return 0
	}
	return time.Duration(-p.tokens * 8 / p.rate * float64(time.Second))
}

// Force consumes tokens even if that drives the bucket negative (used when a
// packet must go out on some link regardless).
func (p *Pacer) Force(n int, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return
	}
	p.refill(now)
	p.tokens -= float64(n)
	p.used += float64(n)
	if floor := -p.burst(); p.tokens < floor {
		p.tokens = floor
	}
	p.limited = true
}

func (p *Pacer) burst() float64 {
	b := p.rate / 8 * burstWindow.Seconds()
	// TCP sends in bursts of up to 64KB (TSO); a smaller bucket would skip
	// redundant copies mid-burst even though the link has room on average.
	if b < 256<<10 {
		b = 256 << 10
	}
	return b
}

func (p *Pacer) refill(now time.Time) {
	if p.last.IsZero() {
		p.last = now
		p.tokens = p.burst()
		return
	}
	dt := now.Sub(p.last).Seconds()
	if dt <= 0 {
		return
	}
	p.last = now
	p.tokens += p.rate / 8 * dt
	if b := p.burst(); p.tokens > b {
		p.tokens = b
	}
}

// Govern adapts the rate from the latest health measurements. Call it a few
// times per second (on every probe result). delivered is the rate the far
// end reports actually receiving on this link (bits/s, 0 if unknown).
func (p *Pacer) Govern(now time.Time, srtt, minRTT time.Duration, loss, delivered float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.lastGov.IsZero() {
		if dt := now.Sub(p.lastGov).Seconds(); dt > 0 {
			inst := p.used * 8 / dt
			p.useRate = 0.6*p.useRate + 0.4*inst
		}
	}
	p.used, p.lastGov = 0, now
	if !p.enabled || srtt <= 0 {
		p.limited = false
		return
	}
	// Queue delay beyond the path's recent base RTT means we're filling a
	// buffer. The threshold scales with the path's own jitter so naturally
	// jumpy links (Starlink, cellular) aren't mistaken for congested ones.
	// Loss alone is a weak signal (Wi-Fi and cellular lose packets at
	// random), so it only counts when it is heavy.
	base := p.baseRTT(now, srtt)
	// The sliding baseline follows real path changes (a new satellite adds
	// tens of ms) but must not absorb sustained queueing of seconds, or
	// flooding a slow link would come to look normal.
	if minRTT > 0 {
		base = min(base, minRTT+max(minRTT, 150*time.Millisecond))
	}
	inflation := srtt - base
	dth := p.delayThresh
	if dth <= 0 {
		dth = 15 * time.Millisecond
	}
	// Allow for the path's own jitter, but cap it: on a slow link, huge
	// "jitter" is usually our own queue (we are flooding it), not the path.
	threshold := max(dth, base/2, min(2*p.jitter, 100*time.Millisecond))
	lth := p.lossThresh
	if lth <= 0 {
		lth = 0.10
	}
	// Latency several times the path's base is a full buffer, whatever the
	// jitter says.
	bloated := srtt > 3*base && inflation > 100*time.Millisecond
	congested := inflation > threshold || loss > lth || bloated
	// Delivery-rate check (as in BBR): if we keep sending clearly more than
	// arrives, a queue is growing or the link is dropping, even before the
	// RTT shows it. Measurements lag by up to a second, so require the
	// shortfall to persist.
	if delivered > 0 && p.useRate > 2e6 && delivered < 0.75*p.useRate {
		if p.shortSince.IsZero() {
			p.shortSince = now
		} else if now.Sub(p.shortSince) > time.Second {
			congested = true
		}
	} else {
		p.shortSince = time.Time{}
	}
	// Require two readings in a row: one RTT spike is not congestion.
	if congested {
		p.congStreak++
	} else {
		p.congStreak = 0
	}
	congested = p.congStreak >= 2
	switch {
	case congested:
		// Back off at most once per RTT-ish interval (capped: on a bloated
		// link the RTT itself is seconds long).
		if now.Sub(p.lastDown) > min(max(2*srtt, 100*time.Millisecond), 500*time.Millisecond) {
			p.rate *= 0.8
			// Far more sent than arrives: jump to about what actually arrives.
			if !p.shortSince.IsZero() && delivered > 0 && delivered < 0.5*p.rate {
				p.rate = 1.1 * delivered
			}
			p.lastDown = now
			p.learned = true
		}
	case p.limited:
		// In demand and healthy: probe for more. Grow fast while no
		// congestion has been seen recently, cautiously near capacity.
		step := 0.08
		if now.Sub(p.lastDown) > 3*time.Second {
			step = 0.25
		}
		target := p.rate + max(p.rate*step, 250e3)
		// Only grow while the link actually carries close to its budget:
		// TCP microbursts can look like demand. The learned rate itself is
		// kept through idle periods.
		ceiling := max(p.startRate, 1.5*p.useRate)
		if delivered > 0 {
			// What the link actually delivers caps growth, even below the
			// start rate: a 1.2 Mbit Wi-Fi must not be fed 20 Mbit of copies.
			ceiling = min(ceiling, max(minPaceRate, 1.4*delivered))
		}
		if target > ceiling {
			target = max(p.rate, ceiling)
		}
		if target != p.rate {
			p.learned = true
		}
		p.rate = target
	}
	if p.rate < minPaceRate {
		p.rate = minPaceRate
	}
	if p.maxRate > 0 && p.rate > p.maxRate {
		p.rate = p.maxRate
	}
	p.limited = false
}

// Rate returns the current paced rate in bits/s (0 if pacing is off).
func (p *Pacer) Rate() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return 0
	}
	return p.rate
}
