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
	roundActive    bool
	roundEnd       seqnum.Value
	roundStart     tcpip.MonotonicTime
	roundDelivered int

	bw    [bbrBWRounds]float64 // delivery rate per round, packets/s
	bwPos int

	// Startup ends once the delivery rate stops growing.
	filled    bool
	fullBW    float64
	fullCount int
}

const (
	bbrBWRounds   = 10 // windowed max delivery rate over this many rounds
	bbrMinRTTFor  = 10 * time.Second
	bbrCwndGain   = 3.0
	bbrMinCwnd    = 4
	bbrFullGrowth = 1.25 // startup: the rate must grow this much per round ...
	bbrFullRounds = 3    // ... or startup ends after this many rounds
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
	if rtt > 0 && (b.minRTT == 0 || rtt <= b.minRTT || now.Sub(b.minRTTAt) > bbrMinRTTFor) {
		b.minRTT, b.minRTTAt = rtt, now
	}
	if !b.roundActive {
		b.roundActive, b.roundStart, b.roundEnd, b.roundDelivered = true, now, b.s.SndNxt, 0
	}
	b.roundDelivered += packetsAcked
	if b.roundEnd.LessThanEq(b.s.SndUna) {
		// A round trip can't take less than the minimum RTT. Measuring it
		// shorter means ACKs were bunched (a reorder buffer releasing a
		// held batch), which would overstate the rate several times over.
		if d := max(now.Sub(b.roundStart), b.minRTT); d > 0 && b.roundDelivered > 0 {
			b.bw[b.bwPos%bbrBWRounds] = float64(b.roundDelivered) / d.Seconds()
			b.bwPos++
		}
		if !b.filled {
			if m := b.maxBW(); m >= b.fullBW*bbrFullGrowth {
				b.fullBW, b.fullCount = m, 0
			} else if b.fullCount++; b.fullCount >= bbrFullRounds {
				b.filled = true
			}
		}
		b.roundStart, b.roundEnd, b.roundDelivered = now, b.s.SndNxt, 0
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
	b.s.Ssthresh = max(b.s.SndCwnd, bbrMinCwnd)
}

// HandleRTOExpired implements congestionControl.HandleRTOExpired: resend
// from one packet, then grow straight back to the measured target.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) HandleRTOExpired() {
	b.s.Ssthresh = max(b.s.SndCwnd, bbrMinCwnd)
	b.s.SndCwnd = 1
	b.roundActive = false
}

// PostRecovery implements congestionControl.PostRecovery.
//
// +checklocks:b.s.ep.mu
func (b *bbrState) PostRecovery() {
	b.roundActive = false
}
