package bond

import (
	"sync/atomic"
	"time"

	"github.com/rayzorben/net-bond-007/internal/dedup"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

// The call lane carries real-time media (calls, per Policy.Realtime) outside
// the ordered stream.
//
// Why: every other packet shares one sequence space, and the receiver
// releases it in order. A bulk packet lost on a flaky link leaves a gap, and
// the reorder buffer holds everything behind it until the repair arrives or
// the hold (~200 ms) gives up, including call audio that had already arrived
// safely on another link. Measured in the field (2026-10-20, campground Wi-Fi
// next to Starlink): 10% of packets waited and 2,424 gave up the full hold,
// and Teams calls dropped. Call media tolerates reordering (its own jitter
// buffer handles it) but not delay.
//
// So a call packet:
//   - takes a sequence number from its own space (callLaneBit set), leaving
//     no gap in the ordered stream when every copy is lost;
//   - goes on every link that is not disabled, down ones included: a copy
//     over a flaky link costs little and covers the others' dropouts;
//   - jumps each link's send queue (the priority lane) and is never dropped
//     for queue depth;
//   - is delivered the moment its first copy arrives: no reorder hold, no
//     ARQ, no FEC (the copies are the redundancy).
//
// A peer that doesn't announce the lane (HelloAck.CallLane, or
// Policy.CallLane for the server) gets calls in the ordered stream as before.

// callLaneBit marks a DATA sequence number as belonging to the call lane.
const callLaneBit = uint64(1) << 63

type callLane struct {
	on  atomic.Bool // the peer understands call-lane frames
	seq atomic.Uint64
	win dedup.Window

	Tx, Rx atomic.Uint64 // call packets sent and delivered (unique)
}

// SetCallLane turns the call lane on for sending; set it only when the peer
// understands it. Receiving always works.
func (e *Engine) SetCallLane(on bool) { e.calls.on.Store(on) }
func (e *Engine) CallLane() bool      { return e.calls.on.Load() }

// sendCall mirrors one call packet on every link that can carry it, ahead
// of queued data. Returns the number of copies queued.
func (e *Engine) sendCall(pkt []byte, links []*Link, othersAlive bool, now time.Time) int {
	wire := len(pkt) + proto.DataHeaderLen
	seq := callLaneBit | e.calls.seq.Add(1)
	var ibuf [16]uint8
	ids := ibuf[:0]
	sent := 0
	for _, l := range links {
		if s := l.State(); s == StateConnecting || s == StateDisabled {
			continue
		}
		// A metered link keeps to its share unless it's the only real path.
		if l.Metered() && othersAlive && !l.meterHas(wire) {
			continue
		}
		buf := e.pool.Get().([]byte)
		f := proto.AppendData(buf[:0], e.Session, l.ID, seq, pkt)
		if !l.enqueueCall(f) {
			l.Drops.Add(1)
			e.pool.Put(buf)
			continue
		}
		l.Tx.Add(len(f))
		l.Pacer.Force(wire, now)
		if l.Metered() {
			l.spendMeter(wire)
		}
		ids = append(ids, l.ID)
		sent++
	}
	if sent == 0 {
		e.NoLink.Add(1)
		return 0
	}
	e.calls.Tx.Add(1)
	e.UniqueTx.Add(len(pkt))
	e.flows.note(pkt, true, ids, now)
	return sent
}

// acceptCall delivers the first copy of a call packet at once.
func (e *Engine) acceptCall(l *Link, seq uint64, payload []byte, now time.Time) {
	if e.calls.win.Check(seq&^callLaneBit) != dedup.Fresh {
		l.Dups.Add(1)
		return
	}
	l.Wins.Add(1)
	e.calls.Rx.Add(1)
	e.UniqueRx.Add(len(payload))
	winner := [1]uint8{l.ID}
	e.flows.note(payload, false, winner[:], now)
	// Deliveries are serialized under rmu, like the ordered path's.
	e.rmu.Lock()
	e.deliver(payload)
	e.rmu.Unlock()
}

// enqueueCall queues a call frame on the priority lane (whether or not
// repairs use it), falling back to the data queue when the lane is full.
func (l *Link) enqueueCall(f []byte) bool {
	select {
	case l.prio <- f:
		return true
	default:
		return l.enqueue(f)
	}
}
