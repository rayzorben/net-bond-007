package e2e

import (
	"crypto/sha256"
	"io"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

// fakePhone does what the Android relay app does: announce itself to the
// client, then copy frames between the client (LAN socket) and the server
// (its "cellular" socket) without looking inside them.
type fakePhone struct {
	t      *testing.T
	lan    *net.UDPConn
	cell   *net.UDPConn
	router netip.AddrPort // client's relay listener
	token  string
	name   string

	server  atomic.Pointer[netip.AddrPort]
	back    atomic.Pointer[netip.AddrPort] // client's link socket
	lastAck atomic.Pointer[proto.RelayAck]
	up      atomic.Uint64
	down    atomic.Uint64
	stop    chan struct{}
}

func newFakePhone(t *testing.T, router netip.AddrPort, token, name string) *fakePhone {
	lan, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	cell, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePhone{t: t, lan: lan, cell: cell, router: router, token: token, name: name, stop: make(chan struct{})}
	go p.lanLoop()
	go p.cellLoop()
	go p.announce()
	t.Cleanup(p.close)
	return p
}

func (p *fakePhone) close() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
		p.lan.Close()
		p.cell.Close()
	}
}

func (p *fakePhone) announce() {
	for {
		f, _ := proto.AppendJSON(nil, proto.TypeRelayHello, 0, 0, proto.RelayHello{
			Token: p.token, Name: p.name, Model: "Test Phone", Cellular: true, Battery: 80, Signal: 3,
		})
		p.lan.WriteToUDPAddrPort(f, p.router)
		select {
		case <-p.stop:
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *fakePhone) lanLoop() {
	buf := make([]byte, proto.MaxFrame)
	for {
		n, from, err := p.lan.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		f := buf[:n]
		if h, err := proto.ParseHeader(f); err == nil && h.Type == proto.TypeRelayAck {
			var a proto.RelayAck
			if proto.ParseJSON(f, &a) == nil {
				p.lastAck.Store(&a)
				if ap, err := netip.ParseAddrPort(a.Server); err == nil && a.OK {
					p.server.Store(&ap)
				}
			}
			continue
		}
		p.back.Store(&from)
		if s := p.server.Load(); s != nil {
			p.cell.WriteToUDPAddrPort(f, *s)
			p.up.Add(uint64(n))
		}
	}
}

func (p *fakePhone) cellLoop() {
	buf := make([]byte, proto.MaxFrame)
	for {
		n, from, err := p.cell.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		s, back := p.server.Load(), p.back.Load()
		if s == nil || back == nil || from != *s {
			continue
		}
		p.lan.WriteToUDPAddrPort(buf[:n], *back)
		p.down.Add(uint64(n))
	}
}

func relayRig(t *testing.T) (*rig, netip.AddrPort) {
	r := newRig(t, []linkSpec{{"eth0", "ethernet", 5 * time.Millisecond, 0}}, func(c *config.Client) {
		c.Relay = config.Relay{Enabled: true, Token: "pair-me", Listen: "127.0.0.1:0"}
	})
	ap, err := netip.ParseAddrPort(r.cli.Snapshot(0).RelayListen)
	if err != nil {
		t.Fatalf("relay listener: %v (%+v)", err, r.cli.Snapshot(0).Warnings)
	}
	return r, ap
}

func waitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A phone relay joins as a link, the server sees it arrive from the phone's
// "cellular" address, and with the client's own link dead a download runs
// entirely through the phone and arrives intact.
func TestPhoneRelay(t *testing.T) {
	r, router := relayRig(t)
	phone := newFakePhone(t, router, "pair-me", "Ray's Pixel")

	waitFor(t, "relay link up", 5*time.Second, func() bool {
		for _, l := range r.cli.Snapshot(0).Links {
			if l.Name == "relay-rays-pixel" && l.State == "up" {
				return true
			}
		}
		return false
	})
	cs := r.cli.Snapshot(0)
	for _, l := range cs.Links {
		if l.Name != "relay-rays-pixel" {
			continue
		}
		if l.Kind != "cellular" || l.Relay == nil || l.Relay.Model != "Test Phone" || l.Relay.Battery != 80 {
			t.Fatalf("relay link view: kind=%s relay=%+v", l.Kind, l.Relay)
		}
	}
	cellAddr := phone.cell.LocalAddr().String()
	var seen bool
	for _, l := range r.srv.Snapshot(0).Sessions[0].Links {
		if l.Name == "relay-rays-pixel" {
			seen = true
			if l.Remote != cellAddr {
				t.Errorf("server sees relay link from %s, want the phone's cellular socket %s", l.Remote, cellAddr)
			}
		}
	}
	if !seen {
		t.Fatal("server does not list the relay link")
	}

	// Unplug the client's own link: everything must flow through the phone.
	r.proxies["eth0"].dead.Store(true)
	waitFor(t, "eth0 down", 5*time.Second, func() bool {
		for _, l := range r.cli.Snapshot(0).Links {
			if l.Name == "eth0" {
				return l.State == "down"
			}
		}
		return false
	})
	body := payload(2 << 20)
	url := r.webServer(t, body)
	before := phone.down.Load()
	resp, err := r.os.httpClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read: %v (got %d bytes)", err, len(got))
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("payload corrupted: got %d bytes", len(got))
	}
	if moved := phone.down.Load() - before; moved < uint64(len(body)) {
		t.Fatalf("phone relayed %d bytes downstream, want at least %d", moved, len(body))
	}
	t.Logf("phone relayed up=%d down=%d bytes", phone.up.Load(), phone.down.Load())
}

// A phone with the wrong token is refused and never becomes a link.
func TestPhoneRelayWrongToken(t *testing.T) {
	r, router := relayRig(t)
	phone := newFakePhone(t, router, "guess", "intruder")
	waitFor(t, "refusal", 3*time.Second, func() bool {
		a := phone.lastAck.Load()
		return a != nil && !a.OK && a.Reason == "wrong pairing token"
	})
	time.Sleep(300 * time.Millisecond)
	for _, l := range r.cli.Snapshot(0).Links {
		if l.Name == "relay-intruder" {
			t.Fatal("refused phone became a link")
		}
	}
	if phone.server.Load() != nil {
		t.Fatal("refused phone was told the server address")
	}
}
