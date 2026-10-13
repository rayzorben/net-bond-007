package client

import (
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

func TestRelayLinkName(t *testing.T) {
	ip := netip.MustParseAddr("192.168.8.42")
	for in, want := range map[string]string{
		"Pixel 9 Pro XL":                 "relay-pixel-9-pro-xl",
		"Ray's  Phone":                   "relay-rays-phone",
		"--odd__name..":                  "relay-odd-name",
		"":                               "relay-192-168-8-42",
		"☎☎☎":                            "relay-192-168-8-42",
		"abcdefghijklmnopqrstuvwxyz0123": "relay-abcdefghijklmnopqrstuvwx",
	} {
		if got := relayLinkName(in, ip); got != want {
			t.Errorf("relayLinkName(%q) = %q, want %q", in, got, want)
		}
	}
}

func relayTestClient(t *testing.T, token string) *Client {
	cfg := config.DefaultClient()
	cfg.Server.Address = "bond.example.com:7007"
	cfg.Relay = config.Relay{Enabled: true, Token: token, Listen: "127.0.0.1:0"}
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.ifaces = []platform.Iface{
		{Name: "br-lan", Up: true, IPv4: []netip.Prefix{netip.MustParsePrefix("192.168.8.1/24")}},
		{Name: "sta0", Up: true, IPv4: []netip.Prefix{netip.MustParsePrefix("10.20.0.17/22")}},
	}
	return c
}

func TestRelayHelloChecks(t *testing.T) {
	now := time.Now()
	lan := netip.MustParseAddrPort("192.168.8.42:7008")
	hello := proto.RelayHello{Token: "pair", Name: "pixel", Cellular: true}

	c := relayTestClient(t, "")
	if a := c.relayHello(lan, hello, now); a.OK || a.Reason != "set a pairing token on the router first" {
		t.Fatalf("no token on router: %+v", a)
	}

	c = relayTestClient(t, "pair")
	bad := hello
	bad.Token = "nope"
	if a := c.relayHello(lan, bad, now); a.OK || a.Reason != "wrong pairing token" {
		t.Fatalf("wrong token: %+v", a)
	}
	// sta0 is a bonded link (campground Wi-Fi): phones there are refused.
	if a := c.relayHello(netip.MustParseAddrPort("10.20.0.99:7008"), hello, now); a.OK || a.Reason != "not on the local network" {
		t.Fatalf("phone on a WAN: %+v", a)
	}
	a := c.relayHello(lan, hello, now)
	if !a.OK || a.Server != "bond.example.com:7007" {
		t.Fatalf("good hello: %+v", a)
	}
	ifs, addrs := c.relayLinks(now)
	if len(ifs) != 1 || ifs[0].Name != "relay-pixel" || ifs[0].Kind != "cellular" || !ifs[0].Up || addrs["relay-pixel"] != lan {
		t.Fatalf("relay links: %+v %v", ifs, addrs)
	}
	if st := c.eligibility(c.cfg.Get(), ifs[0]); st != "bonded" {
		t.Fatalf("relay eligibility = %s", st)
	}

	// No mobile data: listed, but down, so it is not bonded.
	off := hello
	off.Cellular = false
	c.relayHello(lan, off, now)
	ifs, _ = c.relayLinks(now)
	if st := c.eligibility(c.cfg.Get(), ifs[0]); st != "down" {
		t.Fatalf("relay without cellular: eligibility = %s", st)
	}

	// Silent for longer than the expiry: forgotten.
	if ifs, _ := c.relayLinks(now.Add(relayExpiry + time.Second)); len(ifs) != 0 {
		t.Fatalf("silent relay not expired: %+v", ifs)
	}
}
