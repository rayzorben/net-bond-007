package client

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageCycleAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := LoadUsage(path)
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 12, 0, 0, 0, time.Local) }
	u.Add("eth3", 10e9, d(10, 20), 15)
	if got := u.Add("eth3", 5e9, d(11, 14), 15); got != 15e9 {
		t.Fatalf("same cycle (Oct 15 - Nov 14): %d", got)
	}
	u.Save(true)
	u2 := LoadUsage(path)
	if got := u2.Get("eth3").Bytes; got != 15e9 {
		t.Fatalf("not persisted: %d", got)
	}
	if got := u2.Add("eth3", 1e9, d(11, 15), 15); got != 1e9 {
		t.Fatalf("new cycle should reset: %d", got)
	}
	if cs := cycleStart(d(1, 3), 15); cs.Month() != 12 || cs.Year() != 2025 {
		t.Fatalf("January cycle should start Dec 15: %v", cs)
	}
}

func TestPathWarnings(t *testing.T) {
	w := pathWarnings([]PathResult{
		{Link: "eth0", IP: "143.105.1.1", Provider: "Starlink (SpaceX)"},
		{Link: "eth3", IP: "172.56.78.119", Provider: "T-Mobile"},
		{Link: "sta0", IP: "143.105.9.9", Provider: "Starlink (SpaceX)"},
	})
	if len(w) != 1 || !strings.Contains(w[0], "eth0 and sta0") {
		t.Fatalf("warnings: %q", w)
	}
	if providerFor(netip.MustParseAddr("172.56.78.119"), "") != "T-Mobile" {
		t.Fatal("T-Mobile prefix")
	}
}
