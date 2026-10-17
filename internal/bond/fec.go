package bond

import (
	"crypto/subtle"
	"encoding/binary"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
)

// Forward error correction (FEC).
//
// ARQ repairs a lost packet one round trip later, and the reorder buffer
// holds everything behind the hole until the repair lands. On a link with
// steady random loss (Starlink: a fraction of a percent at ~10k packets/s)
// holes open many times a second, so in-order delivery is held most of the
// time. The inner TCP sees its round trip stretched 2-3x and, with a window
// sized for the real RTT, runs at half speed or less. Measured in the netns
// lab on a 40 Mbit / 76 ms link with 0.2% loss: 15-19 Mbit/s bonded versus
// 36 direct, with only one loss ever reaching the inner TCP.
//
// FEC fixes that without a round trip. For every group of k packets sent on
// a link, the sender also sends their XOR. A receiver missing exactly one
// packet of the group rebuilds it as soon as the parity arrives, a few
// milliseconds later. Groups are interleaved (packet i joins group i mod
// fecInterleave) so a short burst of consecutive losses lands in different
// groups. Anything FEC cannot rebuild still falls back to ARQ.
//
// FEC is adaptive: a link gets it only while it actually loses packets
// (repairs plus FEC rebuilds, the latter reported by the receiver in its
// probes, so FEC hiding the loss doesn't switch it off), and the group is
// sized from that rate: about 3% overhead at 0.2% loss, 12.5% at 1%+.

const (
	fecInterleave = 2
	// fecMaxAge flushes a partly filled group, so a pause in traffic doesn't
	// leave its packets unprotected.
	fecMaxAge = 8 * time.Millisecond
	// fecStoreSlots: recently received payloads kept for rebuilding. It must
	// cover every packet in flight across a group's lifetime on all links.
	fecStoreSlots = 4096
	// fecStoreFor: how long after the last FEC frame received payloads are
	// still kept (storing costs a copy per packet, so only while useful).
	// An engine with FEC negotiated (SetFEC) always keeps them, so the first
	// group the peer protects can be rebuilt too.
	fecStoreFor = 10 * time.Second

	// Parity frames that overtook their group wait this long, at most
	// fecPendingMax of them.
	fecPendingFor = 100 * time.Millisecond
	fecPendingMax = 64

	fecOnLoss   = 0.0005 // loss rate that turns FEC on for a link
	fecOffLoss  = 0.0002 // and below which it turns off again ...
	fecOffAfter = 30 * time.Second
	// fecMinPackets: a second with fewer packets says too little about loss.
	fecMinPackets = 200
)

// fecGroupFor sizes a group for a loss rate: about a 2.5% chance that a group
// loses two packets (which FEC can't rebuild) at most.
func fecGroupFor(loss float64) int {
	if loss <= 0 {
		return 0
	}
	return max(8, min(32, int(0.08/loss)))
}

type fecGroup struct {
	seqs     []uint64
	lo, hi   uint64
	parity   []byte // XOR of the payloads so far; len is the longest payload
	lenXor   uint16
	firstAdd time.Time
}

func (g *fecGroup) reset() {
	g.seqs, g.parity, g.lenXor = g.seqs[:0], g.parity[:0], 0
}

// fecEncoder builds the parity frames for one link.
type fecEncoder struct {
	mu   sync.Mutex
	k    int // group size; < 2 = off
	g    [fecInterleave]fecGroup
	next int
}

func (f *fecEncoder) K() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.k
}

func (f *fecEncoder) setK(k int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k < 2 {
		k = 0
		for i := range f.g {
			f.g[i].reset()
		}
	}
	f.k = k
}

// add folds a packet sent on the link into its group and emits the parity
// once the group is full.
func (f *fecEncoder) add(seq uint64, pkt []byte, now time.Time, emit func(seqs []uint64, lenXor uint16, parity []byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.k < 2 || len(pkt) > 0xffff {
		return
	}
	g := &f.g[f.next]
	f.next = (f.next + 1) % fecInterleave
	// Offsets travel as uint16 from the lowest seq (concurrent senders can
	// add slightly out of order).
	if len(g.seqs) > 0 && (max(g.hi, seq)-min(g.lo, seq) > 0xffff) {
		f.flush(g, emit)
	}
	if len(g.seqs) == 0 {
		g.lo, g.hi, g.firstAdd = seq, seq, now
	}
	g.lo, g.hi = min(g.lo, seq), max(g.hi, seq)
	if n := len(pkt); n > len(g.parity) {
		old := len(g.parity)
		if cap(g.parity) < n {
			p := make([]byte, n, max(n, 1500))
			copy(p, g.parity)
			g.parity = p
		} else {
			g.parity = g.parity[:n]
			clear(g.parity[old:])
		}
	}
	subtle.XORBytes(g.parity, g.parity[:len(pkt)], pkt)
	g.lenXor ^= uint16(len(pkt))
	g.seqs = append(g.seqs, seq)
	if len(g.seqs) >= f.k || len(g.seqs) >= proto.MaxFECGroup {
		f.flush(g, emit)
	}
}

// flushOld emits groups that have waited too long to fill.
func (f *fecEncoder) flushOld(now time.Time, emit func(seqs []uint64, lenXor uint16, parity []byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.g {
		if g := &f.g[i]; len(g.seqs) > 0 && now.Sub(g.firstAdd) >= fecMaxAge {
			f.flush(g, emit)
		}
	}
}

func (f *fecEncoder) flush(g *fecGroup, emit func(seqs []uint64, lenXor uint16, parity []byte)) {
	// A single packet's "parity" would be a plain duplicate: ARQ covers it.
	if len(g.seqs) >= 2 {
		sortSeqs(g.seqs)
		emit(g.seqs, g.lenXor, g.parity)
	}
	g.reset()
}

// sortSeqs is an insertion sort: groups are small and nearly sorted.
func sortSeqs(s []uint64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// emitFEC queues a parity frame on the link (bound once as l.fecOut).
func (l *Link) emitFEC(seqs []uint64, lenXor uint16, parity []byte) {
	buf := l.pool.Get().([]byte)
	f := proto.AppendFEC(buf[:0], l.session, l.ID, seqs, lenXor, parity)
	if l.enqueue(f) {
		l.Tx.Add(len(f))
		l.FECSent.Add(1)
	} else {
		l.Drops.Add(1)
		l.pool.Put(buf)
	}
}

// fecSlot is one remembered payload.
type fecSlot struct {
	seq uint64
	at  int64 // arrival, unix nanos
	buf []byte
}

// fecStore keeps recently received payloads so a missing group member can be
// rebuilt from the parity and the others.
type fecStore struct {
	mu      sync.Mutex
	until   int64 // keep storing until then (unix nanos); refreshed by FEC frames
	slots   []fecSlot
	seqs    []uint64 // scratch for parsing
	span    time.Duration
	lastFEC int64
	pending []fecPending
}

// fecPending is a parity frame that arrived ahead of its group.
type fecPending struct {
	l     *Link
	at    int64
	frame []byte
}

func (s *fecStore) remember(seq uint64, payload []byte, nowN int64, expect bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !expect && nowN > s.until {
		return
	}
	if s.slots == nil {
		s.slots = make([]fecSlot, fecStoreSlots)
	}
	sl := &s.slots[seq%fecStoreSlots]
	sl.seq, sl.at = seq, nowN
	sl.buf = append(sl.buf[:0], payload...)
}

// lookup returns the remembered payload for seq (valid under s.mu).
func (s *fecStore) lookup(seq uint64) *fecSlot {
	if s.slots == nil {
		return nil
	}
	if sl := &s.slots[seq%fecStoreSlots]; sl.seq == seq && sl.buf != nil {
		return sl
	}
	return nil
}

// Span is how long a FEC group typically takes to complete at the receiver
// (first member to parity), or 0 if no FEC arrived lately. Gaps should not be
// NACKed before FEC had its chance.
func (s *fecStore) Span(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.UnixNano()-s.lastFEC > int64(2*time.Second) {
		return 0
	}
	return s.span
}

// ReceiveFEC processes a FEC frame that arrived on link l: if exactly one
// packet of its group is missing, it is rebuilt and accepted as if it had
// arrived. A parity that overtook several of its group (paths reorder) is
// kept briefly and tried again as they come in.
func (e *Engine) ReceiveFEC(l *Link, frame []byte) {
	l.Rx.Add(len(frame))
	now := time.Now()
	s := &e.fecRx
	s.mu.Lock()
	s.until, s.lastFEC = now.UnixNano()+int64(fecStoreFor), now.UnixNano()
	if s.slots == nil {
		s.slots = make([]fecSlot, fecStoreSlots)
	}
	s.mu.Unlock()
	if e.tryFEC(l, frame, now, true) {
		s.mu.Lock()
		if len(s.pending) >= fecPendingMax {
			s.pending = s.pending[1:]
		}
		s.pending = append(s.pending, fecPending{l: l, at: now.UnixNano(), frame: append([]byte(nil), frame...)})
		s.mu.Unlock()
	}
}

// retryFEC re-tries parity frames that arrived ahead of their group.
func (e *Engine) retryFEC(now time.Time) {
	s := &e.fecRx
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	list := s.pending
	s.pending = nil
	s.mu.Unlock()
	var keep []fecPending
	for _, p := range list {
		if now.UnixNano()-p.at < int64(fecPendingFor) && e.tryFEC(p.l, p.frame, now, false) {
			keep = append(keep, p)
		}
	}
	if len(keep) > 0 {
		s.mu.Lock()
		s.pending = append(keep, s.pending...)
		if n := len(s.pending); n > fecPendingMax {
			s.pending = s.pending[n-fecPendingMax:]
		}
		s.mu.Unlock()
	}
}

// tryFEC rebuilds the group's missing packet if it can. It reports whether
// the frame is worth trying again later.
func (e *Engine) tryFEC(l *Link, frame []byte, now time.Time, fresh bool) (retry bool) {
	nowN := now.UnixNano()
	s := &e.fecRx
	s.mu.Lock()
	fec, err := proto.ParseFEC(frame, s.seqs)
	if err != nil {
		s.mu.Unlock()
		return false
	}
	s.seqs = fec.Seqs
	missing, nMissing, unstored := uint64(0), 0, false
	first := nowN
	for _, seq := range fec.Seqs {
		if sl := s.lookup(seq); sl != nil {
			first = min(first, sl.at)
			continue
		}
		if e.win.Seen(seq) {
			unstored = true // arrived, but its payload is gone: can't XOR it out
			continue
		}
		missing = seq
		nMissing++
	}
	if fresh && first < nowN {
		// Smoothed time from a group's first member to its parity.
		sp := time.Duration(nowN - first)
		if s.span == 0 {
			s.span = sp
		} else {
			s.span += (sp - s.span) / 8
		}
	}
	if unstored || nMissing != 1 {
		s.mu.Unlock()
		// Retry while members are still missing, or when one was accepted
		// but its payload isn't stored yet (another link's reader is
		// between de-duplication and storing it).
		return nMissing > 1 || unstored && nMissing == 1
	}
	out := make([]byte, len(fec.Parity))
	copy(out, fec.Parity)
	n := fec.LenXor
	for _, seq := range fec.Seqs {
		if seq == missing {
			continue
		}
		sl := s.lookup(seq)
		if len(sl.buf) > len(out) {
			s.mu.Unlock()
			return false // inconsistent group: not built from these packets
		}
		subtle.XORBytes(out, out[:len(sl.buf)], sl.buf)
		n ^= uint16(len(sl.buf))
	}
	s.mu.Unlock()
	if int(n) > len(out) || !plausibleIP(out[:n]) {
		e.FECBad.Add(1)
		return false
	}
	if e.accept(l, missing, out[:n], now) {
		l.FECRecovered.Add(1)
		e.FECRecovered.Add(1)
	}
	return false
}

// plausibleIP checks that a rebuilt packet is a whole IPv4/IPv6 packet, so a
// mismatched group can never inject garbage.
func plausibleIP(p []byte) bool {
	switch {
	case len(p) >= 20 && p[0]>>4 == 4:
		return int(binary.BigEndian.Uint16(p[2:4])) == len(p)
	case len(p) >= 40 && p[0]>>4 == 6:
		return 40+int(binary.BigEndian.Uint16(p[4:6])) == len(p)
	}
	return false
}

// SetFEC turns adaptive FEC on or off for what this engine sends. Only enable
// it when the peer understands FEC frames.
func (e *Engine) SetFEC(on bool) {
	e.fecOn.Store(on)
	if !on {
		for _, l := range e.Links() {
			l.fec.setK(0)
		}
	}
}

func (e *Engine) FEC() bool { return e.fecOn.Load() }

// updateFEC sizes a link's FEC from the loss it showed in the last interval.
// Call once per Tick.
func (e *Engine) updateFEC(l *Link, now time.Time) {
	tx := l.Tx.Packets.Load()
	lost := l.lossLost.Load() + l.peerFEC.Load()
	l.healthMu.Lock()
	dtx := tx - l.fecLastTx
	dl := uint64(0)
	if lost >= l.fecLastLost {
		dl = lost - l.fecLastLost // the peer's counter restarts with its session
	}
	l.fecLastTx, l.fecLastLost = tx, lost
	if dtx < fecMinPackets {
		l.healthMu.Unlock()
		return // idle: keep what was learned
	}
	rate := float64(dl) / float64(dtx)
	if l.fecLoss == 0 {
		l.fecLoss = rate
	} else {
		l.fecLoss += (rate - l.fecLoss) * 0.3
	}
	loss := l.fecLoss
	k := l.fec.K()
	newK := k
	switch {
	case loss >= fecOnLoss:
		l.fecQuiet = time.Time{}
		newK = fecGroupFor(loss)
	case k > 0 && loss < fecOffLoss:
		if l.fecQuiet.IsZero() {
			l.fecQuiet = now
		} else if now.Sub(l.fecQuiet) >= fecOffAfter {
			newK = 0
		}
	case k > 0:
		l.fecQuiet = time.Time{}
		newK = fecGroupFor(fecOnLoss)
	}
	l.healthMu.Unlock()
	if newK != k {
		l.fec.setK(newK)
	}
}

// SetPeerFECRecovered records the peer's count of packets it rebuilt from
// this link's FEC (reported in probes).
func (l *Link) SetPeerFECRecovered(n uint32) { l.peerFEC.Store(uint64(n)) }

// FECGroup is the link's current FEC group size (0 = off).
func (l *Link) FECGroup() int { return l.fec.K() }
