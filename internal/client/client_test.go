package client

import (
	"testing"

	"github.com/rayzorben/bond007/internal/config"
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
