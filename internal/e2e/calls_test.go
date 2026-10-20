package e2e

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
)

// A call (UDP to a call port) rides the call lane both ways: the client and
// server agree on it, and every packet comes back while one link is lossy.
func TestCallRidesCallLane(t *testing.T) {
	r := newRig(t, []linkSpec{
		{"eth0", "ethernet", 5 * time.Millisecond, 0},
		{"wlan0", "wifi", 5 * time.Millisecond, 0.3},
	}, nil)

	echo, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := echo.ReadFromUDP(buf)
			if err != nil {
				return
			}
			echo.WriteToUDP(buf[:n], from)
		}
	}()
	const callAddr = "203.0.113.20:3479"
	real := echo.LocalAddr().String()
	r.srv.Gateway().SetDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == callAddr {
			addr = real
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})

	ap := netip.MustParseAddrPort(callAddr)
	conn, err := gonet.DialUDP(r.os.s, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(ap.Addr().As4()), Port: ap.Port()}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	got := make(chan struct{}, 256)
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
			got <- struct{}{}
		}
	}()
	const n = 100
	for i := 0; i < n; i++ {
		if _, err := conn.Write(make([]byte, 160)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	echoed := 0
	deadline := time.After(3 * time.Second)
wait:
	for echoed < n {
		select {
		case <-got:
			echoed++
		case <-deadline:
			break wait
		}
	}

	ce := r.cli.Snapshot(0).Engine
	se := r.srv.Snapshot(0).Sessions[0].Engine
	t.Logf("echoed %d/%d; client lane=%v tx=%d rx=%d; server lane=%v tx=%d rx=%d",
		echoed, n, ce.CallLane, ce.CallsTx, ce.CallsRx, se.CallLane, se.CallsTx, se.CallsRx)
	if !ce.CallLane || !se.CallLane {
		t.Fatalf("call lane not negotiated: client %v, server %v", ce.CallLane, se.CallLane)
	}
	if ce.CallsTx < n || se.CallsRx < n || se.CallsTx < uint64(echoed) || ce.CallsRx < uint64(echoed) {
		t.Fatal("call packets did not ride the lane")
	}
	// The first packet can be lost before the gateway's UDP flow exists;
	// after that the mirrored copies cover the lossy link.
	if echoed < n-1 {
		t.Fatalf("echoed %d/%d call packets", echoed, n)
	}
}
