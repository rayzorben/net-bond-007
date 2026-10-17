package client

import (
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

func TestPickBypass(t *testing.T) {
	cands := []bypassCandidate{
		{name: "wwan0", kind: "cellular", gateway: true, metered: true, rel: 1},
		{name: "wlan0", kind: "wifi", gateway: true, rel: 0.9},
		{name: "eth0", kind: "ethernet", gateway: true, rel: 0.9},
		{name: "eth1", kind: "ethernet", gateway: true, backup: true, rel: 1},
		{name: "ppp0", kind: "other", rel: 1},
	}
	if got := pickBypass(cands, true); got != "eth0" {
		t.Fatalf("got %q, want eth0 (unmetered primary, equal reliability, wired)", got)
	}
	cands[1].rel = 0.95
	if got := pickBypass(cands, true); got != "wlan0" {
		t.Fatalf("got %q, want the more reliable wlan0", got)
	}
	if got := pickBypass(cands[3:], true); got != "eth1" {
		t.Fatalf("got %q, a backup with a gateway beats a gatewayless link", got)
	}
	if got := pickBypass(cands[4:], false); got != "" {
		t.Fatalf("got %q, a gatewayless link is unusable without device routes", got)
	}
	if got := pickBypass(cands[4:], true); got != "ppp0" {
		t.Fatalf("got %q, want the point-to-point link", got)
	}
}

func TestFailOpenEntersAndLeaves(t *testing.T) {
	cfg := config.DefaultClient()
	cfg.Server.Address = "192.0.2.1:7007"
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.NoTunnel = true
	e := bond.New(1, func([]byte) {})
	defer e.Close()
	mk := func(id uint8, name, kind string) *clink {
		l := &clink{id: id, link: e.AddLink(id, name, kind, func([]byte) error { return nil })}
		l.iface = platform.Iface{Name: name, Kind: kind, Up: true, IPv4: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/24")}}
		c.ifaces = append(c.ifaces, l.iface)
		return l
	}
	links := []*clink{mk(1, "fo-wlan", "wifi"), mk(2, "fo-eth", "ethernet")}
	set := func(s bond.State) {
		for _, l := range links {
			l.link.SetState(s)
		}
	}
	now := time.Now()
	step := func(secs int) {
		for i := 0; i < secs; i++ {
			now = now.Add(time.Second)
			c.failOpenTick(now, cfg, links)
		}
	}

	set(bond.StateDown)
	step(30)
	if v := c.bypassVia(); v != "" {
		t.Fatalf("bypassed %q before the tunnel ever came up", v)
	}

	// Bonding turned on after a long time off: the links take a moment to
	// answer, and the time without a session is not silence.
	c.ack.Store(&proto.HelloAck{})
	step(cfg.Tunnel.FailOpenSec - 2)
	if v := c.bypassVia(); v != "" {
		t.Fatalf("bypassed %q as soon as the session started", v)
	}
	set(bond.StateUp)
	step(3)
	set(bond.StateDown)
	step(cfg.Tunnel.FailOpenSec - 1)
	if v := c.bypassVia(); v != "" {
		t.Fatalf("bypassed %q too early", v)
	}
	step(2)
	if v := c.bypassVia(); v != "fo-eth" {
		t.Fatalf("bypass = %q, want fo-eth", v)
	}

	// A brief answer does not end the bypass; a steady one does.
	set(bond.StateUp)
	step(3)
	set(bond.StateDown)
	step(1)
	if c.bypassVia() == "" {
		t.Fatal("left the bypass after a blip")
	}
	set(bond.StateUp)
	step(int(failOpenRecover/time.Second) + 1)
	if v := c.bypassVia(); v != "" {
		t.Fatalf("still bypassing via %q after the server came back", v)
	}

	// Switched off: never bypass.
	cfg.Tunnel.FailOpen = false
	set(bond.StateDown)
	step(60)
	if v := c.bypassVia(); v != "" {
		t.Fatalf("bypassed %q with fail-open off", v)
	}
}
