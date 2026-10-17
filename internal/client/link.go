package client

import (
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/metrics"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

// clink is one interface's path to the server.
type clink struct {
	c     *Client
	e     *bond.Engine
	iface platform.Iface
	// relayTo is the phone this link goes through (phone relay links only);
	// the socket talks to the phone instead of the server.
	relayTo netip.AddrPort
	id      uint8
	conn    *net.UDPConn
	link    *bond.Link

	helloAcked atomic.Bool
	rejected   atomic.Pointer[string]
	done       chan struct{}
	once       sync.Once

	mu         sync.Mutex
	probes     metrics.ProbeTracker
	probeSeq   uint32
	everAcked  bool
	opened     time.Time
	upRate     metrics.Rate // goodput the server confirms receiving on this link
	upBytes    uint64
	downWeight atomic.Uint32
	downCap    atomic.Uint32
	lastRxB    uint64
	lastRxAt   time.Time

	testing     atomic.Bool      // a speed test is loading this link on purpose
	meterSeen   uint64           // wire bytes already counted towards usage
	rel         bond.Reliability // long-run delivery score (automatic scaling)
	lastAckSeen time.Time
}

func (c *Client) openLink(e *bond.Engine, it platform.Iface, id uint8, relayTo netip.AddrPort) (*clink, error) {
	var conn *net.UDPConn
	var err error
	if relayTo.IsValid() {
		conn, err = c.DialRelay(relayTo)
	} else {
		conn, err = c.Dial(it, c.server)
	}
	if err != nil {
		return nil, err
	}
	l := &clink{c: c, e: e, iface: it, relayTo: relayTo, id: id, conn: conn, done: make(chan struct{}), opened: time.Now()}
	l.link = e.AddLink(id, it.Name, it.Kind, func(f []byte) error {
		_, err := conn.Write(f)
		return err
	})
	l.link.SetState(bond.StateConnecting)
	l.link.Health(func(h *bond.Health) {
		h.LocalAddr = it.PrimaryIPv4().String()
		h.RemoteAddr = c.server.String()
	})
	go l.reader()
	go l.prober()
	return l, nil
}

func (l *clink) close(bye bool) {
	l.once.Do(func() {
		if bye && l.helloAcked.Load() {
			f := make([]byte, proto.HeaderLen)
			proto.PutHeader(f, proto.Header{Type: proto.TypeBye, Session: l.e.Session, Link: l.id})
			l.conn.Write(f)
		}
		close(l.done)
		l.conn.Close()
		l.e.RemoveLink(l.id)
	})
}

func (l *clink) status() string {
	if r := l.rejected.Load(); r != nil {
		return "server rejected: " + *r
	}
	if !l.helloAcked.Load() {
		return "waiting for server reply"
	}
	return ""
}

func (l *clink) reader() {
	buf := make([]byte, proto.MaxFrame)
	for {
		n, err := l.conn.Read(buf)
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			// ICMP port unreachable etc. surface here; keep listening.
			l.link.SetError(err.Error())
			time.Sleep(100 * time.Millisecond)
			continue
		}
		f := buf[:n]
		h, err := proto.ParseHeader(f)
		if err != nil || h.Session != l.e.Session {
			continue
		}
		now := time.Now()
		l.link.LastRx.Store(now.UnixNano())
		switch h.Type {
		case proto.TypeData:
			l.e.Receive(l.link, f)
		case proto.TypeNack:
			l.link.Rx.Add(n)
			l.e.HandleNack(f)
		case proto.TypeFEC:
			l.e.ReceiveFEC(l.link, f)
		case proto.TypeProbeAck:
			a, err := proto.ParseProbeAck(f)
			if err != nil {
				continue
			}
			l.link.Rx.Add(n)
			l.mu.Lock()
			rtt, ok := l.probes.Acked(a.Seq, now)
			if ok {
				l.everAcked = true
				l.upBytes = a.RxBytes
			}
			l.link.SetPeerFECRecovered(a.FECRecovered)
			l.mu.Unlock()
			if ok {
				var srtt time.Duration
				l.link.Health(func(h *bond.Health) { h.RTT.Update(rtt); srtt = h.RTT.SRTT })
				l.link.SetOneWayDelay(srtt / 2)
			}
		case proto.TypeHelloAck:
			var a proto.HelloAck
			if proto.ParseJSON(f, &a) == nil {
				l.rejected.Store(nil)
				l.helloAcked.Store(true)
				l.link.SetError("")
				l.c.onHelloAck(a)
			}
		case proto.TypeReject:
			var r proto.Reject
			proto.ParseJSON(f, &r)
			switch r.Reason {
			case "unknown-session", "unknown-link":
				// Server restarted or forgot us: say hello again.
				l.helloAcked.Store(false)
			default:
				l.rejected.Store(&r.Reason)
				l.helloAcked.Store(false)
				l.link.SetError("server rejected: " + r.Reason)
			}
		}
	}
}

func (l *clink) prober() {
	var lastHello time.Time
	for {
		cfg := l.c.cfg.Get()
		iv := time.Duration(cfg.Health.ProbeIntervalMs) * time.Millisecond
		select {
		case <-l.done:
			return
		case <-time.After(iv):
		}
		now := time.Now()
		if !l.helloAcked.Load() {
			if now.Sub(lastHello) >= time.Second {
				l.sendHello(cfg.Server.Token)
				lastHello = now
			}
			l.evaluate(now)
			continue
		}
		l.evaluate(now)
		l.sendProbe(now)
	}
}

func (l *clink) sendHello(token string) {
	f, err := proto.AppendJSON(nil, proto.TypeHello, l.e.Session, l.id, proto.Hello{
		Token: token, ClientName: l.c.deviceName(), Version: Version, OS: osName(),
		LinkName: l.iface.Name, LinkKind: l.iface.Kind, FEC: true,
	})
	if err == nil {
		if _, err := l.conn.Write(f); err != nil {
			l.link.SetError(err.Error())
		}
	}
}

func (l *clink) sendProbe(now time.Time) {
	var p proto.Probe
	l.link.Health(func(h *bond.Health) {
		p.RTTMicros = uint32(h.RTT.SRTT / time.Microsecond)
		p.JitterMicro = uint32(h.RTT.RTTVar / time.Microsecond)
		p.LossPermil = uint16(h.Loss * 1000)
	})
	p.CapKbps = l.downCap.Load()
	l.link.Health(func(h *bond.Health) { p.LastRTTMicr = uint32(h.RTT.Last / time.Microsecond) })
	if rx := l.link.Rx.Bytes.Load(); !l.lastRxAt.IsZero() {
		if dt := now.Sub(l.lastRxAt).Seconds(); dt > 0 {
			p.RxKbps = uint32(float64(rx-l.lastRxB) * 8 / dt / 1000)
		}
		l.lastRxB, l.lastRxAt = rx, now
	} else {
		l.lastRxB, l.lastRxAt = rx, now
	}
	p.Weight = uint16(l.downWeight.Load())
	p.FECRecovered = uint32(l.link.FECRecovered.Load())
	p.State = uint8(l.link.State())
	if l.link.Backup() {
		p.State |= proto.ProbeBackupBit
	}
	l.mu.Lock()
	l.probeSeq++
	p.Seq = l.probeSeq
	p.SentAt = now.UnixNano()
	l.probes.Sent(p.Seq, now)
	l.mu.Unlock()
	f := proto.AppendProbe(nil, l.e.Session, l.id, p)
	if _, err := l.conn.Write(f); err != nil {
		l.link.SetError(err.Error())
	}
}

// evaluate recomputes loss, upstream goodput and the link state.
func (l *clink) evaluate(now time.Time) {
	cfg := l.c.cfg.Get().Health
	l.mu.Lock()
	loss := l.probes.Loss(now, time.Duration(cfg.ProbeTimeoutMs)*time.Millisecond, cfg.LossWindow)
	everAcked := l.everAcked
	last := l.probes.LastAck
	if last.IsZero() {
		last = l.opened
	}
	l.upRate.Sample(l.upBytes, now)
	delivered := l.upRate.BPS
	// Reliability sample: did this link deliver anything (a probe reply)
	// during the last interval?
	gotAck := l.probes.LastAck.After(l.lastAckSeen)
	l.lastAckSeen = l.probes.LastAck
	l.mu.Unlock()

	var srtt, recent, minRTT time.Duration
	l.link.Health(func(h *bond.Health) {
		h.Loss = loss
		srtt, minRTT = h.RTT.SRTT, h.RTT.Min
		recent = (h.RTT.SRTT + h.RTT.Last) / 2 // react faster than SRTT alone
	})
	testing := l.testing.Load()
	if !testing { // our own speed test fills the queue: not a signal
		l.link.Pacer.Govern(now, recent, minRTT, loss, delivered)
		l.link.Shaper.Observe(now, delivered)
	}
	st := bond.Classify(bond.HealthParams{
		DegradedRTT:  time.Duration(cfg.DegradedRTTMs) * time.Millisecond,
		DegradedLoss: cfg.DegradedLossPct / 100,
		DownLoss:     cfg.DownLossPct / 100,
		DownAfter:    time.Duration(cfg.DownAfterMs) * time.Millisecond,
	}, everAcked && l.helloAcked.Load(), now.Sub(last), srtt, loss)
	if prev := l.link.State(); testing && st == bond.StateDegraded && prev == bond.StateUp {
		st = prev // a saturated link during a speed test isn't degraded
	}
	if prev := l.link.State(); prev != st {
		l.c.log.Info("link state", "link", l.iface.Name, "from", prev, "to", st)
	}
	l.link.SetState(st)

	bcfg := l.c.cfg.Get().Bonding
	l.mu.Lock()
	if !testing {
		l.rel.Observe(gotAck && st != bond.StateDown, now,
			time.Duration(bcfg.ReliabilityFallSec)*time.Second, time.Duration(bcfg.ReliabilityRiseSec)*time.Second)
	}
	r := l.rel.Value()
	l.mu.Unlock()
	l.link.Health(func(h *bond.Health) { h.Reliability = r })
}
