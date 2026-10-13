package bond

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"sync"
	"time"
)

// Connection tracking: which flows run through the bond, how fast, and which
// links carry them. A flow is keyed by protocol plus the inside endpoint
// (client side of the tunnel) and the outside endpoint (the internet host).

type FlowKey struct {
	Proto   uint8          // 6 TCP, 17 UDP, 1/58 ICMP
	Inside  netip.AddrPort // client side
	Outside netip.AddrPort // internet side
}

type Flow struct {
	Key            FlowKey
	First, Last    time.Time
	TxBytes        uint64 // inside -> outside (upload)
	RxBytes        uint64 // outside -> inside (download)
	TxBps, RxBps   float64
	PeakTx, PeakRx float64
	lastTx, lastRx uint64
	CopiesOn       map[uint8]uint64 // packet copies sent per link
	WinsOn         map[uint8]uint64 // first-arriving copies received per link
	Name           string           // TLS SNI when seen
}

const (
	maxFlows      = 8192
	flowIdleAfter = 2 * time.Minute
)

type flowTable struct {
	mu        sync.Mutex
	m         map[FlowKey]*Flow
	lastTick  time.Time
	upstream  bool // engine sends upstream (client side) vs downstream (server)
	disabled  bool
	Untracked uint64
	// sink receives flows when they end (evicted after going idle).
	sink func([]Flow)
}

// parseFlow extracts protocol, source and destination from an IP packet.
func parseFlow(pkt []byte) (proto uint8, src, dst netip.AddrPort, l4 []byte, ok bool) {
	if len(pkt) < 1 {
		return
	}
	var s, d netip.Addr
	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < 20 {
			return
		}
		ihl := int(pkt[0]&0x0f) * 4
		if ihl < 20 || len(pkt) < ihl {
			return
		}
		proto = pkt[9]
		s, d = netip.AddrFrom4([4]byte(pkt[12:16])), netip.AddrFrom4([4]byte(pkt[16:20]))
		l4 = pkt[ihl:]
		if pkt[6]&0x1f != 0 || pkt[7] != 0 { // non-first fragment: no ports
			return proto, netip.AddrPortFrom(s, 0), netip.AddrPortFrom(d, 0), nil, true
		}
	case 6:
		if len(pkt) < 40 {
			return
		}
		proto = pkt[6]
		s, d = netip.AddrFrom16([16]byte(pkt[8:24])), netip.AddrFrom16([16]byte(pkt[24:40]))
		l4 = pkt[40:]
	default:
		return
	}
	var sp, dp uint16
	if (proto == 6 || proto == 17) && len(l4) >= 4 {
		sp, dp = binary.BigEndian.Uint16(l4), binary.BigEndian.Uint16(l4[2:])
	}
	return proto, netip.AddrPortFrom(s, sp), netip.AddrPortFrom(d, dp), l4, true
}

// note records one packet. sent: this engine is sending it (vs received).
// links: ids carrying copies (send) or the winning link (receive).
func (t *flowTable) note(pkt []byte, sent bool, links []uint8, now time.Time) {
	if t.disabled {
		return
	}
	proto, src, dst, l4, ok := parseFlow(pkt)
	if !ok {
		return
	}
	// Upstream packets travel inside -> outside.
	upstream := sent == t.upstream
	k := FlowKey{Proto: proto, Inside: src, Outside: dst}
	if !upstream {
		k.Inside, k.Outside = dst, src
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[FlowKey]*Flow{}
	}
	f := t.m[k]
	if f == nil {
		if len(t.m) >= maxFlows {
			t.Untracked++
			return
		}
		f = &Flow{Key: k, First: now, CopiesOn: map[uint8]uint64{}, WinsOn: map[uint8]uint64{}}
		t.m[k] = f
	}
	f.Last = now
	if upstream {
		f.TxBytes += uint64(len(pkt))
		if f.Name == "" && proto == 6 {
			f.Name = tlsSNI(l4)
		}
	} else {
		f.RxBytes += uint64(len(pkt))
	}
	for _, id := range links {
		if sent {
			f.CopiesOn[id]++
		} else {
			f.WinsOn[id]++
		}
	}
}

// tick computes rates and evicts idle flows.
func (t *flowTable) tick(now time.Time) {
	t.mu.Lock()
	dt := now.Sub(t.lastTick).Seconds()
	t.lastTick = now
	var ended []Flow
	for k, f := range t.m {
		if now.Sub(f.Last) > flowIdleAfter {
			delete(t.m, k)
			if t.sink != nil {
				ended = append(ended, *f)
			}
			continue
		}
		if dt > 0 && dt < 10 {
			f.TxBps = 0.5*f.TxBps + 0.5*float64(f.TxBytes-f.lastTx)*8/dt
			f.RxBps = 0.5*f.RxBps + 0.5*float64(f.RxBytes-f.lastRx)*8/dt
			f.PeakTx, f.PeakRx = max(f.PeakTx, f.TxBps), max(f.PeakRx, f.RxBps)
		}
		f.lastTx, f.lastRx = f.TxBytes, f.RxBytes
	}
	sink := t.sink
	t.mu.Unlock()
	if len(ended) > 0 {
		sink(ended)
	}
}

// drain ends every flow (session closing).
func (t *flowTable) drain() {
	t.mu.Lock()
	var ended []Flow
	for k, f := range t.m {
		delete(t.m, k)
		ended = append(ended, *f)
	}
	sink := t.sink
	t.mu.Unlock()
	if sink != nil && len(ended) > 0 {
		sink(ended)
	}
}

// snapshot returns copies of the flows, busiest first.
func (t *flowTable) snapshot() []Flow {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Flow, 0, len(t.m))
	for _, f := range t.m {
		c := *f
		c.CopiesOn = clone(f.CopiesOn)
		c.WinsOn = clone(f.WinsOn)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].RxBps+out[i].TxBps, out[j].RxBps+out[j].TxBps
		if ri != rj {
			return ri > rj
		}
		return out[i].RxBytes+out[i].TxBytes > out[j].RxBytes+out[j].TxBytes
	})
	return out
}

func clone(m map[uint8]uint64) map[uint8]uint64 {
	c := make(map[uint8]uint64, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// tlsSNI extracts the server name from a TLS ClientHello at the start of a
// TCP payload, or "" if this segment isn't one.
func tlsSNI(tcp []byte) string {
	if len(tcp) < 20 {
		return ""
	}
	off := int(tcp[12]>>4) * 4
	if off < 20 || len(tcp) < off+5 {
		return ""
	}
	b := tcp[off:]
	// TLS record: handshake(22), version 3.x, then ClientHello(1).
	if len(b) < 9 || b[0] != 22 || b[1] != 3 || b[5] != 1 {
		return ""
	}
	p := 9 + 2 + 32 // record hdr(5) + hs hdr(4) + version(2) + random(32)
	if len(b) < p+1 {
		return ""
	}
	p += 1 + int(b[p]) // session id
	if len(b) < p+2 {
		return ""
	}
	p += 2 + int(binary.BigEndian.Uint16(b[p:])) // cipher suites
	if len(b) < p+1 {
		return ""
	}
	p += 1 + int(b[p]) // compression methods
	if len(b) < p+2 {
		return ""
	}
	end := p + 2 + int(binary.BigEndian.Uint16(b[p:]))
	p += 2
	for p+4 <= len(b) && p+4 <= end {
		typ, n := binary.BigEndian.Uint16(b[p:]), int(binary.BigEndian.Uint16(b[p+2:]))
		p += 4
		if p+n > len(b) {
			return ""
		}
		if typ == 0 && n >= 5 { // server_name
			l := int(binary.BigEndian.Uint16(b[p+3:]))
			if b[p+2] == 0 && p+5+l <= len(b) {
				return string(b[p+5 : p+5+l])
			}
			return ""
		}
		p += n
	}
	return ""
}
