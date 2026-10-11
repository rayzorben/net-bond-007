package bond

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/bond007/internal/metrics"
)

type State uint8

const (
	StateConnecting State = iota
	StateUp
	StateDegraded
	StateDown
	StateDisabled
)

func (s State) String() string {
	return [...]string{"connecting", "up", "degraded", "down", "disabled"}[min(int(s), 4)]
}

// Usable reports whether a link in this state may carry data.
func (s State) Usable() bool { return s == StateUp || s == StateDegraded }

// Link is one path between client and server. The engine owns scheduling and
// counters; the owner (client or server) supplies the transport via send and
// updates health through Health().
type Link struct {
	ID   uint8
	Name string
	Kind string

	state  atomic.Uint32
	weight atomic.Uint64 // math.Float64bits
	backup atomic.Bool

	queue chan []byte
	done  chan struct{}
	send  func([]byte) error
	pool  *sync.Pool

	Tx, Rx           metrics.Counter
	Wins, Dups       atomic.Uint64
	Drops, Errors    atomic.Uint64
	lastErr          atomic.Pointer[string]
	LastRx           atomic.Int64 // unix nanos of last frame received
	healthMu         sync.Mutex
	health           Health
	closeOnce        sync.Once
	queueCap         int
	lastWins, lastRx uint64 // for per-interval win share (guarded by healthMu)

	// Pacer limits how fast this link is fed; see pacer.go.
	Pacer Pacer

	owd    atomic.Int64 // estimated one-way delay, nanoseconds
	rxBusy atomic.Bool  // carried real traffic in the last tick
}

// SilencePenalty is how much a link's expected delivery time should grow
// because it has gone quiet while the bond is busy. Within a grace period
// (~8 one-way delays, >=120ms; 600ms when the link only carries probes) there
// is no penalty. Beyond it, every extra millisecond of silence adds a
// millisecond of expected delay.
//
// This replaces excluding "silent" links outright. Satellite links like
// Starlink go quiet for 100-300ms during handovers. Excluding them moved
// traffic onto far worse links, or dropped it when every link was briefly
// quiet. With a penalty, a short blip still beats a link with 1.5s latency,
// while a genuinely dead link loses within about a second.
func (l *Link) SilencePenalty(nowN int64) time.Duration {
	last := l.LastRx.Load()
	if last == 0 {
		return 0
	}
	grace := int64(600 * time.Millisecond)
	if l.rxBusy.Load() {
		grace = min(max(8*l.owd.Load(), int64(120*time.Millisecond)), int64(time.Second))
	}
	if over := nowN - last - grace; over > 0 {
		return time.Duration(over)
	}
	return 0
}

// Silent reports whether the link is past its silence grace period.
func (l *Link) Silent(nowN int64) bool { return l.SilencePenalty(nowN) > 0 }

// SetOneWayDelay records the link's estimated one-way latency (RTT/2).
func (l *Link) SetOneWayDelay(d time.Duration) { l.owd.Store(int64(d)) }

// ArrivalDelay estimates when a packet queued now would arrive: one-way
// latency plus the time to drain our own send queue. Pacing debt is not
// counted: the primary copy is never paced, and counting debt once made a
// healthy link look slower than a 2-second one.
func (l *Link) ArrivalDelay(now time.Time) time.Duration {
	return time.Duration(l.owd.Load()) + time.Duration(len(l.queue))*100*time.Microsecond
}

// Health is the link's current measured condition.
type Health struct {
	RTT        metrics.RTT
	Loss       float64
	TxRate     metrics.Rate
	RxRate     metrics.Rate
	CapBps     float64 // best capacity estimate (measured or reported)
	ManualCap  float64 // user override (bps), 0 = none
	ManualW    float64 // user weight override, 0 = automatic
	Quality    float64
	History    *metrics.History
	LocalAddr  string
	RemoteAddr string
	Label      string
	WinShare   float64 // fraction of unique packets this link delivered first (last interval)
}

func newLink(id uint8, name, kind string, queueCap int, send func([]byte) error, pool *sync.Pool, history int) *Link {
	l := &Link{
		ID: id, Name: name, Kind: kind,
		queue:    make(chan []byte, queueCap),
		done:     make(chan struct{}),
		send:     send,
		pool:     pool,
		queueCap: queueCap,
	}
	l.health.History = metrics.NewHistory(history)
	l.SetWeight(0)
	go l.writer()
	return l
}

func (l *Link) writer() {
	for {
		select {
		case <-l.done:
			return
		case f := <-l.queue:
			if err := l.send(f); err != nil {
				l.Errors.Add(1)
				s := err.Error()
				l.lastErr.Store(&s)
			}
			l.pool.Put(f[:cap(f)])
		}
	}
}

// enqueue hands a frame to the writer; returns false if the queue is full.
func (l *Link) enqueue(f []byte) bool {
	select {
	case l.queue <- f:
		return true
	default:
		return false
	}
}

func (l *Link) close() { l.closeOnce.Do(func() { close(l.done) }) }

func (l *Link) State() State     { return State(l.state.Load()) }
func (l *Link) SetState(s State) { l.state.Store(uint32(s)) }
func (l *Link) Backup() bool     { return l.backup.Load() }
func (l *Link) SetBackup(b bool) { l.backup.Store(b) }
func (l *Link) Weight() float64  { return math.Float64frombits(l.weight.Load()) }
func (l *Link) SetWeight(w float64) {
	l.weight.Store(math.Float64bits(w))
}
func (l *Link) QueueLen() int { return len(l.queue) }

// Congested reports whether the send queue is nearly full.
func (l *Link) Congested() bool { return len(l.queue) >= l.queueCap*7/8 }

func (l *Link) LastError() string {
	if p := l.lastErr.Load(); p != nil {
		return *p
	}
	return ""
}

func (l *Link) SetError(s string) { l.lastErr.Store(&s) }

// Health runs fn with exclusive access to the link's health record.
func (l *Link) Health(fn func(h *Health)) {
	l.healthMu.Lock()
	fn(&l.health)
	l.healthMu.Unlock()
}

// SendRaw writes a control frame immediately (bypassing the data queue).
func (l *Link) SendRaw(f []byte) error { return l.send(f) }

// AutoWeight derives a scheduling weight from capacity and latency.
// Capacity dominates; with latencyWeighting, links slower than the best link
// are discounted (down to 30%) so latency-sensitive flows favour fast paths.
func AutoWeight(capBps float64, rtt, bestRTT time.Duration, latencyWeighting bool) float64 {
	mbps := capBps / 1e6
	if mbps < 1 {
		mbps = 1
	}
	if !latencyWeighting || rtt <= 0 || bestRTT <= 0 {
		return mbps
	}
	f := float64(bestRTT) / float64(rtt)
	if f < 0.3 {
		f = 0.3
	}
	if f > 1 {
		f = 1
	}
	return mbps * f
}

// HealthParams are the thresholds used to classify a link.
type HealthParams struct {
	DegradedRTT  time.Duration
	DegradedLoss float64 // 0..1
	DownLoss     float64 // 0..1
	DownAfter    time.Duration
}

// Classify turns raw measurements into a link state.
func Classify(p HealthParams, everAcked bool, sinceLastAck time.Duration, srtt time.Duration, loss float64) State {
	if !everAcked {
		if sinceLastAck > p.DownAfter {
			return StateDown
		}
		return StateConnecting
	}
	if sinceLastAck > p.DownAfter || (p.DownLoss > 0 && loss >= p.DownLoss) {
		return StateDown
	}
	if (p.DegradedRTT > 0 && srtt > p.DegradedRTT) || (p.DegradedLoss > 0 && loss > p.DegradedLoss) {
		return StateDegraded
	}
	return StateUp
}
