package bond

import (
	"sort"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
)

// In-tunnel retransmission (ARQ).
//
// Inner TCP connections run end to end through the bond. When a packet is lost
// on every path, TCP sees the loss after a long RTT, halves its window, and on
// long, jittery links such as Starlink it never recovers its speed. Instead,
// the receiver notices the hole in the bond's sequence numbers almost
// immediately and NACKs it. The sender re-sends from a short history and the
// reorder buffer holds the following packets until the repair arrives, so the
// inner TCP never sees the loss.

const rtxSlots = 4096

type rtxEntry struct {
	seq uint64
	pkt []byte
	// arrive is when the earliest copy should reach the receiver (unix
	// nanos): send time + the link's queue drain + its one-way delay. A NACK
	// for a packet that can't have arrived yet is premature, not a loss.
	arrive int64
	links  uint64 // bitmask (id % 64) of the links that carried a copy
	tries  uint8  // repairs sent so far
}

// rtxBuffer remembers recently sent packets by sequence number.
type rtxBuffer struct {
	mu    sync.Mutex
	slots []rtxEntry
}

func (b *rtxBuffer) put(seq uint64, pkt []byte, arrive int64, links uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots == nil {
		b.slots = make([]rtxEntry, rtxSlots)
	}
	e := &b.slots[seq%rtxSlots]
	if cap(e.pkt) < len(pkt) {
		e.pkt = make([]byte, len(pkt), max(len(pkt), 1500))
	}
	e.pkt = e.pkt[:len(pkt)]
	copy(e.pkt, pkt)
	e.seq, e.arrive, e.links, e.tries = seq, arrive, links, 0
}

// carriers returns the bitmask of links that carried seq's copies and how
// many repairs were already sent.
func (b *rtxBuffer) carriers(seq uint64) (uint64, uint8) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots == nil {
		return 0, 0
	}
	if e := &b.slots[seq%rtxSlots]; e.seq == seq {
		return e.links, e.tries
	}
	return 0, 0
}

func linkBit(id uint8) uint64 { return 1 << (id % 64) }

// onlyOn returns the sequence numbers whose copies all went on the links in
// mask and that were expected to arrive at or after since (unix nanos),
// oldest first.
func (b *rtxBuffer) onlyOn(mask uint64, since int64) []uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []uint64
	for i := range b.slots {
		e := &b.slots[i]
		if e.pkt != nil && e.links != 0 && e.links&^mask == 0 && e.arrive >= since {
			out = append(out, e.seq)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// inFlight reports whether seq's earliest copy is still expected after t.
func (b *rtxBuffer) inFlight(seq uint64, t int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots == nil {
		return false
	}
	e := &b.slots[seq%rtxSlots]
	return e.seq == seq && e.arrive > t
}

// resent records a repair's expected arrival.
func (b *rtxBuffer) resent(seq uint64, arrive int64, links uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots != nil {
		if e := &b.slots[seq%rtxSlots]; e.seq == seq {
			e.arrive = arrive
			e.links |= links
			e.tries++
		}
	}
}

// get returns a copy of packet seq if it is still in the history.
func (b *rtxBuffer) get(seq uint64) ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.slots == nil {
		return nil, false
	}
	e := &b.slots[seq%rtxSlots]
	if e.seq != seq || e.pkt == nil {
		return nil, false
	}
	return append([]byte(nil), e.pkt...), true
}

type gap struct {
	detected time.Time
	lastNack time.Time
	tries    int
}

// gapTracker finds sequence numbers that should have arrived but haven't.
type gapTracker struct {
	mu      sync.Mutex
	maxSeen uint64
	pending map[uint64]*gap
	// Stats
	Detected, Recovered, GaveUp uint64
}

const (
	maxPendingGaps = 4096
	maxGapJump     = 2048 // a larger jump is a restart, not loss
	maxNackTries   = 3
	gapExpiry      = 3 * time.Second
)

// seen records a fresh sequence number and notes any hole it reveals.
func (g *gapTracker) seen(seq uint64, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil {
		g.pending = map[uint64]*gap{}
	}
	if gp, ok := g.pending[seq]; ok {
		if gp.tries > 0 {
			g.Recovered++
		}
		delete(g.pending, seq)
	}
	if g.maxSeen == 0 {
		g.maxSeen = seq
		return
	}
	if seq <= g.maxSeen {
		return
	}
	if seq-g.maxSeen <= maxGapJump {
		for s := g.maxSeen + 1; s < seq && len(g.pending) < maxPendingGaps; s++ {
			g.pending[s] = &gap{detected: now}
			g.Detected++
		}
	}
	g.maxSeen = seq
}

// due returns the missing ranges to NACK now. A gap is first NACKed after
// wait (to tolerate ordinary jitter/reordering) and retried every retry until
// maxNackTries; then it is abandoned (the inner TCP will cope).
func (g *gapTracker) due(now time.Time, wait, retry time.Duration) []proto.SeqRange {
	g.mu.Lock()
	defer g.mu.Unlock()
	var seqs []uint64
	for s, gp := range g.pending {
		switch {
		case now.Sub(gp.detected) > gapExpiry || gp.tries >= maxNackTries && now.Sub(gp.lastNack) > retry:
			delete(g.pending, s)
			g.GaveUp++
		case gp.tries == 0 && now.Sub(gp.detected) >= wait,
			gp.tries > 0 && gp.tries < maxNackTries && now.Sub(gp.lastNack) >= retry:
			gp.tries++
			gp.lastNack = now
			seqs = append(seqs, s)
		}
	}
	if len(seqs) == 0 {
		return nil
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	var out []proto.SeqRange
	for _, s := range seqs {
		if n := len(out); n > 0 && out[n-1].Start+uint64(out[n-1].Count) == s && out[n-1].Count < 0xffff {
			out[n-1].Count++
			continue
		}
		out = append(out, proto.SeqRange{Start: s, Count: 1})
	}
	return out
}

func (g *gapTracker) pendingCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.pending)
}

func (g *gapTracker) reset() {
	g.mu.Lock()
	g.maxSeen = 0
	g.pending = nil
	g.mu.Unlock()
}
