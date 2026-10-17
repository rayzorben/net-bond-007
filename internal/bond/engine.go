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
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/net-bond-007/internal/dedup"
	"github.com/rayzorben/net-bond-007/internal/metrics"
	"github.com/rayzorben/net-bond-007/internal/proto"
	"github.com/rayzorben/net-bond-007/internal/reorder"
	"github.com/rayzorben/net-bond-007/internal/sched"
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

	// In-tunnel retransmission; see arq.go.
	arq                  atomic.Bool
	rtx                  rtxBuffer
	gaps                 gapTracker
	Retransmits          atomic.Uint64
	RtxPremature         atomic.Uint64 // NACKed packets still in flight: not repaired
	Reinjected           atomic.Uint64 // resent at once because their link went quiet
	NacksSent, NacksRecv atomic.Uint64
	closed               chan struct{}
	closeOnce            sync.Once

	flows flowTable // per-connection tracking; see flows.go

	// Forward error correction; see fec.go.
	fecOn                atomic.Bool
	fecRx                fecStore
	FECRecovered, FECBad atomic.Uint64
}

// SetUpstream marks this engine as the client side (what it sends travels
// toward the internet). Used to orient connection tracking.
func (e *Engine) SetUpstream(up bool) {
	e.flows.mu.Lock()
	e.flows.upstream = up
	e.flows.mu.Unlock()
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
	e.closed = make(chan struct{})
	go e.nackLoop()
	return e
}

// SetARQ turns in-tunnel retransmission on or off.
func (e *Engine) SetARQ(on bool) { e.arq.Store(on) }
func (e *Engine) ARQ() bool      { return e.arq.Load() }

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
	e.gaps.reset()
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
	l.session = e.Session
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
	// Record open connections while their links still have names.
	e.flows.drain()
	e.mu.Lock()
	for id, l := range e.links {
		l.close()
		delete(e.links, id)
	}
	e.publish()
	e.mu.Unlock()
	e.rtimer.Stop()
	e.closeOnce.Do(func() { close(e.closed) })
}

// Send schedules one IP packet over the bond. pkt is copied; the caller may
// reuse it immediately. Returns the number of copies queued.
func (e *Engine) Send(pkt []byte) int {
	links := *e.ordered.Load()
	now := time.Now()
	nowN := now.UnixNano()
	wire := len(pkt) + proto.DataHeaderLen
	// Metered links earn their share of everything sent, and may carry a copy
	// only while they have budget, unless no unmetered link is a real
	// alternative (down, degraded, benched or too slow): then they are the
	// failover and carry everything.
	othersAlive := false
	for _, l := range links {
		l.earnMeter(wire)
		othersAlive = othersAlive || l.IsAlternative(links, nowN)
	}
	var cbuf [16]sched.Candidate
	cands := cbuf[:0]
	for _, l := range links {
		cands = append(cands, sched.Candidate{
			ID:      l.ID,
			Weight:  l.Weight(),
			Usable:  l.State().Usable(),
			Backup:  l.Backup(),
			Benched: l.Benched(),
			Metered: l.Metered() && othersAlive && !l.meterHas(wire),
			Degrade: l.State() == StateDegraded,
			Delay:   l.ArrivalDelay(now) + penalty(links, l, nowN),
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
			// A redundant copy is only worth sending if it can arrive in time:
			// skip links whose own queue is already deep.
			if l := linkByID(links, c.ID); l != nil && !l.Congested() && l.QueueDrain(now) < 100*time.Millisecond && l.Pacer.Has(wire, now) {
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
			if l.Metered() {
				l.spendMeter(wire)
			}
		}
	}
	e.flows.note(pkt, true, ids, now)

	seq := e.txSeq.Add(1)
	e.UniqueTx.Add(len(pkt))
	if e.arq.Load() {
		var arrive time.Time
		var carriers uint64
		for _, id := range ids {
			carriers |= linkBit(id)
			if l := linkByID(links, id); l != nil {
				if a := now.Add(l.ArrivalDelay(now)); arrive.IsZero() || a.Before(arrive) {
					arrive = a
				}
			}
		}
		e.rtx.put(seq, pkt, arrive.UnixNano(), carriers)
	}
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
			l.fec.add(seq, pkt, now, l.fecOut)
		} else {
			l.Drops.Add(1)
			e.pool.Put(buf)
		}
	}
	return sent
}

// penalty applies a link's silence penalty when it went quiet while other
// links are still heard (silence means nothing if the whole connection is
// idle). This must not depend on whether anything arrived in the last few
// milliseconds: after a stall, a dead link would look like the fastest.
func penalty(links []*Link, l *Link, nowN int64) time.Duration {
	if !l.QuietVs(links, nowN) {
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
	now := time.Now()
	l.LastRx.Store(now.UnixNano())
	e.accept(l, seq, payload, now)
}

// accept runs a received (or FEC-rebuilt) packet through de-duplication and
// the reorder buffer. It reports whether the packet was new.
func (e *Engine) accept(l *Link, seq uint64, payload []byte, now time.Time) bool {
	switch e.win.Check(seq) {
	case dedup.Duplicate:
		l.Dups.Add(1)
		return false
	case dedup.TooOld:
		l.Dups.Add(1)
		e.TooOld.Add(1)
		return false
	}
	l.Wins.Add(1)
	e.UniqueRx.Add(len(payload))
	e.fecRx.remember(seq, payload, now.UnixNano(), e.fecOn.Load())
	if e.arq.Load() {
		e.gaps.seen(seq, now)
	}
	winner := [1]uint8{l.ID}
	e.flows.note(payload, false, winner[:], now)

	e.rmu.Lock()
	defer e.rmu.Unlock()
	if e.reorder.WillHold(seq) {
		// The buffer keeps the packet, so it must own a copy.
		p := make([]byte, len(payload))
		copy(p, payload)
		payload = p
	}
	e.reorder.Push(seq, payload, now, e.deliver)
	e.armTimer()
	return true
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
	e.flows.tick(now)
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
	fec := e.fecOn.Load()
	for _, l := range links {
		if fec {
			e.updateFEC(l, now)
		}
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
	// Only links that actually deliver first copies matter for ordering. A
	// slow link that only carries redundant duplicates (a 1-second campground
	// Wi-Fi next to Starlink) must not make every gap wait for it.
	winners := 0
	for _, l := range e.Links() {
		var share float64
		l.Health(func(h *Health) { share = h.WinShare })
		if l.State().Usable() && share >= 0.02 {
			winners++
		}
	}
	var lo, hi time.Duration
	for _, l := range e.Links() {
		if !l.State().Usable() {
			continue
		}
		var r time.Duration
		var share float64
		l.Health(func(h *Health) { r, share = h.RTT.SRTT+2*h.RTT.RTTVar, h.WinShare })
		if r <= 0 || (winners > 0 && share < 0.02) {
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

// ---------------------------------------------------------------- ARQ

// fastest returns up to n usable links ordered by estimated arrival.
// withMetered includes metered links as ordinary candidates (tiny control
// frames like NACKs); otherwise they are used only when no unmetered link is
// a real alternative.
func (e *Engine) fastest(n int, now time.Time, withMetered bool) []*Link {
	// Metered links (capped phone plans) carry repairs only when nothing
	// else can: repairs are pure overhead on someone's data allowance.
	var ls, metered, benched []*Link
	all := e.Links()
	for _, l := range all {
		switch {
		case !l.State().Usable():
		case l.Metered():
			metered = append(metered, l)
		case l.Benched():
			benched = append(benched, l)
		default:
			ls = append(ls, l)
		}
	}
	nowN := now.UnixNano()
	alt := false
	for _, l := range ls {
		alt = alt || l.IsAlternative(all, nowN)
	}
	if withMetered || !alt {
		ls = append(ls, metered...)
	}
	if len(ls) == 0 {
		ls = benched
	}
	sort.Slice(ls, func(i, j int) bool {
		di := ls[i].ArrivalDelay(now) + ls[i].SilencePenalty(nowN)
		dj := ls[j].ArrivalDelay(now) + ls[j].SilencePenalty(nowN)
		if ls[i].State() == StateDegraded {
			di = di*4 + 20*time.Millisecond
		}
		if ls[j].State() == StateDegraded {
			dj = dj*4 + 20*time.Millisecond
		}
		return di < dj
	})
	if len(ls) > n {
		ls = ls[:n]
	}
	return ls
}

// bestPath returns the smoothed RTT and jitter of the fastest usable link.
func (e *Engine) bestPath(now time.Time) (srtt, jitter time.Duration) {
	ls := e.fastest(1, now, false)
	if len(ls) == 0 {
		return 0, 0
	}
	ls[0].Health(func(h *Health) { srtt, jitter = h.RTT.SRTT, h.RTT.RTTVar })
	return srtt, jitter
}

// nackTiming: how long to wait before NACKing a hole (tolerates jitter), and
// how long to wait for a repair before asking again.
func (e *Engine) nackTiming(now time.Time) (wait, retry time.Duration) {
	srtt, jit := e.bestPath(now)
	var best time.Duration
	if ls := e.fastest(1, now, false); len(ls) > 0 {
		best = time.Duration(ls[0].owd.Load())
	}
	// Packets that win races on links of similar speed legitimately arrive out
	// of order by up to their one-way-delay spread; waiting that long avoids
	// NACKing packets that are merely late. Only comparable links count: a
	// slow backup that wins only when the primary drops a packet (seconds of
	// latency on bad Wi-Fi) must not stretch the wait past the reorder hold.
	var hi time.Duration
	for _, l := range e.Links() {
		var share float64
		l.Health(func(h *Health) { share = h.WinShare })
		d := time.Duration(l.owd.Load())
		if !l.State().Usable() || share < 0.02 || d > 2*best+50*time.Millisecond {
			continue
		}
		hi = max(hi, d)
	}
	wait = max(15*time.Millisecond, jit, hi-best+5*time.Millisecond)
	// Give FEC its chance to rebuild the packet before asking for a repair.
	if sp := e.fecRx.Span(now); sp > 0 {
		wait = max(wait, sp+sp/4+2*time.Millisecond)
	}
	// Never wait so long that the repair can't arrive within the hold.
	wait = min(wait, max(25*time.Millisecond, srtt/2))
	retry = max(50*time.Millisecond, srtt+srtt/2)
	return wait, retry
}

// ARQHold is the reorder hold needed for a NACKed packet's repair to arrive:
// detection wait + one round trip + margin, capped at max.
func (e *Engine) ARQHold(max time.Duration) time.Duration {
	now := time.Now()
	srtt, _ := e.bestPath(now)
	wait, _ := e.nackTiming(now)
	// Cover a repair that needs a second NACK round plus queueing. Measured
	// on an emulated Starlink path: holds that give up before late repairs
	// arrive cause spurious fast recoveries in the inner TCP (each halves its
	// window), which cost far more than the brief wait.
	d := wait + srtt*5/2 + 40*time.Millisecond
	return min(d, max)
}

func (e *Engine) nackLoop() {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-e.closed:
			return
		case now := <-t.C:
			if e.fecOn.Load() {
				for _, l := range e.Links() {
					l.fec.flushOld(now, l.fecOut)
				}
			}
			e.retryFEC(now)
			if !e.arq.Load() {
				continue
			}
			e.reinjectQuiet(now)
			if e.gaps.pendingCount() == 0 {
				continue
			}
			wait, retry := e.nackTiming(now)
			ranges := e.gaps.due(now, wait, retry)
			for len(ranges) > 0 {
				n := min(len(ranges), proto.MaxNackRanges)
				// Send the NACK on the two fastest links: it is tiny and its
				// loss would cost a whole retry interval.
				for _, l := range e.fastest(2, now, true) {
					l.SendRaw(proto.AppendNack(nil, e.Session, l.ID, ranges[:n]))
				}
				e.NacksSent.Add(1)
				ranges = ranges[n:]
			}
		}
	}
}

// reinjectQuiet: the moment a link goes quiet (or down) while others are
// still heard, resend on the surviving links everything that went only on
// it since it was last heard from. The receiver would otherwise discover
// those losses one NACK round at a time, or not at all for the tail, and the
// inner TCP would see a whole window lost at once (and crawl in recovery).
func (e *Engine) reinjectQuiet(now time.Time) {
	nowN := now.UnixNano()
	all := e.Links()
	for _, l := range all {
		q := !l.State().Usable() || l.QuietVs(all, nowN)
		was := l.quiet
		l.quiet = q
		if !q || was || l.LastRx.Load() == 0 {
			continue
		}
		since := l.LastRx.Load() - l.owd.Load()
		seqs := e.rtx.onlyOn(linkBit(l.ID), since)
		if len(seqs) == 0 {
			continue
		}
		cands := e.fastest(4, now, true)
		n := 0
		for _, seq := range seqs {
			pkt, ok := e.rtx.get(seq)
			if !ok {
				continue
			}
			carriers, _ := e.rtx.carriers(seq)
			// tries=1: the link is gone, so metered links may carry these.
			links := repairLinks(cands, all, carriers, 1, len(pkt)+proto.DataHeaderLen, nowN, now)
			if len(links) == 0 || links[0] == l {
				continue
			}
			to := links[0]
			buf := e.pool.Get().([]byte)
			f := proto.AppendData(buf[:0], e.Session, to.ID, seq, pkt)
			if to.enqueueRepair(f) {
				to.Tx.Add(len(f))
				if to.Metered() {
					to.spendMeter(len(f))
				}
				e.rtx.resent(seq, now.Add(to.RepairDelay(now)).UnixNano(), linkBit(to.ID))
				n++
			} else {
				e.pool.Put(buf)
			}
		}
		e.Reinjected.Add(uint64(n))
	}
}

// repairLinks picks up to two links for a repair. A copy was lost on the
// links that carried it, so the repair prefers a different link. Metered
// links carry repairs within their budget, or always once the original
// links have gone quiet (a dying primary must be repaired via the phone at
// once, not after the primary is declared dead).
func repairLinks(cands, all []*Link, carriers uint64, tries uint8, wire int, nowN int64, now time.Time) []*Link {
	// Asked again: the earlier repair was lost too, so its link is suspect
	// and a metered link may carry this one whatever its budget.
	origAlive := false
	for _, l := range cands {
		if tries == 0 && carriers&linkBit(l.ID) != 0 && l.IsAlternative(all, nowN) {
			origAlive = true
		}
	}
	allowed := func(l *Link) bool { return !l.Metered() || !origAlive || l.meterHas(wire) }
	// A link in a loss storm would most likely lose the repair too: use it
	// only when no calm link can carry it.
	calm := false
	for _, l := range cands {
		calm = calm || (!l.Storming(nowN) && allowed(l))
	}
	var out []*Link
	for _, pass := range []struct{ fresh, calmOnly bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		if pass.calmOnly && !calm {
			continue
		}
		for _, l := range cands {
			if len(out) == 2 || (pass.fresh && carriers&linkBit(l.ID) != 0) || !allowed(l) ||
				(pass.calmOnly && l.Storming(nowN)) || slices.Contains(out, l) {
				continue
			}
			// A second copy only helps if it can arrive promptly. Repairs go
			// ahead of queued data, so the data queue doesn't matter here.
			if len(out) == 1 && l.RepairDelay(now) >= out[0].RepairDelay(now)+100*time.Millisecond {
				continue
			}
			out = append(out, l)
		}
	}
	return out
}

// HandleNack retransmits the requested packets from history over the
// fastest links (two copies, so a repair is not lost to the same fate).
func (e *Engine) HandleNack(frame []byte) {
	ranges, err := proto.ParseNack(frame)
	if err != nil || !e.arq.Load() {
		return
	}
	e.NacksRecv.Add(1)
	now := time.Now()
	nowN := now.UnixNano()
	cands := e.fastest(4, now, true) // by expected arrival, metered included
	all := e.Links()
	if len(cands) == 0 {
		return
	}
	// The receiver sent this NACK about one one-way delay ago; anything we
	// expected to reach it after that was still in flight (queued behind a
	// paced link, typically), not lost. It will NACK again if it never comes.
	var owd time.Duration
	if ls := e.fastest(1, now, false); len(ls) > 0 {
		owd = time.Duration(ls[0].owd.Load())
	}
	seenBy := now.Add(-owd).UnixNano()
	budget := 512 // bound work per NACK
	for _, r := range ranges {
		for i := uint64(0); i < uint64(r.Count) && budget > 0; i++ {
			if e.rtx.inFlight(r.Start+i, seenBy) {
				e.RtxPremature.Add(1)
				continue
			}
			pkt, ok := e.rtx.get(r.Start + i)
			if !ok {
				continue
			}
			budget--
			carriers, tries := e.rtx.carriers(r.Start + i)
			// Every link that carried a copy (or an earlier repair) lost it.
			for _, l := range all {
				if carriers&linkBit(l.ID) != 0 {
					l.storm.loss(nowN)
				}
			}
			if tries == 0 && r.Count == 1 {
				// An isolated loss on every link that carried it sizes FEC.
				// Bursts (a buffer overflowing) are not counted: one parity
				// per group can't rebuild them, and they would inflate the
				// overhead for nothing.
				for _, l := range all {
					if carriers&linkBit(l.ID) != 0 {
						l.lossLost.Add(1)
					}
				}
			}
			links := repairLinks(cands, all, carriers, tries, len(pkt)+proto.DataHeaderLen, nowN, now)
			if len(links) == 0 {
				continue
			}
			var mask uint64
			for _, l := range links {
				mask |= linkBit(l.ID)
			}
			e.rtx.resent(r.Start+i, now.Add(links[0].RepairDelay(now)).UnixNano(), mask)
			for _, l := range links {
				if l.Metered() {
					l.spendMeter(len(pkt) + proto.DataHeaderLen)
				}
				buf := e.pool.Get().([]byte)
				f := proto.AppendData(buf[:0], e.Session, l.ID, r.Start+i, pkt)
				if l.enqueueRepair(f) {
					l.Tx.Add(len(f))
				} else {
					e.pool.Put(buf)
				}
			}
			e.Retransmits.Add(1)
		}
	}
}
