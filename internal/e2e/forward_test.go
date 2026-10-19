package e2e

import (
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"github.com/rayzorben/net-bond-007/internal/config"
)

func freePort(t *testing.T) uint16 {
	t.Helper()
	for {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := uint16(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		u, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(p)})
		if err == nil {
			u.Close()
			return p
		}
	}
}

// A public port on the server reaches a service on the client, with the
// real remote address as the source, over TCP and UDP; and the client's own
// packets from that UDP port leave from the public port (full cone).
func TestPortForward(t *testing.T) {
	port := freePort(t)
	r := newRig(t, []struct {
		name, kind string
		delay      time.Duration
		loss       float64
	}{{"eth0", "ethernet", 5 * time.Millisecond, 0}, {"wlan0", "wifi", 15 * time.Millisecond, 0}},
		func(c *config.Client) {
			c.Forwards = []config.Forward{{Name: "torrent", Proto: "both", Port: port, Device: "192.168.8.10"}}
		},
		func(s *config.Server) {
			s.Forward.Ports = fmt.Sprint(port)
			s.Egress.AllowPrivate = true // the "internet" here is loopback
		})
	// The test's internet is loopback, which IP stacks drop as martian.
	r.srv.Gateway().AllowLoopback()
	allow := tcpip.AllowExternalLoopbackTrafficOption(true)
	r.os.s.SetNetworkProtocolOption(ipv4.ProtocolNumber, &allow)
	ip := netip.MustParseAddr(r.srv.Snapshot(0).Sessions[0].IP)
	self := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(ip.As4()), Port: port}

	// Wait until the server reports the forward open to the client.
	deadline := time.Now().Add(10 * time.Second)
	for {
		fv := r.cli.Forwards()
		if len(fv.Forwards) == 1 && fv.Forwards[0].ServerState == "open" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("forward never opened: %+v", fv)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if fs := r.srv.Snapshot(0).Sessions[0].Forwards; len(fs) != 2 {
		t.Fatalf("server shows forwards %+v, want tcp and udp", fs)
	}

	// TCP: echo server on the client.
	ln, err := gonet.ListenTCP(r.os.s, self, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	peers := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		peers <- c.RemoteAddr().String()
		io.Copy(c, c)
	}()
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	msg := payload(256 << 10)
	go c.Write(msg)
	got := make([]byte, len(msg))
	c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("tcp echo: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatal("tcp echo corrupted")
	}
	if p := <-peers; p != c.LocalAddr().String() {
		t.Fatalf("client saw the connection from %s, want the real peer %s", p, c.LocalAddr())
	}

	// UDP: the client answers a peer, then speaks first to another peer.
	uc, err := gonet.DialUDP(r.os.s, &self, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	a, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer a.Close()
	pub := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)}
	a.WriteToUDP([]byte("hello"), pub)
	buf := make([]byte, 1500)
	uc.SetDeadline(time.Now().Add(5 * time.Second))
	n, from, err := uc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "hello" {
		t.Fatalf("udp in: %q %v", buf[:n], err)
	}
	if from.String() != a.LocalAddr().String() {
		t.Fatalf("udp from %s, want the real peer %s", from, a.LocalAddr())
	}
	uc.WriteTo([]byte("hi back"), from)
	a.SetDeadline(time.Now().Add(5 * time.Second))
	n, src, err := a.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "hi back" || src.Port != int(port) {
		t.Fatalf("udp reply: %q from %v, %v", buf[:n], src, err)
	}

	b, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	defer b.Close()
	bAddr := b.LocalAddr().(*net.UDPAddr)
	uc.WriteTo([]byte("first"), &net.UDPAddr{IP: bAddr.IP, Port: bAddr.Port})
	b.SetDeadline(time.Now().Add(5 * time.Second))
	n, src, err = b.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "first" {
		t.Fatalf("udp out: %q %v", buf[:n], err)
	}
	if src.Port != int(port) {
		t.Fatalf("outbound left from port %d, want the public port %d", src.Port, port)
	}
	b.WriteToUDP([]byte("reply"), src)
	n, _, err = uc.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "reply" {
		t.Fatalf("udp out reply: %q %v", buf[:n], err)
	}

	// Removing the forward closes the public ports.
	r.cfg.Mutate(func(c *config.Client) { c.Forwards = nil })
	deadline = time.Now().Add(10 * time.Second)
	for len(r.srv.Snapshot(0).Sessions[0].Forwards) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("server kept the ports open")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
