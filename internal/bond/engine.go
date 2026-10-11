// Package bond is the multipath engine shared by client and server.
//
// Send direction: each outbound IP packet gets the next sequence number. The
// scheduler picks one or more links and a copy of the framed packet is queued
// on each.
//
// Receive direction: frames from any link are de-duplicated by sequence
// number. The first copy to arrive wins and the rest are dropped. Surviving
// packets pass through an optional reorder buffer and are then delivered (to
// the TUN device on the client, or to the userspace TCP/IP stack on the server).
package bond

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/bond007/internal/dedup"
	"github.com/rayzorben/bond007/internal/metrics"
	"github.com/rayzorben/bond007/internal/proto"
	"github.com/rayzorben/bond007/internal/reorder"
	"github.com/rayzorben/bond007/internal/sched"
)

const frameBuf = 2048 + proto.DataHeaderLen

type Engine struct {
	Session uint32

	mu      sync.Mutex
	links   map[uint8]*Link
	ordered atomic.Pointer[[]*Link]

	sched  *sched.Scheduler
	policy atomic.Pointer[sched.Policy]
	txSeq  atomic.Uint64
	win    dedup.Window

	rmu     sync.Mutex
	reorder reorder.Buffer
	rtimer  *time.Timer
	deliver func([]byte)

	pool sync.Pool

	UniqueTx, UniqueRx metrics.Counter
	NoLink             atomic.Uint64 // packets dropped: no usable link
	TooOld             atomic.Uint64
	txRate, rxRate     metrics.Rate
	TxBps, RxBps       atomic.Uint64
	queueCap           atomic.Int64
	history            atomic.Int64
}

// New creates an engine. deliver receives de-duplicated, ordered packets.
func New(session uint32, deliver func([]byte)) *Engine {
	e := &Engine{
		Session: session,
		links:   map[uint8]*Link{},
		sched:   sched.New(),
		deliver: deliver,
	}
	e.pool.New = func() any { return make([]byte, frameBuf) }
	e.ordered.Store(&[]*Link{})
	e.SetPolicy(sched.Policy{Mode: sched.ModeSmart, Redundancy: 2})
	e.queueCap.Store(1024)
	e.history.Store(300)
	e.rtimer = time.AfterFunc(time.Hour, e.expire)
	e.rtimer.Stop()
	return e
}

func (e *Engine) SetPolicy(p sched.Policy) { e.policy.Store(&p) }
func (e *Engine) Policy() sched.Policy     { return *e.policy.Load() }
func (e *Engine) SetQueueDepth(n int)      { e.queueCap.Store(int64(n)) }
func (e *Engine) SetHistory(n int) {
	e.history.Store(int64(n))
	for _, l := range e.Links() {
		l.Health(func(h *Health) { h.History.Resize(n) })
	}
}

// SetReorderHold changes the reorder hold time (0 disables).
func (e *Engine) SetReorderHold(d time.Duration) {
	e.rmu.Lock()
	e.reorder.MaxHold = d
	e.rmu.Unlock()
}

func (e *Engine) ReorderStats() (reorder.Stats, time.Duration, int) {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	return e.reorder.Stats, e.reorder.MaxHold, e.reorder.Held()
}

// ResetRx forgets receive-side history (dedup window and reorder state). Used
// when the peer's sequence numbers restart.
func (e *Engine) ResetRx() {
	e.win.Reset()
	e.rmu.Lock()
	e.reorder = reorder.Buffer{MaxHold: e.reorder.MaxHold}
	e.rmu.Unlock()
}

// AddLink registers a path. send performs the actual transmission.
func (e *Engine) AddLink(id uint8, name, kind string, send func([]byte) error) *Link {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old, ok := e.links[id]; ok {
		old.close()
	}
	l := newLink(id, name, kind, int(e.queueCap.Load()), send, &e.pool, int(e.history.Load()))
	e.links[id] = l
	e.publish()
	return l
}

func (e *Engine) RemoveLink(id uint8) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l, ok := e.links[id]; ok {
		l.close()
		delete(e.links, id)
		e.sched.Forget(id)
		e.publish()
	}
}

func (e *Engine) Link(id uint8) *Link {
	for _, l := range *e.ordered.Load() {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// Links returns a snapshot ordered by id.
func (e *Engine) Links() []*Link { return *e.ordered.Load() }

func (e *Engine) publish() {
	ls := make([]*Link, 0, len(e.links))
	for _, l := range e.links {
		ls = append(ls, l)
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID })
	e.ordered.Store(&ls)
}

// Close stops all link writers.
func (e *Engine) Close() {
	e.mu.Lock()
	for id, l := range e.links {
		l.close()
		delete(e.links, id)
	}
	e.publish()
	e.mu.Unlock()
	e.rtimer.Stop()
}

// Send schedules one IP packet over the bond. pkt is copied; the caller may
// reuse it immediately. Returns the number of copies queued.
func (e *Engine) Send(pkt []byte) int {
	links := *e.ordered.Load()
	now := time.Now()
	nowN := now.UnixNano()
	// Silence only means something while other links are still talking.
	busy := false
	for _, l := range links {
		if nowN-l.LastRx.Load() < int64(50*time.Millisecond) {
			busy = true
			break
		}
	}
	var cbuf [16]sched.Candidate
	cands := cbuf[:0]
	for _, l := range links {
		cands = append(cands, sched.Candidate{
			ID:      l.ID,
			Weight:  l.Weight(),
			Usable:  l.State().Usable() && !l.Congested(),
			Backup:  l.Backup(),
			Degrade: l.State() == StateDegraded,
			Delay:   l.ArrivalDelay(now) + penalty(busy, l, nowN),
		})
	}
	var ebuf [16]sched.Candidate
	el := sched.Eligible(cands, ebuf[:0])
	if len(el) == 0 {
		// Every link congested? Fall back to any up link rather than drop.
		for i := range cands {
			l := links[i]
			cands[i].Usable = l.State().Usable()
		}
		el = sched.Eligible(cands, ebuf[:0])
		if len(el) == 0 {
			e.NoLink.Add(1)
			return 0
		}
	}
	pol := e.policy.Load()
	r := pol.Copies(len(el), sched.Inspect(pkt))
	wire := len(pkt) + proto.DataHeaderLen
	// The first copy always goes on the best link and is never paced: TCP's
	// own congestion control governs it. Pacing only limits the extra,
	// redundant copies so slower links aren't drowned in duplicates.
	var ibuf [16]uint8
	ids := e.sched.Choose(pol, el, 1, ibuf[:0])
	if r > 1 && len(ids) == 1 {
		var pbuf [16]sched.Candidate
		paced := pbuf[:0]
		for _, c := range el {
			if c.ID == ids[0] {
				continue
			}
			if l := linkByID(links, c.ID); l != nil && l.Pacer.Has(wire, now) {
				paced = append(paced, c)
			}
		}
		if len(paced) > 0 {
			var xbuf [16]uint8
			ids = append(ids, e.sched.Choose(pol, paced, min(r-1, len(paced)), xbuf[:0])...)
		}
	}
	for _, id := range ids {
		if l := linkByID(links, id); l != nil {
			l.Pacer.Force(wire, now)
		}
	}

	seq := e.txSeq.Add(1)
	e.UniqueTx.Add(len(pkt))
	sent := 0
	for _, id := range ids {
		l := linkByID(links, id)
		if l == nil {
			continue
		}
		buf := e.pool.Get().([]byte)
		f := proto.AppendData(buf[:0], e.Session, id, seq, pkt)
		if l.enqueue(f) {
			l.Tx.Add(len(f))
			sent++
		} else {
			l.Drops.Add(1)
			e.pool.Put(buf)
		}
	}
	return sent
}

// penalty applies silence penalties only while other links are talking.
func penalty(busy bool, l *Link, nowN int64) time.Duration {
	if !busy {
		return 0
	}
	return l.SilencePenalty(nowN)
}

func linkByID(links []*Link, id uint8) *Link {
	for _, l := range links {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// Receive processes a DATA frame that arrived on link. The frame buffer may be
// reused by the caller after Receive returns.
func (e *Engine) Receive(l *Link, frame []byte) {
	seq, payload, err := proto.ParseData(frame)
	if err != nil {
		return
	}
	l.Rx.Add(len(frame))
	l.LastRx.Store(time.Now().UnixNano())
	switch e.win.Check(seq) {
	case dedup.Duplicate:
		l.Dups.Add(1)
		return
	case dedup.TooOld:
		l.Dups.Add(1)
		e.TooOld.Add(1)
		return
	}
	l.Wins.Add(1)
	e.UniqueRx.Add(len(payload))

	e.rmu.Lock()
	defer e.rmu.Unlock()
	if e.reorder.WillHold(seq) {
		// The buffer keeps the packet, so it must own a copy.
		p := make([]byte, len(payload))
		copy(p, payload)
		payload = p
	}
	e.reorder.Push(seq, payload, time.Now(), e.deliver)
	e.armTimer()
}

func (e *Engine) armTimer() {
	if d := e.reorder.NextDeadline(); !d.IsZero() {
		e.rtimer.Reset(time.Until(d) + 100*time.Microsecond)
	}
}

func (e *Engine) expire() {
	e.rmu.Lock()
	defer e.rmu.Unlock()
	e.reorder.Expire(time.Now(), e.deliver)
	e.armTimer()
}

// Tick samples rates and records one history point per link. Call once per
// second (the interval is measured, so jitter in calling is fine).
func (e *Engine) Tick(now time.Time) {
	e.TxBps.Store(uint64(e.txRate.Sample(e.UniqueTx.Bytes.Load(), now)))
	e.RxBps.Store(uint64(e.rxRate.Sample(e.UniqueRx.Bytes.Load(), now)))
	links := e.Links()
	var totalWins uint64
	wins := make([]uint64, len(links))
	for i, l := range links {
		w := l.Wins.Load()
		l.healthMu.Lock()
		wins[i] = w - l.lastWins
		l.lastWins = w
		l.healthMu.Unlock()
		totalWins += wins[i]
	}
	for i, l := range links {
		l.Health(func(h *Health) {
			tx := h.TxRate.Sample(l.Tx.Bytes.Load(), now)
			rx := h.RxRate.Sample(l.Rx.Bytes.Load(), now)
			l.rxBusy.Store(rx > 500e3)
			share := 0.0
			if totalWins > 0 {
				share = float64(wins[i]) / float64(totalWins)
			}
			h.WinShare = share
			h.Quality = metrics.Quality(h.RTT.SRTT, h.RTT.RTTVar, h.Loss)
			h.History.Add(metrics.Sample{
				T:      now.UnixMilli(),
				RTT:    ms(h.RTT.SRTT),
				Jitter: ms(h.RTT.RTTVar),
				Loss:   h.Loss,
				Tx:     tx,
				Rx:     rx,
				Wins:   share,
			})
		})
	}
}

// AutoReorderHold computes a hold time from the one-way latency spread of
// usable links, capped at max.
func (e *Engine) AutoReorderHold(max time.Duration) time.Duration {
	var lo, hi time.Duration
	for _, l := range e.Links() {
		if !l.State().Usable() {
			continue
		}
		var r time.Duration
		l.Health(func(h *Health) { r = h.RTT.SRTT + 2*h.RTT.RTTVar })
		if r <= 0 {
			continue
		}
		if lo == 0 || r < lo {
			lo = r
		}
		if r > hi {
			hi = r
		}
	}
	d := (hi-lo)/2 + 5*time.Millisecond
	if d > max {
		d = max
	}
	return d
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
