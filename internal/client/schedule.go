package client

import (
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
)

// When the client may load a link flat out to measure it (speed tests and the
// never-slower-than-direct comparison): a call on that link feels every test.
// So tests run once a day at a quiet hour (speedTest.dailyAt), when a link's
// address changes (a new network: we moved), never more than once an hour,
// and never while a call is on.

// testMinGap is the shortest time between automatic tests.
const testMinGap = time.Hour

// dailyWindow is how long after the daily time a missed test may still run;
// after that it waits for the next day rather than land in the middle of one.
const dailyWindow = 2 * time.Hour

// callQuietFor: tests wait until the bond has carried no call media this long.
const callQuietFor = 30 * time.Second

// minGap is the configured debounce, never below testMinGap.
func minGap(sc config.SpeedTest) time.Duration {
	return max(time.Duration(sc.MinGapMinutes)*time.Minute, testMinGap)
}

// dailySlot returns the start of the current daily test window ("02:00"
// local), or zero when now is outside it.
func dailySlot(now time.Time, at string) time.Time {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}
	}
	now = now.In(localZone())
	for _, day := range []time.Time{now, now.AddDate(0, 0, -1)} {
		s := time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
		if !now.Before(s) && now.Sub(s) < dailyWindow {
			return s
		}
	}
	return time.Time{}
}

// dailyDue reports whether the daily test is due: inside today's window and
// not yet attempted in it.
func dailyDue(last, now time.Time, at string) bool {
	s := dailySlot(now, at)
	return !s.IsZero() && last.Before(s)
}

// nextDaily is the next start of the daily window after now.
func nextDaily(now time.Time, at string) time.Time {
	t, err := time.Parse("15:04", at)
	if err != nil {
		return time.Time{}
	}
	now = now.In(localZone())
	s := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !s.After(now) {
		s = s.AddDate(0, 0, 1)
	}
	return s
}

// callActive reports whether a call is going through the bond now.
func (c *Client) callActive(now time.Time) bool {
	eng := c.engine.Load()
	if eng == nil {
		return false
	}
	at := eng.RealtimeAt()
	return !at.IsZero() && now.Sub(at) < callQuietFor
}

var (
	zoneOnce sync.Once
	zone     *time.Location
)

// localZone is the local time zone. OpenWrt keeps it outside Go's reach (a
// POSIX TZ string in /etc/TZ, /etc/localtime pointing at a missing file), so
// fall back to the zone name in its system config.
func localZone() *time.Location {
	zoneOnce.Do(func() {
		zone = time.Local
		if os.Getenv("TZ") != "" {
			return
		}
		if _, err := os.Stat("/etc/localtime"); err == nil {
			return
		}
		out, err := exec.Command("uci", "-q", "get", "system.@system[0].zonename").Output()
		if err != nil {
			return
		}
		if l, err := time.LoadLocation(strings.ReplaceAll(strings.TrimSpace(string(out)), " ", "_")); err == nil {
			zone = l
		}
	})
	return zone
}
