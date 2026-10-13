package bond

import (
	"math"
	"sort"
	"time"
)

// Reliability is a link's long-run delivery score in [0,1], the product of
// two parts:
//
//   - delivery: the fraction of probes that get answered (symmetric average).
//     A Wi-Fi that drops 13% of packets at random scores 0.87, not "flaky".
//   - availability: driven only by real outages (a second or more of total
//     silence). It falls quickly during an outage (fall time constant) and
//     recovers slowly afterwards (rise time constant), so a link that keeps
//     cutting out has to earn trust back.
type Reliability struct {
	delivery  float64
	avail     float64
	missSince time.Time
	last      time.Time
	init      bool
}

const (
	// startReliability is where a new link begins: trusted enough to carry
	// traffic, not trusted enough to carry it alone.
	startReliability = 0.9
	outageAfter      = time.Second
	deliveryTau      = 30 * time.Second
)

func ewma(v, sample float64, dt, tau time.Duration) float64 {
	if tau <= 0 {
		return sample
	}
	return v + (1-math.Exp(-float64(dt)/float64(tau)))*(sample-v)
}

// Observe records whether the link delivered during the interval ending now.
func (r *Reliability) Observe(ok bool, now time.Time, fall, rise time.Duration) {
	if !r.init {
		r.delivery, r.avail, r.last, r.init = startReliability, 1, now, true
	}
	dt := now.Sub(r.last)
	r.last = now
	if dt <= 0 {
		return
	}
	if ok {
		r.missSince = time.Time{}
		r.delivery = ewma(r.delivery, 1, dt, deliveryTau)
		r.avail = ewma(r.avail, 1, dt, rise)
		return
	}
	r.delivery = ewma(r.delivery, 0, dt, deliveryTau)
	if r.missSince.IsZero() {
		r.missSince = now.Add(-dt)
	}
	if now.Sub(r.missSince) >= outageAfter {
		r.avail = ewma(r.avail, 0, dt, fall)
	}
}

// Value returns the score (startReliability before any observation).
func (r *Reliability) Value() float64 {
	if !r.init {
		return startReliability
	}
	return r.delivery * r.avail
}

// CopiesFor returns how many of the given links (by reliability) a packet
// must travel on so that the chance of losing every copy is at most
// 1-target, assuming independent links. It never returns more than the
// number of links, nor fewer than 1.
//
// Two flaky links (0.8 each) need both: 0.2*0.2 = 4% > 0.5%. Two clean links
// (0.999) need one: 0.1% <= 0.5%, so they can be load-balanced for speed.
func CopiesFor(rels []float64, target float64) int {
	if len(rels) == 0 {
		return 0
	}
	rs := append([]float64(nil), rels...)
	sort.Sort(sort.Reverse(sort.Float64Slice(rs)))
	allowed := 1 - target
	pLoseAll := 1.0
	for i, r := range rs {
		pLoseAll *= 1 - math.Max(0, math.Min(1, r))
		if pLoseAll <= allowed {
			return i + 1
		}
	}
	return len(rs)
}
