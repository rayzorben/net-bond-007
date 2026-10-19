package client

import (
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/rayzorben/net-bond-007/internal/bond"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

func TestMergeConfigReplacesLinkOverrides(t *testing.T) {
	c := config.DefaultClient()
	c.Links = map[string]config.Link{"eth1": {Disabled: true}, "usb1": {Backup: true}}
	got, err := MergeConfig(c, []byte(`{"links":{"usb1":{"backup":true}},"bonding":{"mode":"mirror"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Links["eth1"]; ok {
		t.Fatal("eth1 override should have been removed")
	}
	if !got.Links["usb1"].Backup || got.Bonding.Mode != "mirror" {
		t.Fatalf("merge lost data: %+v %s", got.Links, got.Bonding.Mode)
	}
	// A partial document without "links" keeps existing overrides.
	got, _ = MergeConfig(got, []byte(`{"name":"x"}`))
	if !got.Links["usb1"].Backup || got.Name != "x" {
		t.Fatalf("partial update clobbered links: %+v", got.Links)
	}
}

func TestEstimateCapacity(t *testing.T) {
	if estimate(50e6, 100e6, 0, true) != 62.5e6 {
		t.Fatal("healthy link should get 25% headroom")
	}
	if estimate(10e6, 100e6, 0, false) != 25e6 {
		t.Fatal("floor at 25% of best link")
	}
	if estimate(10e6, 100e6, 42e6, true) != 42e6 {
		t.Fatal("manual capacity wins")
	}
}

// Every policy field the server relies on must be filled from config.
// (A field silently left out once meant the server never got the user's
// pacing thresholds.)
func TestWirePolicyPopulatesEveryField(t *testing.T) {
	cfg := config.DefaultClient()
	cfg.Links = map[string]config.Link{"eth0": {MaxMbps: 50}}
	cfg.Bonding.LossTolerantTCP = true // off by default; it must still reach the wire
	cfg.Forwards = []config.Forward{{Proto: "tcp", Port: 40000, Device: "192.168.8.2"}}
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := bond.New(1, func([]byte) {})
	defer e.Close()
	c.links["eth0"] = &clink{id: 1, link: e.AddLink(1, "eth0", "sim", func([]byte) error { return nil })}
	c.links["eth0"].link.SetMetered(true, 0.1)
	c.links["eth0"].iface.Name = "eth0"
	c.plan.Store(&bond.Plan{Copies: 2, BenchedID: []uint8{3}, Mode: "direct", Direct: true, DirectVia: 1}) // automatic scaling active
	p := c.wirePolicy()
	v := reflect.ValueOf(p)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("proto.Policy.%s is never set by wirePolicy", v.Type().Field(i).Name)
		}
	}
}

func TestABVerdict(t *testing.T) {
	cases := map[string][2]float64{
		"faster": {60, 40},
		"same":   {40, 41},
		"slower": {10, 40},
	}
	for want, v := range cases {
		got := verdict(ABSummary{Mbps: v[0], OK: 3}, ABSummary{Mbps: v[1], OK: 3})
		if !strings.Contains(got, want) {
			t.Errorf("%v: verdict %q, want %q", v, got, want)
		}
	}
	if !strings.Contains(verdict(ABSummary{}, ABSummary{OK: 1, Mbps: 5}), "failed") {
		t.Error("bonded failure not reported")
	}
}

// Pinning only applies while the tunnel carries all LAN traffic: with full
// tunnel off it would pull every new LAN connection into the tunnel anyway.
func TestPinsOnlyWithFullTunnel(t *testing.T) {
	cfg := config.DefaultClient()
	cfg.Tunnel.RouterMode, cfg.Tunnel.PinConnections, cfg.Tunnel.FullTunnel = true, true, false
	c := New(config.NewMemory(cfg), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if c.pinsWanted(cfg) {
		t.Fatal("pinning wanted with full tunnel off")
	}
	cfg.Tunnel.FullTunnel = true
	if c.pinsWanted(cfg) != platform.PinsSupported() {
		t.Fatal("pinning not wanted in full-tunnel router mode")
	}
}
