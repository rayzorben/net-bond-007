package client

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// flapHarness runs reconcile against a fake interface list and a fake clock,
// with link sockets pointed at a dummy UDP listener.
type flapHarness struct {
	t    *testing.T
	c    *Client
	ifs  []platform.Iface
	now  time.Time
	hold int
	cool int
}

func newFlapHarness(t *testing.T, holdSec, coolSec int) *flapHarness {
	t.Helper()
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	cfg := config.DefaultClient()
	cfg.Server.Address = srv.LocalAddr().String()
	cfg.Interfaces.HoldSec, cfg.Interfaces.CooldownSec = holdSec, coolSec
	cfg.Interfaces.Exclude = ""
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.NoTunnel = true
	c.Deliver = func([]byte) {}
	h := &flapHarness{t: t, c: c, now: time.Unix(1_700_000_000, 0)}
	c.now = func() time.Time { return h.now }
	c.Discover = func() ([]platform.Iface, error) { return h.ifs, nil }
	c.Dial = func(platform.Iface, netip.AddrPort) (*net.UDPConn, error) {
		return net.DialUDP("udp", nil, srv.LocalAddr().(*net.UDPAddr))
	}
	c.newSession()
	t.Cleanup(c.Close)
	return h
}

func iface(name string, index int) platform.Iface {
	return platform.Iface{Name: name, Kind: "ethernet", Index: index, Up: true,
		IPv4: []netip.Prefix{netip.MustParsePrefix("10.0." + string(rune('0'+index)) + ".2/24")}}
}

func (h *flapHarness) step(secs int, ifs ...platform.Iface) {
	h.now = h.now.Add(time.Duration(secs) * time.Second)
	h.ifs = ifs
	h.c.reconcile()
}

func (h *flapHarness) link(name string) *clink {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return h.c.links[name]
}

func (h *flapHarness) want(names ...string) {
	h.t.Helper()
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if len(h.c.links) != len(names) {
		h.t.Fatalf("have %d links, want %v", len(h.c.links), names)
	}
	for _, n := range names {
		if _, ok := h.c.links[n]; !ok {
			h.t.Fatalf("link %s missing; have %v", n, h.c.links)
		}
	}
}

func TestVanishedLinkIsHeldThenRemoved(t *testing.T) {
	h := newFlapHarness(t, 20, 60)
	eth, usb := iface("eth0", 2), iface("usb0", 5)
	h.step(0, eth, usb)
	h.want("eth0", "usb0")

	// The USB device disappears: the link stays, marked down.
	h.step(2, eth)
	h.want("eth0", "usb0")
	if l := h.link("usb0"); !l.held.Load() || l.link.State() != bond.StateDown {
		t.Fatalf("vanished link should be held and down: held=%v state=%v", l.held.Load(), l.link.State())
	}
	// It re-enumerates as a new device within the hold: still the old
	// (dead) link, no rebuild.
	usb2 := iface("usb0", 6)
	h.step(8, eth, usb2)
	h.want("eth0", "usb0")
	if l := h.link("usb0"); !l.held.Load() || l.iface.Index != 5 {
		t.Fatalf("a re-enumerated device must not replace the held link yet: held=%v index=%d", l.held.Load(), l.iface.Index)
	}
	// Hold expires (21 s gone): removed, and not re-added during the cool-down.
	h.step(12, eth, usb2)
	h.want("eth0")
	h.step(2, eth, usb2)
	h.step(30, eth, usb2)
	h.want("eth0")
	// Cool-down over: it rejoins with the new device.
	h.step(30, eth, usb2)
	h.want("eth0", "usb0")
	if l := h.link("usb0"); l.held.Load() || l.iface.Index != 6 {
		t.Fatalf("rejoined link wrong: held=%v index=%d", l.held.Load(), l.iface.Index)
	}
}

func TestLinkBackWithinHoldSurvives(t *testing.T) {
	h := newFlapHarness(t, 20, 60)
	eth, usb := iface("eth0", 2), iface("usb0", 5)
	h.step(0, eth, usb)
	before := h.link("usb0")
	h.step(2, eth)
	h.step(5, eth, usb) // same device, same address: a blip
	h.want("eth0", "usb0")
	if l := h.link("usb0"); l != before || l.held.Load() {
		t.Fatalf("a link that came back should be the same object and released: same=%v held=%v", l == before, l.held.Load())
	}
	h.step(60, eth, usb)
	h.want("eth0", "usb0")
}

func TestCooldownSkippedForOnlyLink(t *testing.T) {
	h := newFlapHarness(t, 20, 60)
	eth, usb := iface("eth0", 2), iface("usb0", 5)
	h.step(0, eth, usb)
	h.step(0, eth)  // usb0 held
	h.step(21, eth) // usb0 removed, cooling down
	h.want("eth0")
	h.step(0)  // eth0 vanishes too: held
	h.step(21) // removed
	h.want()
	// Only usb0 is available: the cool-down must not keep us offline.
	h.step(1, usb)
	h.want("usb0")
}

func TestHoldDisabledRemovesAtOnce(t *testing.T) {
	h := newFlapHarness(t, 0, 0)
	eth, usb := iface("eth0", 2), iface("usb0", 5)
	h.step(0, eth, usb)
	h.step(1, eth)
	h.want("eth0")
	h.step(1, eth, usb)
	h.want("eth0", "usb0")
}

func TestDisabledLinkLeavesAtOnce(t *testing.T) {
	h := newFlapHarness(t, 20, 60)
	eth, usb := iface("eth0", 2), iface("usb0", 5)
	h.step(0, eth, usb)
	h.c.cfg.Mutate(func(c *config.Client) { c.Links["usb0"] = config.Link{Disabled: true} })
	h.step(0, eth, usb)
	h.want("eth0")
	h.c.cfg.Mutate(func(c *config.Client) { delete(c.Links, "usb0") })
	h.step(0, eth, usb)
	h.want("eth0", "usb0")
}
