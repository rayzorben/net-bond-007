package e2e

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// One inner UDP socket talking to two destinations leaves the server from
// one public port (endpoint-independent mapping, so peer-to-peer hole
// punching works), and a stranger sending to that port is not let in.
func TestUDPEndpointIndependentMapping(t *testing.T) {
	r := newRig(t, []linkSpec{{"eth0", "ethernet", 5 * time.Millisecond, 0}}, nil,
		func(s *config.Server) { s.Egress.AllowPrivate = true })
	r.srv.Gateway().AllowLoopback()
	allow := tcpip.AllowExternalLoopbackTrafficOption(true)
	r.os.s.SetNetworkProtocolOption(ipv4.ProtocolNumber, &allow)

	// Each "STUN server" answers with the public address it saw.
	stun := func() *net.UDPConn {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		go func() {
			buf := make([]byte, 2048)
			for {
				_, from, err := c.ReadFromUDPAddrPort(buf)
				if err != nil {
					return
				}
				c.WriteToUDPAddrPort([]byte(from.String()), from)
			}
		}()
		return c
	}
	a, b := stun(), stun()

	conn, err := gonet.DialUDP(r.os.s, &tcpip.FullAddress{NIC: 1, Port: 41641}, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ask := func(c *net.UDPConn) string {
		ap := netip.MustParseAddrPort(c.LocalAddr().String())
		to := net.UDPAddrFromAddrPort(ap)
		buf := make([]byte, 2048)
		for i := 0; i < 20; i++ {
			if _, err := conn.WriteTo([]byte("?"), to); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
			if n, _, err := conn.ReadFrom(buf); err == nil {
				return string(buf[:n])
			}
		}
		t.Fatalf("no answer from %s", ap)
		return ""
	}
	pa, pb := ask(a), ask(b)
	if pa != pb {
		t.Fatalf("public mapping varies by destination: %s vs %s", pa, pb)
	}

	// A remote the client never sent to is filtered.
	stranger, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer stranger.Close()
	pub := netip.MustParseAddrPort(pa)
	for i := 0; i < 5; i++ {
		stranger.WriteToUDPAddrPort([]byte("hi"), pub)
	}
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n, from, err := conn.ReadFrom(make([]byte, 2048)); err == nil {
		t.Fatalf("unsolicited packet let in: %d bytes from %v", n, from)
	}
}
