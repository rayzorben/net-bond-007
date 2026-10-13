package bond

import (
	"crypto/tls"
	"encoding/binary"
	"net"
	"net/netip"
	"testing"
	"time"
)

// ipv4TCP builds a minimal IPv4/TCP packet with the given payload.
func ipv4TCP(src, dst netip.AddrPort, payload []byte) []byte {
	p := make([]byte, 40+len(payload))
	p[0] = 0x45
	p[9] = 6
	s, d := src.Addr().As4(), dst.Addr().As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[20:], src.Port())
	binary.BigEndian.PutUint16(p[22:], dst.Port())
	p[32] = 5 << 4 // data offset 20
	copy(p[40:], payload)
	return p
}

func TestFlowTableDirectionAndLinks(t *testing.T) {
	var ft flowTable
	ft.upstream = true // client side: what we send goes upstream
	in := netip.MustParseAddrPort("10.77.0.2:50000")
	out := netip.MustParseAddrPort("104.16.1.1:443")
	now := time.Now()
	ft.note(ipv4TCP(in, out, make([]byte, 100)), true, []uint8{1, 2}, now) // upload, 2 copies
	ft.note(ipv4TCP(out, in, make([]byte, 1000)), false, []uint8{1}, now)  // download won on link 1
	ft.note(ipv4TCP(out, in, make([]byte, 1000)), false, []uint8{2}, now)  // ... and on link 2
	fs := ft.snapshot()
	if len(fs) != 1 {
		t.Fatalf("want 1 flow, got %d", len(fs))
	}
	f := fs[0]
	if f.Key.Inside != in || f.Key.Outside != out || f.Key.Proto != 6 {
		t.Fatalf("bad key %+v", f.Key)
	}
	if f.TxBytes != 140 || f.RxBytes != 2080 {
		t.Fatalf("bytes tx=%d rx=%d", f.TxBytes, f.RxBytes)
	}
	if f.CopiesOn[1] != 1 || f.CopiesOn[2] != 1 || f.WinsOn[1] != 1 || f.WinsOn[2] != 1 {
		t.Fatalf("link attribution %+v %+v", f.CopiesOn, f.WinsOn)
	}

	// Server orientation: what it sends goes downstream.
	var sv flowTable
	sv.note(ipv4TCP(out, in, make([]byte, 10)), true, []uint8{1}, now)
	if k := sv.snapshot()[0].Key; k.Inside != in || k.Outside != out {
		t.Fatalf("server orientation wrong: %+v", k)
	}
}

func TestFlowRatesAndEviction(t *testing.T) {
	var ft flowTable
	ft.upstream = true
	in := netip.MustParseAddrPort("10.77.0.2:1")
	out := netip.MustParseAddrPort("1.2.3.4:443")
	t0 := time.Now()
	ft.tick(t0)
	for i := 0; i < 100; i++ {
		ft.note(ipv4TCP(out, in, make([]byte, 1210)), false, []uint8{1}, t0)
	}
	ft.tick(t0.Add(time.Second))
	if r := ft.snapshot()[0].RxBps; r < 400e3 || r > 600e3 { // EWMA half of ~1 Mbit
		t.Fatalf("rate %v", r)
	}
	ft.tick(t0.Add(flowIdleAfter + 2*time.Second))
	if len(ft.snapshot()) != 0 {
		t.Fatal("idle flow not evicted")
	}
}

func TestTLSSNIFromRealClientHello(t *testing.T) {
	// Capture a genuine ClientHello produced by crypto/tls.
	c1, c2 := net.Pipe()
	go func() {
		tls.Client(c1, &tls.Config{ServerName: "api.torbox.app", InsecureSkipVerify: true}).Handshake()
	}()
	buf := make([]byte, 4096)
	n, _ := c2.Read(buf)
	c1.Close()
	c2.Close()
	tcp := make([]byte, 20+n)
	tcp[12] = 5 << 4
	copy(tcp[20:], buf[:n])
	if got := tlsSNI(tcp); got != "api.torbox.app" {
		t.Fatalf("SNI = %q", got)
	}
	if tlsSNI(tcp[:30]) != "" || tlsSNI([]byte("GET / HTTP/1.1\r\n\r\n    ")) != "" {
		t.Fatal("garbage parsed as SNI")
	}
}
