// Package metrics holds the per-link health estimators: RTT and jitter
// smoothing, probe-based loss, throughput meters and a rolling history used
// by the dashboard sparklines.
package metrics

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// RTT is an RFC 6298 style smoothed round-trip estimator.
type RTT struct {
	SRTT    time.Duration
	RTTVar  time.Duration // used as "jitter"
	Min     time.Duration
	Last    time.Duration
	Samples uint64
}

func (r *RTT) Update(sample time.Duration) {
	if sample <= 0 {
		sample = time.Microsecond
	}
	r.Last = sample
	r.Samples++
	if r.Samples == 1 {
		r.SRTT = sample
		r.RTTVar = sample / 2
		r.Min = sample
		return
	}
	if sample < r.Min {
		r.Min = sample
	}
	diff := r.SRTT - sample
	if diff < 0 {
		diff = -diff
	}
	r.RTTVar = (3*r.RTTVar + diff) / 4
	r.SRTT = (7*r.SRTT + sample) / 8
}

// ProbeTracker remembers recently sent probes to compute RTT and loss.
type ProbeTracker struct {
	slots   [128]probeSlot
	LastAck time.Time
}

type probeSlot struct {
	seq   uint32
	sent  time.Time
	acked bool
	valid bool
}

func (p *ProbeTracker) Sent(seq uint32, at time.Time) {
	p.slots[seq%uint32(len(p.slots))] = probeSlot{seq: seq, sent: at, valid: true}
}

// Acked marks seq acknowledged and returns its RTT. ok is false for unknown,
// duplicate or ancient acks.
func (p *ProbeTracker) Acked(seq uint32, now time.Time) (rtt time.Duration, ok bool) {
	s := &p.slots[seq%uint32(len(p.slots))]
	if !s.valid || s.seq != seq || s.acked {
		return 0, false
	}
	s.acked = true
	p.LastAck = now
	return now.Sub(s.sent), true
}

// Loss returns the fraction of probes older than timeout that were never
// acknowledged, considering at most the last `window` such probes.
func (p *ProbeTracker) Loss(now time.Time, timeout time.Duration, window int) float64 {
	var total, lost int
	cutoff := now.Add(-timeout)
	// Walk newest-first by sent time.
	type ss struct {
		t     time.Time
		acked bool
	}
	var buf [128]ss
	n := 0
	for _, s := range p.slots {
		if s.valid && !s.sent.After(cutoff) {
			buf[n] = ss{s.sent, s.acked}
			n++
		}
	}
	// Selection of the newest `window` without full sort: simple insertion sort
	// on at most 128 items is cheap at probe rates.
	for i := 1; i < n; i++ {
		for j := i; j > 0 && buf[j].t.After(buf[j-1].t); j-- {
			buf[j], buf[j-1] = buf[j-1], buf[j]
		}
	}
	for i := 0; i < n && i < window; i++ {
		total++
		if !buf[i].acked {
			lost++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(lost) / float64(total)
}

// Counter is a lock-free byte/packet counter pair.
type Counter struct {
	Packets atomic.Uint64
	Bytes   atomic.Uint64
}

func (c *Counter) Add(bytes int) {
	c.Packets.Add(1)
	c.Bytes.Add(uint64(bytes))
}

// Rate turns monotonically increasing byte counts into a bits/second rate,
// smoothed with an EWMA, and tracks a decaying peak used as a capacity hint.
type Rate struct {
	lastBytes uint64
	lastAt    time.Time
	BPS       float64 // smoothed bits per second
	Peak      float64 // decaying peak bits per second
}

func (r *Rate) Sample(totalBytes uint64, now time.Time) float64 {
	if r.lastAt.IsZero() {
		r.lastAt, r.lastBytes = now, totalBytes
		return 0
	}
	dt := now.Sub(r.lastAt).Seconds()
	if dt <= 0 {
		return r.BPS
	}
	inst := float64(totalBytes-r.lastBytes) * 8 / dt
	r.lastAt, r.lastBytes = now, totalBytes
	r.BPS = 0.5*r.BPS + 0.5*inst
	// Peak decays ~1% per second so a link that got slower is re-learned.
	r.Peak *= math.Pow(0.99, dt)
	if inst > r.Peak {
		r.Peak = inst
	}
	return r.BPS
}

// Sample is one point of link history, rendered by the UI.
type Sample struct {
	T      int64   `json:"t"`  // unix ms
	RTT    float64 `json:"r"`  // ms
	Jitter float64 `json:"j"`  // ms
	Loss   float64 `json:"l"`  // 0..1
	Tx     float64 `json:"tx"` // bits/s
	Rx     float64 `json:"rx"` // bits/s
	Wins   float64 `json:"w"`  // fraction of first-arrivals won in the interval
}

// History is a fixed-capacity ring of samples. Safe for concurrent use.
type History struct {
	mu   sync.Mutex
	buf  []Sample
	next int
	full bool
}

func NewHistory(n int) *History {
	if n < 2 {
		n = 2
	}
	return &History{buf: make([]Sample, n)}
}

func (h *History) Add(s Sample) {
	h.mu.Lock()
	h.buf[h.next] = s
	h.next = (h.next + 1) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
	h.mu.Unlock()
}

// Resize changes capacity, keeping the newest samples.
func (h *History) Resize(n int) {
	if n < 2 {
		n = 2
	}
	old := h.Snapshot(0)
	h.mu.Lock()
	defer h.mu.Unlock()
	if n == len(h.buf) {
		return
	}
	h.buf = make([]Sample, n)
	h.next, h.full = 0, false
	if len(old) > n {
		old = old[len(old)-n:]
	}
	for _, s := range old {
		h.buf[h.next] = s
		h.next = (h.next + 1) % n
		if h.next == 0 {
			h.full = true
		}
	}
}

// Snapshot returns samples oldest-first; if last > 0 only the newest `last`.
func (h *History) Snapshot(last int) []Sample {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []Sample
	if h.full {
		out = append(out, h.buf[h.next:]...)
	}
	out = append(out, h.buf[:h.next]...)
	if last > 0 && len(out) > last {
		out = out[len(out)-last:]
	}
	return out
}

// Quality folds RTT, jitter and loss into a 0..100 score for the UI.
func Quality(rtt, jitter time.Duration, loss float64) float64 {
	ms := float64(rtt) / float64(time.Millisecond)
	j := float64(jitter) / float64(time.Millisecond)
	// Effective latency, roughly as in the ITU E-model simplification.
	eff := ms/2 + 2*j + 10
	var r float64
	if eff < 160 {
		r = 93.2 - eff/40
	} else {
		r = 93.2 - (eff-120)/10
	}
	r -= loss * 100 * 2.5
	if r < 0 {
		r = 0
	}
	if r > 93.2 {
		r = 93.2
	}
	return math.Round(r / 93.2 * 100)
}
