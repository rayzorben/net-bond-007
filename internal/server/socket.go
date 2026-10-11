package server

import (
	"errors"
	"net"
	"net/netip"
	"strconv"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// packetConn is one UDP listening socket. When bound to a wildcard address
// it records each packet's destination address (IP_PKTINFO) so replies leave
// from the exact address the client sent to. Without this, a multihomed
// server answers from whichever address the routing table prefers, and the
// client's connected socket silently drops the reply.
type packetConn struct {
	c  *net.UDPConn
	p4 *ipv4.PacketConn
	p6 *ipv6.PacketConn
}

// path is how to reach one client link: socket, peer and our source address.
type path struct {
	pc     *packetConn
	remote netip.AddrPort
	raddr  *net.UDPAddr
	local  netip.Addr
}

func newPath(pc *packetConn, remote netip.AddrPort, local netip.Addr) *path {
	return &path{pc: pc, remote: remote, raddr: net.UDPAddrFromAddrPort(remote), local: local}
}

func (p *packetConn) read(buf []byte) (int, netip.AddrPort, netip.Addr, error) {
	switch {
	case p.p4 != nil:
		n, cm, src, err := p.p4.ReadFrom(buf)
		if err != nil {
			return 0, netip.AddrPort{}, netip.Addr{}, err
		}
		var local netip.Addr
		if cm != nil {
			local, _ = netip.AddrFromSlice(cm.Dst)
			local = local.Unmap()
		}
		return n, src.(*net.UDPAddr).AddrPort(), local, nil
	case p.p6 != nil:
		n, cm, src, err := p.p6.ReadFrom(buf)
		if err != nil {
			return 0, netip.AddrPort{}, netip.Addr{}, err
		}
		var local netip.Addr
		if cm != nil {
			local, _ = netip.AddrFromSlice(cm.Dst)
		}
		return n, src.(*net.UDPAddr).AddrPort(), local, nil
	}
	n, from, err := p.c.ReadFromUDPAddrPort(buf)
	return n, from, netip.Addr{}, err
}

func (p *packetConn) write(b []byte, pa *path) error {
	switch {
	case p.p4 != nil && pa.local.IsValid():
		_, err := p.p4.WriteTo(b, &ipv4.ControlMessage{Src: pa.local.AsSlice()}, pa.raddr)
		return err
	case p.p6 != nil && pa.local.IsValid():
		_, err := p.p6.WriteTo(b, &ipv6.ControlMessage{Src: pa.local.AsSlice()}, pa.raddr)
		return err
	}
	_, err := p.c.WriteToUDPAddrPort(b, pa.remote)
	return err
}

func (p *packetConn) close() { p.c.Close() }

func tune(c *net.UDPConn) {
	c.SetReadBuffer(8 << 20)
	c.SetWriteBuffer(8 << 20)
}

// listenAll binds addr. A wildcard host ("" / 0.0.0.0 / ::) yields separate
// IPv4 and IPv6 sockets on the same port, both with destination tracking.
func listenAll(addr string) ([]*packetConn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	if host != "" && host != "0.0.0.0" && host != "::" {
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		c, err := net.ListenUDP("udp", ua)
		if err != nil {
			return nil, err
		}
		tune(c)
		return []*packetConn{{c: c}}, nil
	}
	var out []*packetConn
	c4, err4 := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
	if err4 == nil {
		tune(c4)
		pc := &packetConn{c: c4, p4: ipv4.NewPacketConn(c4)}
		if pc.p4.SetControlMessage(ipv4.FlagDst, true) != nil {
			pc.p4 = nil
		}
		out = append(out, pc)
		port = c4.LocalAddr().(*net.UDPAddr).Port
	}
	c6, err6 := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6unspecified, Port: port})
	if err6 == nil {
		tune(c6)
		pc := &packetConn{c: c6, p6: ipv6.NewPacketConn(c6)}
		if pc.p6.SetControlMessage(ipv6.FlagDst, true) != nil {
			pc.p6 = nil
		}
		out = append(out, pc)
	}
	if len(out) == 0 {
		return nil, errors.Join(err4, err6)
	}
	return out, nil
}
