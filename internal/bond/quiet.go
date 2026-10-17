package bond

import (
	"slices"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
)

// Fast outage handling.
//
// When a Starlink link drops out for 20-200 ms (around satellite handovers),
// everything sent into it during the outage is lost, but the receiver can't
// NACK those packets until traffic resumes and reveals the gap. The sender's
// own silence detection (reinjectQuiet) watches the link's upstream, which a
// download barely uses, so its grace was 600 ms. In the field, Starlink-only
// packets took ~285 ms (median) from sending to their first NACK.
//
// So the receiver watches every busy link: when one that was delivering a
// steady stream goes silent for quietAfter while another link is still heard,
// it sends QUIET on the live links, repeated while the silence lasts. The
// sender then resends at once, on its other links, everything it put only on
// the silent link since the receiver last heard it, keeps new packets off it,
// and treats it as storming so repairs avoid it.

const (
	quietAfter    = 50 * time.Millisecond // silence on a busy link this long is an outage
	quietBusyPkts = 30                    // busy: at least this many packets in the last 100 ms
	quietRepeat   = 25 * time.Millisecond // QUIET again this often while silent
	quietMaxFor   = 2 * time.Second       // past this, ordinary link-down handling takes over
	quietHold     = 3 * quietRepeat       // a QUIET keeps the link avoided this long
	quietMargin   = 10 * time.Millisecond // resend a little more than strictly missing
	rxWatchSlots  = 10                    // x 10 ms nackLoop ticks = 100 ms of history
)

// rxWatch is one link's receive-side silence watch (nackLoop only).
type rxWatch struct {
	hist     [rxWatchSlots]uint64 // Rx.Packets at each tick
	pos      int
	busy     bool
	lastSent int64
}

// watchSilence runs every nackLoop tick on the receiving side.
func (e *Engine) watchSilence(now time.Time) {
	nowN := now.UnixNano()
	links := e.Links()
	for _, l := range links {
		w := &l.rxw
		cur := l.Rx.Packets.Load()
		old := w.hist[w.pos]
		w.hist[w.pos] = cur
		w.pos = (w.pos + 1) % rxWatchSlots
		last := l.LastRx.Load()
		if last == 0 {
			continue
		}
		silent := time.Duration(nowN - last)
		if silent < quietAfter {
			// Still heard: remember whether it was busy, for when it stops.
			w.busy = old != 0 && cur-old >= quietBusyPkts
			w.lastSent = 0
			continue
		}
		if !w.busy || silent > quietMaxFor || nowN-w.lastSent < int64(quietRepeat) {
			continue
		}
		var via []*Link
		for _, o := range links {
			if o != l && o.State().Usable() && nowN-o.LastRx.Load() < int64(quietAfter) {
				via = append(via, o)
			}
		}
		if len(via) == 0 {
			continue // nothing else is heard: nobody to resend over
		}
		slices.SortFunc(via, func(a, b *Link) int { return int(a.owd.Load() - b.owd.Load()) })
		ms := uint16(min(silent/time.Millisecond, 0xffff))
		for _, o := range via[:min(2, len(via))] {
			o.SendRaw(proto.AppendQuiet(nil, e.Session, o.ID, l.ID, ms))
		}
		w.lastSent = nowN
		e.QuietsSent.Add(1)
	}
}

// HandleQuiet processes a QUIET frame that arrived on link via: the peer has
// heard nothing on another link for a while.
func (e *Engine) HandleQuiet(via *Link, frame []byte) {
	via.Rx.Add(len(frame))
	id, ms, err := proto.ParseQuiet(frame)
	if err != nil || !e.arq.Load() {
		return
	}
	l := e.Link(id)
	if l == nil || l == via {
		return
	}
	e.QuietsRecv.Add(1)
	now := time.Now()
	nowN := now.UnixNano()
	l.peerQuiet.Store(nowN + int64(quietHold))
	l.storm.mark(nowN)
	// The peer last heard l about ms before it sent this, one way-delay ago.
	// Anything due to arrive since then is lost (or will be: the link is out).
	since := nowN - via.owd.Load() - int64(time.Duration(ms)*time.Millisecond) - int64(quietMargin)
	e.reinjectFrom(l, since, now)
}

// PeerQuiet reports whether the peer recently said it hears nothing on l.
func (l *Link) PeerQuiet(nowN int64) bool { return nowN < l.peerQuiet.Load() }
