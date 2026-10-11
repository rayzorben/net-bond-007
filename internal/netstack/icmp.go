package netstack

import (
	"bufio"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// handleICMP answers IPv4 echo requests. Pings to the gateway are answered
// locally; pings to internet hosts are forwarded with an unprivileged ICMP
// socket (Linux "ping sockets", enabled by default in Docker). Returns true if
// the packet was consumed.
func (g *Gateway) handleICMP(pkt []byte) bool {
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+8 || pkt[ihl] != 8 { // not an echo request
		return false
	}
	src := netip.AddrFrom4([4]byte(pkt[12:16]))
	dst := netip.AddrFrom4([4]byte(pkt[16:20]))
	id := binary.BigEndian.Uint16(pkt[ihl+4:])
	seq := binary.BigEndian.Uint16(pkt[ihl+6:])
	data := append([]byte(nil), pkt[ihl+8:]...)
	g.Stats.Pings.Add(1)

	if dst == g.gw {
		g.out(src, EchoReply(dst, src, id, seq, data, 64))
		return true
	}
	if !g.Allowed(dst) {
		g.Stats.Blocked.Add(1)
		return true
	}
	go func() {
		// Prefer an unprivileged ping socket; fall back to a raw socket
		// (needs CAP_NET_RAW, which Docker grants by default).
		raw := false
		c, err := icmp.ListenPacket("udp4", "0.0.0.0")
		if err != nil {
			c, err = icmp.ListenPacket("ip4:icmp", "0.0.0.0")
			raw = true
		}
		if err != nil {
			g.log.Debug("no ICMP socket available", "err", err)
			return
		}
		defer c.Close()
		msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Body: &icmp.Echo{ID: int(id), Seq: int(seq), Data: data}}
		b, _ := msg.Marshal(nil)
		var to net.Addr = &net.UDPAddr{IP: dst.AsSlice()}
		if raw {
			to = &net.IPAddr{IP: dst.AsSlice()}
		}
		if _, err := c.WriteTo(b, to); err != nil {
			return
		}
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		rb := make([]byte, 1500)
		for {
			n, from, err := c.ReadFrom(rb)
			if err != nil {
				return
			}
			m, err := icmp.ParseMessage(1, rb[:n])
			if err != nil || m.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
			e, ok := m.Body.(*icmp.Echo)
			// Raw sockets see every reply on the host: match peer, id and seq.
			if !ok || e.Seq != int(seq) || (raw && (e.ID != int(id) || addrOf(from) != dst)) {
				continue
			}
			g.out(src, EchoReply(dst, src, id, seq, e.Data, 57))
			return
		}
	}()
	return true
}

func addrOf(a net.Addr) netip.Addr {
	switch v := a.(type) {
	case *net.IPAddr:
		ip, _ := netip.AddrFromSlice(v.IP)
		return ip.Unmap()
	case *net.UDPAddr:
		ip, _ := netip.AddrFromSlice(v.IP)
		return ip.Unmap()
	}
	return netip.Addr{}
}

// EchoReply builds an IPv4 ICMP echo reply packet.
func EchoReply(from, to netip.Addr, id, seq uint16, data []byte, ttl byte) []byte {
	p := make([]byte, 20+8+len(data))
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	p[8] = ttl
	p[9] = 1
	f4, t4 := from.As4(), to.As4()
	copy(p[12:16], f4[:])
	copy(p[16:20], t4[:])
	binary.BigEndian.PutUint16(p[10:], checksum(p[:20]))
	ic := p[20:]
	ic[0] = 0 // echo reply
	binary.BigEndian.PutUint16(ic[4:], id)
	binary.BigEndian.PutUint16(ic[6:], seq)
	copy(ic[8:], data)
	binary.BigEndian.PutUint16(ic[2:], checksum(ic))
	return p
}

func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

var (
	sysDNSOnce sync.Once
	sysDNS     string
)

// systemDNS returns the first nameserver in /etc/resolv.conf (or 1.1.1.1).
func systemDNS() string {
	sysDNSOnce.Do(func() {
		sysDNS = "1.1.1.1:53"
		f, err := os.Open("/etc/resolv.conf")
		if err != nil {
			return
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fs := strings.Fields(sc.Text())
			if len(fs) >= 2 && fs[0] == "nameserver" {
				if a, err := netip.ParseAddr(fs[1]); err == nil && !a.IsLoopback() {
					sysDNS = netip.AddrPortFrom(a, 53).String()
					return
				}
			}
		}
	})
	return sysDNS
}
