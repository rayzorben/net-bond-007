// Net-Bond-007 patch test (see PATCHES.md).

package tcp

import (
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/seqnum"
)

// bbrSim drives a bbrState over a path that delivers rate packets/s with
// round trip rtt: each 10 ms, the packets sent one RTT ago are acked.
type bbrSim struct {
	s     *sender
	b     *bbrState
	now   tcpip.MonotonicTime
	sent  []int // packets sent per 10 ms tick, oldest first
	rate  float64
	rtt   time.Duration
	carry float64
}

func newBBRSim(rate float64, rtt time.Duration) *bbrSim {
	s := &sender{}
	s.SndCwnd, s.Ssthresh = InitialCwnd, InitialSsthresh
	s.MaxPayloadSize = 1 // the simulation counts sequence space in packets
	s.SndUna, s.SndNxt = seqnum.Value(1), seqnum.Value(1)
	return &bbrSim{s: s, b: newBBRCC(s), rate: rate, rtt: rtt}
}

func (m *bbrSim) tick() {
	const dt = 10 * time.Millisecond
	m.now = m.now.Add(dt)
	inflight := 0
	for _, n := range m.sent {
		inflight += n
	}
	// The path carries at most rate*dt per tick; the window limits the rest.
	m.carry += m.rate * dt.Seconds()
	n := min(int(m.carry), max(0, m.s.SndCwnd-inflight))
	m.carry -= float64(n)
	m.carry = min(m.carry, m.rate*dt.Seconds())
	m.sent = append(m.sent, n)
	m.s.SndNxt = m.s.SndNxt.Add(seqnum.Size(n))
	if len(m.sent) > int(m.rtt/dt) {
		acked := m.sent[0]
		m.sent = m.sent[1:]
		m.s.SndUna = m.s.SndUna.Add(seqnum.Size(acked))
		if acked > 0 {
			m.b.Update(acked, m.rtt, m.now)
		}
	}
}

func TestBBRConvergesToBDPAndIgnoresLoss(t *testing.T) {
	const rate = 3500.0 // packets/s: ~40 Mbit/s of 1400-byte packets
	rtt := 80 * time.Millisecond
	m := newBBRSim(rate, rtt)
	for i := 0; i < 500; i++ { // 5 s
		m.tick()
	}
	bdp := rate * rtt.Seconds()
	want := int(bbrCwndGain * bdp)
	if !m.b.filled {
		t.Fatal("startup never ended")
	}
	if c := m.s.SndCwnd; c < want*8/10 || c > want*12/10 {
		t.Fatalf("cwnd %d, want ~%d (3 x BDP %.0f)", c, want, bdp)
	}
	if bw := m.b.maxBW(); bw < rate*0.9 || bw > rate*1.1 {
		t.Fatalf("measured %.0f packets/s, path carries %.0f", bw, rate)
	}
	// A loss doesn't cut the window: recovery restores cwnd from ssthresh.
	before := m.s.SndCwnd
	m.b.HandleLossDetected()
	if m.s.Ssthresh < before {
		t.Fatalf("loss lowered ssthresh %d -> %d", before, m.s.Ssthresh)
	}
	// After a timeout it restarts from one packet and is back within a few
	// round trips, not CUBIC's many seconds.
	m.b.HandleRTOExpired()
	if m.s.SndCwnd != 1 {
		t.Fatal("RTO did not restart from one packet")
	}
	for i := 0; i < 100; i++ { // 1 s
		m.tick()
	}
	if c := m.s.SndCwnd; c < want*8/10 {
		t.Fatalf("1 s after an RTO cwnd is %d, want ~%d", c, want)
	}
}

// The path slows down: the window follows the new rate once the old peak
// leaves the window.
func TestBBRFollowsRateDrop(t *testing.T) {
	m := newBBRSim(3500, 80*time.Millisecond)
	for i := 0; i < 500; i++ {
		m.tick()
	}
	m.rate = 1000
	for i := 0; i < 500; i++ {
		m.tick()
	}
	want := int(bbrCwndGain * 1000 * 0.08)
	if c := m.s.SndCwnd; c > want*13/10 {
		t.Fatalf("cwnd %d after the path slowed, want ~%d", c, want)
	}
}

// "bbr" is offered next to reno and cubic.
func TestBBRIsAvailable(t *testing.T) {
	var s sender
	if _, ok := s.initCongestionControl(ccBBR).(*bbrState); !ok {
		t.Fatal("initCongestionControl(bbr) is not BBR")
	}
}

// ACKs released in a bunch (a reorder buffer flushing) must not inflate the
// rate estimate.
func TestBBRIgnoresAckCompression(t *testing.T) {
	m := newBBRSim(3500, 80*time.Millisecond)
	for i := 0; i < 500; i++ {
		m.tick()
	}
	base := m.b.maxBW()
	// 200 ms of nothing acked, then all of it at once.
	held := 0
	for _, n := range m.sent {
		held += n
	}
	m.now = m.now.Add(200 * time.Millisecond)
	m.s.SndNxt = m.s.SndNxt.Add(seqnum.Size(700))
	m.s.SndUna = m.s.SndNxt
	m.b.Update(held+700, 80*time.Millisecond, m.now)
	m.now = m.now.Add(time.Millisecond)
	m.b.Update(1, 80*time.Millisecond, m.now)
	if bw := m.b.maxBW(); bw > base*1.5 {
		t.Fatalf("bunched ACKs inflated the rate %.0f -> %.0f packets/s", base, bw)
	}
}

// queueSim: a bottleneck serving rate packets/s with an unbounded queue in
// front, base RTT rtt. Packets wait in the queue, so a window larger than the
// BDP shows up as RTT growth: the case where cwnd once ran away.
type queueSim struct {
	s      *sender
	b      *bbrState
	now    tcpip.MonotonicTime
	rate   float64
	rtt    time.Duration
	queue  []tcpip.MonotonicTime // send time of each queued packet
	acks   []qAck                // served packets, acked at .at
	credit float64
	maxRTT time.Duration
}

type qAck struct {
	at, sent tcpip.MonotonicTime
}

func newQueueSim(rate float64, rtt time.Duration) *queueSim {
	s := &sender{MaxPayloadSize: 1}
	s.SndCwnd, s.Ssthresh = InitialCwnd, InitialSsthresh
	s.SndUna, s.SndNxt = 1, 1
	return &queueSim{s: s, b: newBBRCC(s), rate: rate, rtt: rtt}
}

func (q *queueSim) tick() {
	const dt = time.Millisecond
	q.now = q.now.Add(dt)
	inflight := int(q.s.SndUna.Size(q.s.SndNxt))
	for ; inflight < q.s.SndCwnd; inflight++ {
		q.queue = append(q.queue, q.now)
		q.s.SndNxt++
	}
	q.credit += q.rate * dt.Seconds()
	for q.credit >= 1 && len(q.queue) > 0 {
		q.acks = append(q.acks, qAck{at: q.now.Add(q.rtt), sent: q.queue[0]})
		q.queue = q.queue[1:]
		q.credit--
	}
	q.credit = min(q.credit, 1)
	n := 0
	var last qAck
	for n < len(q.acks) && q.acks[n].at.Sub(q.now) <= 0 {
		last = q.acks[n]
		n++
	}
	if n > 0 {
		q.acks = q.acks[n:]
		q.s.SndUna = q.s.SndUna.Add(seqnum.Size(n))
		r := q.now.Sub(last.sent)
		q.maxRTT = max(q.maxRTT, r)
		q.b.Update(n, r, q.now)
	}
}

func TestBBRBoundsQueueAndProbesRTT(t *testing.T) {
	q := newQueueSim(3500, 80*time.Millisecond)
	for i := 0; i < 30000; i++ { // 30 s: several ProbeRTT periods
		q.tick()
		if i == 5000 {
			q.maxRTT = 0 // judge the steady state, not startup's overshoot
		}
	}
	bdp := 3500 * 0.08
	if c := float64(q.s.SndCwnd); c > 3.5*bdp {
		t.Fatalf("cwnd ran away: %.0f packets for a BDP of %.0f", c, bdp)
	}
	// cwnd 3 x BDP queues 2 BDPs: RTT stays near 3 x base, never grows on.
	if q.maxRTT > 300*time.Millisecond {
		t.Fatalf("RTT grew to %v on an 80 ms path", q.maxRTT)
	}
	if q.b.minRTT > 90*time.Millisecond {
		t.Fatalf("min RTT drifted to %v (base 80 ms): ProbeRTT not refreshing it", q.b.minRTT)
	}
	if bw := q.b.maxBW(); bw < 3000 || bw > 4000 {
		t.Fatalf("measured %.0f packets/s, bottleneck serves 3500", bw)
	}
}
