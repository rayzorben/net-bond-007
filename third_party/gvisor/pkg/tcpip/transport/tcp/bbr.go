// Net-Bond-007 patch (see PATCHES.md): a loss-tolerant congestion control.

package tcp

import (
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/seqnum"
)

// bbrState is a simplified, window-based take on BBR for paths where most
// loss is not congestion (satellite links, and a tunnel that repairs loss
// itself but delays delivery while it does).
//
// CUBIC and Reno cut the window on every loss. On a link losing a few
// percent at random they never get near its capacity: on Starlink, plain
// CUBIC downloads ran at 3-14 Mbit/s where Cloudflare's BBR reached 73. This
// controller instead measures what the path delivers per round trip and
// keeps cwnd at bbrCwndGain x the bandwidth-delay product (windowed max
// delivery rate x windowed min RTT), whatever is lost.
//
// gVisor has no pacing, so unlike real BBR this cannot set a sending rate:
// it bounds what is in flight. The gain of 3 leaves room for round trips
// stretched by a reorder buffer holding packets for a repair; the queue it
// can build is bounded by those 3 BDPs.
type bbrState struct {
	s *sender

	minRTT   time.Duration
	minRTTAt tcpip.MonotonicTime

	// The current round trip: it ends when everything sent at its start is
	// acknowledged.
	roundActive bool
	roundEnd    seqnum.Value
	roundStart  tcpip.MonotonicTime
	// The previous round's start: the data of this round (between the two
	// ends) was sent from prevStart to roundStart.
	hasPrev   bool
	prevEnd   seqnum.Value
	prevStart tcpip.MonotonicTime

	bw    [bbrBWRounds]float64 // delivery rate per round, packets/s
	bwPos int

	// Startup ends once the delivery rate stops growing.
	filled    bool
	fullBW    float64
	fullCount int

	// ProbeRTT: every bbrMinRTTFor, hold cwnd at bbrMinCwnd for
	// bbrProbeRTTFor so the queue drains and minRTT is measured afresh.
	// Without it the min-RTT window refills from samples that include the
	// queue cwnd itself built, and cwnd ratchets up without bound.
	probing    bool
	probeDrain seqnum.Value // the probe's 200 ms start once this is acked
	probeTimed bool
	probeUntil tcpip.MonotonicTime
	probeMin   time.Duration
	savedCwnd  int
}

const (
	bbrBWRounds   = 10 // windowed max delivery rate over this many rounds
	bbrMinRTTFor  = 10 * time.Second
	bbrCwndGain   = 3.0
	bbrMinCwnd    = 4
	bbrFullGrowth = 1.25 // startup: the rate must grow this much per round ...
	bbrFullRounds = 3    // ... or startup ends after this many rounds
	// Startup also ends once RTT has doubled: a queue of a whole BDP is
	// building (without pacing, startup's doubling cwnd overshoots fast).
	bbrStartupRTTRatio = 2
	bbrProbeRTTFor     = 200 * time.Millisecond
)

func newBBRCC(s *sender) *bbrState { return &bbrState{s: s} }

func (b *bbrState) maxBW() float64 {
	var m float64
	for _, v := range b.bw {
		m = max(m, v)
	}
	return m
}

// target is the cwnd the path supports, in packets (0 = not measured yet).
func (b *bbrState) target() int {
	bw := b.maxBW()
	if bw <= 0 || b.minRTT <= 0 {
		return 0
	}
	return max(bbrMinCwnd, int(bbrCwndGain*bw*b.minRTT.Seconds()+0.5))
}

// Update implements congestionControl.Update.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) Update(packetsAcked int, rtt time.Duration, ackTime tcpip.MonotonicTime) {
	now := ackTime
	if rtt > 0 && (b.minRTT == 0 || rtt <= b.minRTT) {
		b.minRTT, b.minRTTAt = rtt, now
	}
	if b.probing {
		if rtt > 0 && (b.probeMin == 0 || rtt < b.probeMin) {
			b.probeMin = rtt
		}
		if !b.probeTimed {
			// Wait for everything in flight at the start to be acked: the
			// queue has drained only then.
			if b.s.SndUna.LessThan(b.probeDrain) {
				b.s.SndCwnd = bbrMinCwnd
				return
			}
			b.probeTimed, b.probeUntil, b.probeMin = true, now.Add(bbrProbeRTTFor), 0
		}
		if now.Sub(b.probeUntil) < 0 {
			b.s.SndCwnd = bbrMinCwnd
			return
		}
		b.probing = false
		if b.probeMin > 0 {
			b.minRTT = b.probeMin
		}
		b.minRTTAt = now
		// Come back at one BDP and grow from there: restoring the whole
		// window at once would land in the drained queue as one burst
		// (there is no pacing).
		b.s.SndCwnd = max(min(b.savedCwnd, int(b.maxBW()*b.minRTT.Seconds())), bbrMinCwnd)
		b.roundActive = false
	} else if b.minRTT > 0 && b.filled && now.Sub(b.minRTTAt) > bbrMinRTTFor {
		b.probing, b.probeTimed, b.probeDrain, b.probeMin, b.savedCwnd = true, false, b.s.SndNxt, 0, b.s.SndCwnd
		b.s.SndCwnd = bbrMinCwnd
		return
	}
	if !b.roundActive {
		b.roundActive, b.hasPrev, b.roundStart, b.roundEnd = true, false, now, b.s.SndNxt
	}
	if b.roundEnd.LessThanEq(b.s.SndUna) {
		// The data between the previous round's end and this one's was sent
		// from prevStart to roundStart and acknowledged from roundStart to
		// now. Its rate is its size over the longer of the two (as BBR
		// does): ACKs released in a bunch (a reorder buffer flushing a held
		// batch) make the ACK interval short, the send interval is not.
		if b.hasPrev {
			pkts := float64(b.prevEnd.Size(b.roundEnd)) / float64(max(b.s.MaxPayloadSize, 1))
			if d := max(now.Sub(b.roundStart), b.roundStart.Sub(b.prevStart)); d > 0 && pkts > 0 {
				b.bw[b.bwPos%bbrBWRounds] = pkts / d.Seconds()
				b.bwPos++
			}
		}
		b.hasPrev, b.prevEnd, b.prevStart = true, b.roundEnd, b.roundStart
		if !b.filled {
			if b.minRTT > 0 && rtt >= bbrStartupRTTRatio*b.minRTT {
				b.filled = true
			} else if m := b.maxBW(); m >= b.fullBW*bbrFullGrowth {
				b.fullBW, b.fullCount = m, 0
			} else if b.fullCount++; b.fullCount >= bbrFullRounds {
				b.filled = true
			}
		}
		b.roundStart, b.roundEnd = now, b.s.SndNxt
	}
	t := b.target()
	switch {
	case !b.filled || t == 0:
		b.s.SndCwnd += packetsAcked // startup: double per round trip
	case b.s.SndCwnd < t:
		b.s.SndCwnd = min(t, b.s.SndCwnd+packetsAcked)
	default:
		b.s.SndCwnd = t
	}
	b.s.SndCwnd = max(b.s.SndCwnd, bbrMinCwnd)
	b.s.Ssthresh = b.s.SndCwnd
}

// HandleLossDetected implements congestionControl.HandleLossDetected. Loss
// is not taken as congestion: recovery keeps the window.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) HandleLossDetected() {
	// Recovery runs at ssthresh (gVisor doesn't call Update meanwhile):
	// never less than what the path is measured to carry, even right
	// after an RTO dropped cwnd to one packet.
	b.s.Ssthresh = max(b.s.SndCwnd, b.target(), bbrMinCwnd)
}

// HandleRTOExpired implements congestionControl.HandleRTOExpired: resend
// from one packet, then grow straight back to the measured target.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) HandleRTOExpired() {
	b.s.Ssthresh = max(b.s.SndCwnd, b.target(), bbrMinCwnd)
	b.s.SndCwnd = 1
	b.roundActive = false
}

// PostRecovery implements congestionControl.PostRecovery.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) PostRecovery() {
	b.roundActive = false
}
