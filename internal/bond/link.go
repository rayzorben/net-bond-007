package bond

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/net-bond-007/internal/metrics"
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

	state   atomic.Uint32
	weight  atomic.Uint64 // math.Float64bits
	backup  atomic.Bool
	benched atomic.Bool

	// Metered links (phone tethering, capped data plans) carry at most a
	// share of the traffic, banked as a byte budget, except as failover.
	metered    atomic.Bool
	meterShare atomic.Uint64 // math.Float64bits of 0..1
	meterMu    sync.Mutex
	meterBank  float64 // bytes this link may still carry

	queue chan []byte
	// prio carries repairs ahead of queued data: a repair stuck behind a
	// deep queue arrives after the receiver's reorder hold gave up on it.
	prio chan []byte
	done chan struct{}
	send func([]byte) error
	pool *sync.Pool

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
	// Shaper smooths bursts to the delivery rate; see shaper.go.
	Shaper Shaper

	// FEC; see fec.go.
	session      uint32
	fec          fecEncoder
	fecOut       func(seqs []uint64, lenXor uint16, parity []byte)
	FECSent      atomic.Uint64 // parity frames sent
	FECRecovered atomic.Uint64 // packets rebuilt from this link's parity
	peerFEC      atomic.Uint64 // the peer's FECRecovered for this link
	lossLost     atomic.Uint64 // packets the peer NACKed that went only on this link
	// Recent losses on this link (sender side), to spot a loss storm.
	storm stormMeter
	// Receive-side silence watch (nackLoop only); see quiet.go.
	rxw rxWatch
	// The peer reported this link silent until then (unix nanos).
	peerQuiet atomic.Int64
	// FEC sizing state, guarded by healthMu.
	fecLastTx, fecLastLost uint64
	fecLoss                float64
	fecQuiet               time.Time

	owd    atomic.Int64 // estimated one-way delay, nanoseconds
	rxBusy atomic.Bool  // carried real traffic in the last tick
	quiet  bool         // last seen quiet/down by the reinjection check (nackLoop only)
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

// QuietVs reports whether the link has gone quiet while other links are
// still heard: nothing received for its grace period, and another link has
// received since. (Comparing with the others avoids calling every link quiet
// when the whole connection is merely idle.)
func (l *Link) QuietVs(all []*Link, nowN int64) bool {
	if !l.Silent(nowN) {
		return false
	}
	last := l.LastRx.Load()
	grace := nowN - last - int64(l.SilencePenalty(nowN)) // the grace period
	for _, o := range all {
		if o != l && o.LastRx.Load()-last > grace {
			return true
		}
	}
	return false
}

// SetOneWayDelay records the link's estimated one-way latency (RTT/2).
func (l *Link) SetOneWayDelay(d time.Duration) { l.owd.Store(int64(d)) }

// QueueDrain estimates how long the link's send queue takes to drain.
func (l *Link) QueueDrain(now time.Time) time.Duration {
	q := len(l.queue)
	if q == 0 {
		return 0
	}
	r := max(l.Shaper.ActiveRate(now), l.Pacer.Rate())
	if r <= 0 {
		return 0 // rate unknown (pacing off): Congested() still guards the queue
	}
	return time.Duration(float64(q*1300*8) / r * float64(time.Second))
}

// ArrivalDelay estimates when a packet queued now would arrive: one-way
// latency plus the time to drain our own send queue. Pacing debt is not
// counted: the primary copy is never paced, and counting debt once made a
// healthy link look slower than a 2-second one.
func (l *Link) ArrivalDelay(now time.Time) time.Duration {
	if d := l.QueueDrain(now); d > 0 {
		// Real drain time at the link's learned rate: a slow link with a
		// queue must not look "fast" just because its base latency is low.
		return time.Duration(l.owd.Load()) + d
	}
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
	// Reliability is the long-run delivery score used by automatic scaling
	// (set by the side that measures it: the client).
	Reliability float64
}

func newLink(id uint8, name, kind string, queueCap int, send func([]byte) error, pool *sync.Pool, history int) *Link {
	l := &Link{
		ID: id, Name: name, Kind: kind,
		queue:    make(chan []byte, queueCap),
		prio:     make(chan []byte, prioCap),
		done:     make(chan struct{}),
		send:     send,
		pool:     pool,
		queueCap: queueCap,
	}
	l.health.History = metrics.NewHistory(history)
	l.fecOut = l.emitFEC
	l.SetWeight(0)
	go l.writer()
	return l
}

const prioCap = 256

func (l *Link) writer() {
	for {
		var f []byte
		select {
		case f = <-l.prio:
		default:
			select {
			case <-l.done:
				return
			case f = <-l.prio:
			case f = <-l.queue:
			}
		}
		if d := l.Shaper.Delay(len(f), time.Now()); d > 0 {
			time.Sleep(d)
		}
		if err := l.send(f); err != nil {
			l.Errors.Add(1)
			s := err.Error()
			l.lastErr.Store(&s)
		}
		l.pool.Put(f[:cap(f)])
	}
}

// enqueueRepair queues a repair ahead of ordinary data (falling back to the
// data queue when the priority lane is full).
func (l *Link) enqueueRepair(f []byte) bool {
	select {
	case l.prio <- f:
		return true
	default:
		return l.enqueue(f)
	}
}

// RepairDelay estimates when a repair queued now would arrive.
func (l *Link) RepairDelay(now time.Time) time.Duration {
	if len(l.prio) >= prioCap {
		return l.ArrivalDelay(now)
	}
	return time.Duration(l.owd.Load()) + time.Duration(len(l.prio))*300*time.Microsecond
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

// SetMetered marks the link as metered with the share of traffic it may
// carry (0 = failover only).
func (l *Link) SetMetered(on bool, share float64) {
	l.metered.Store(on)
	l.meterShare.Store(math.Float64bits(max(0, min(1, share))))
}

func (l *Link) Metered() bool       { return l.metered.Load() }
func (l *Link) MeterShare() float64 { return math.Float64frombits(l.meterShare.Load()) }

const maxMeterBank = 2 << 20 // bytes: allow short bursts, not hoarding

// minAltBps: an unmetered link slower than this (a struggling campground
// Wi-Fi) is not a real alternative to a metered failover link.
const minAltBps = 3e6

// IsAlternative reports whether this unmetered link can stand in for a
// metered one: up, trusted (not benched), still talking, and with useful
// capacity. (Degraded alone doesn't disqualify: a loaded Starlink often reads
// degraded while still moving 40 Mbit.) While none is, metered links carry
// everything.
func (l *Link) IsAlternative(all []*Link, nowN int64) bool {
	if l.Metered() || !l.State().Usable() || l.Benched() || l.QuietVs(all, nowN) {
		return false
	}
	r := l.Pacer.Rate()
	if r == 0 {
		l.Health(func(h *Health) { r = h.CapBps })
	}
	return r == 0 || r >= minAltBps
}

// earnMeter credits the link with its share of a packet sent on the bond.
func (l *Link) earnMeter(n int) {
	if s := l.MeterShare(); s > 0 {
		l.meterMu.Lock()
		l.meterBank = min(maxMeterBank, l.meterBank+float64(n)*s)
		l.meterMu.Unlock()
	}
}

func (l *Link) meterHas(n int) bool {
	l.meterMu.Lock()
	defer l.meterMu.Unlock()
	return l.meterBank >= float64(n)
}

func (l *Link) spendMeter(n int) {
	l.meterMu.Lock()
	l.meterBank -= float64(n)
	l.meterMu.Unlock()
}

// Benched: automatic scaling stopped using this link (it is still probed).
func (l *Link) Benched() bool     { return l.benched.Load() }
func (l *Link) SetBenched(b bool) { l.benched.Store(b) }
func (l *Link) Weight() float64   { return math.Float64frombits(l.weight.Load()) }
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
