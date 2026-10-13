package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Usage counts bytes per metered link for the current billing cycle and
// persists them (usage.json next to the config) so a restart doesn't reset
// the month.
type Usage struct {
	mu    sync.Mutex
	path  string
	Links map[string]*LinkUsage `json:"links"`
	saved time.Time
	dirty bool
}

type LinkUsage struct {
	CycleStart time.Time `json:"cycleStart"`
	Bytes      uint64    `json:"bytes"`
}

func LoadUsage(path string) *Usage {
	u := &Usage{path: path, Links: map[string]*LinkUsage{}}
	if path == "" {
		return u
	}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, u)
		if u.Links == nil {
			u.Links = map[string]*LinkUsage{}
		}
	}
	return u
}

// cycleStart returns the start of the billing cycle containing now.
func cycleStart(now time.Time, day int) time.Time {
	if day < 1 || day > 28 {
		day = 1
	}
	y, m, d := now.Date()
	if d < day {
		m--
	}
	return time.Date(y, m, day, 0, 0, 0, 0, now.Location())
}

// Add counts n bytes on link and returns the cycle's total.
func (u *Usage) Add(link string, n uint64, now time.Time, cycleDay int) uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	cs := cycleStart(now, cycleDay)
	lu := u.Links[link]
	if lu == nil || !lu.CycleStart.Equal(cs) {
		lu = &LinkUsage{CycleStart: cs}
		u.Links[link] = lu
		u.dirty = true
	}
	if n > 0 {
		lu.Bytes += n
		u.dirty = true
	}
	return lu.Bytes
}

func (u *Usage) Get(link string) LinkUsage {
	u.mu.Lock()
	defer u.mu.Unlock()
	if lu := u.Links[link]; lu != nil {
		return *lu
	}
	return LinkUsage{}
}

// Reset zeroes a link's count for the current cycle.
func (u *Usage) Reset(link string) {
	u.mu.Lock()
	if lu := u.Links[link]; lu != nil {
		lu.Bytes, u.dirty = 0, true
	}
	u.mu.Unlock()
	u.Save(true)
}

// Save writes the file (at most every 5 minutes unless forced: routers keep
// it in flash).
func (u *Usage) Save(force bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.path == "" || !u.dirty || (!force && time.Since(u.saved) < 5*time.Minute) {
		return
	}
	b, err := json.MarshalIndent(u, "", "  ")
	if err != nil {
		return
	}
	tmp := u.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil && os.Rename(tmp, filepath.Clean(u.path)) == nil {
		u.saved, u.dirty = time.Now(), false
	}
}
