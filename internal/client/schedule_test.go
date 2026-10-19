package client

import (
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
)

func TestDailyWindow(t *testing.T) {
	loc := localZone()
	at := func(h, m int) time.Time { return time.Date(2026, 10, 19, h, m, 0, 0, loc) }
	yesterday := at(14, 0).AddDate(0, 0, -1)
	for _, tc := range []struct {
		now  time.Time
		last time.Time
		due  bool
	}{
		{at(1, 59), yesterday, false}, // not yet
		{at(2, 0), yesterday, true},
		{at(3, 30), yesterday, true},  // still in the window
		{at(4, 0), yesterday, false},  // missed: wait for tomorrow, not mid-morning
		{at(9, 42), yesterday, false}, // the case that cut a call
		{at(2, 30), at(2, 5), false},  // already ran today
	} {
		if got := dailyDue(tc.last, tc.now, "02:00"); got != tc.due {
			t.Errorf("now %s last %s: due %v, want %v", tc.now.Format("15:04"), tc.last.Format("Jan 2 15:04"), got, tc.due)
		}
	}
	if n := nextDaily(at(9, 42), "02:00"); !n.Equal(at(2, 0).AddDate(0, 0, 1)) {
		t.Errorf("next daily %s", n)
	}
	if dailyDue(yesterday, at(2, 0), "bogus") {
		t.Error("an unparsable time must never be due")
	}
}

func TestMinGapAtLeastHourly(t *testing.T) {
	if g := minGap(config.SpeedTest{MinGapMinutes: 5}); g != time.Hour {
		t.Fatalf("gap %s, want 1h", g)
	}
	if g := minGap(config.SpeedTest{MinGapMinutes: 90}); g != 90*time.Minute {
		t.Fatalf("gap %s, want 90m", g)
	}
}

func TestGuardRetestsOnlyOnNewAddress(t *testing.T) {
	var g guardState
	if m := g.noteAddrs(map[string]string{"eth0": "192.168.1.101", "sta1": "172.16.10.127"}); m != nil {
		t.Fatalf("start counted as a move: %v", m)
	}
	// Campground Wi-Fi drops and comes back with the same lease: no test.
	if m := g.noteAddrs(map[string]string{"eth0": "192.168.1.101"}); m != nil {
		t.Fatalf("a dropout counted as a move: %v", m)
	}
	if m := g.noteAddrs(map[string]string{"eth0": "192.168.1.101", "sta1": "172.16.10.127"}); m != nil {
		t.Fatalf("a return counted as a move: %v", m)
	}
	// We moved: new campground network.
	if m := g.noteAddrs(map[string]string{"eth0": "192.168.1.101", "sta1": "10.0.0.44"}); len(m) != 1 || m[0] != "sta1 10.0.0.44" {
		t.Fatalf("move not seen: %v", m)
	}
	// A phone relay joins for the first time.
	if m := g.noteAddrs(map[string]string{"eth0": "192.168.1.101", "sta1": "10.0.0.44", "relay-pixel": "192.168.8.229"}); len(m) != 1 {
		t.Fatalf("new link not seen: %v", m)
	}
}
