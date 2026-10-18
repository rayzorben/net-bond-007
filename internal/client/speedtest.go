package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/speedtest"
)

// Speed tests: every so often, each connection is tested on its own (sockets
// pinned to that interface, not through the bond) to learn its real download
// and upload bandwidth. Tests run only while the connection is quiet, are
// debounced, and are triggered by time, by a change of local or public
// address (a new network), or by hand.

type SpeedResult struct {
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	DownMbps float64   `json:"downMbps"`
	UpMbps   float64   `json:"upMbps"`
	PingMs   float64   `json:"pingMs"`
	UsedMB   float64   `json:"usedMB"` // data the test itself used
	PublicIP string    `json:"publicIp,omitempty"`
	Provider string    `json:"provider,omitempty"`
	Colo     string    `json:"colo,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type SpeedLink struct {
	Link        string        `json:"link"`
	Label       string        `json:"label,omitempty"`
	Kind        string        `json:"kind"`
	Metered     bool          `json:"metered,omitempty"`
	Status      string        `json:"status"` // testing | waiting | ready | idle | manual | off
	Detail      string        `json:"detail"`
	TrafficMbps float64       `json:"trafficMbps"`
	LimitMbps   float64       `json:"limitMbps,omitempty"`
	NextAt      *time.Time    `json:"nextAt,omitempty"`
	Phase       string        `json:"phase,omitempty"` // while testing
	Last        *SpeedResult  `json:"last,omitempty"`
	Best        *SpeedResult  `json:"best,omitempty"` // fastest in the history
	History     []SpeedResult `json:"history"`
}

type SpeedReport struct {
	Enabled bool        `json:"enabled"`
	Links   []SpeedLink `json:"links"`
}

const speedHistoryLen = 48

type speedLinkState struct {
	dueReason   string
	dueSince    time.Time
	localIP     string
	publicIP    string
	provider    string
	lastTrace   time.Time
	tracing     bool
	firstSeen   time.Time
	rxB, txB    uint64
	countAt     time.Time
	rateMbps    float64
	ownCounters bool
}

type speedTester struct {
	mu      sync.Mutex
	path    string
	hist    map[string][]SpeedResult
	st      map[string]*speedLinkState
	running string
	phase   string
}

func newSpeedTester(path string) *speedTester {
	t := &speedTester{path: path, hist: map[string][]SpeedResult{}, st: map[string]*speedLinkState{}}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			var f struct {
				Links map[string][]SpeedResult `json:"links"`
			}
			if json.Unmarshal(b, &f) == nil && f.Links != nil {
				t.hist = f.Links
			}
		}
	}
	return t
}

func (t *speedTester) saveLocked() {
	if t.path == "" {
		return
	}
	b, err := json.MarshalIndent(map[string]any{"links": t.hist}, "", "  ")
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, t.path)
	}
}

func (t *speedTester) last(name string) *SpeedResult {
	h := t.hist[name]
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Error == "" {
			return &h[i]
		}
	}
	return nil
}

func (t *speedTester) lastResult(name string) *SpeedResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r := t.last(name); r != nil {
		c := *r
		return &c
	}
	return nil
}

func (t *speedTester) lastAttempt(name string) time.Time {
	if h := t.hist[name]; len(h) > 0 {
		return h[len(h)-1].At
	}
	return time.Time{}
}

// idleLimit is the traffic level below which a waiting test may start: the
// base limit, raised step by step the longer the test has been waiting.
func idleLimit(cfg config.SpeedTest, waiting time.Duration) float64 {
	lim := cfg.IdleMbps
	if cfg.IdleStepMinutes > 0 && cfg.IdleStepMbps > 0 {
		steps := int(waiting / (time.Duration(cfg.IdleStepMinutes) * time.Minute))
		lim += float64(steps) * cfg.IdleStepMbps
	}
	return min(lim, max(cfg.IdleMaxMbps, cfg.IdleMbps))
}

// speedCandidates are the interfaces that are (or would be) bonded links.
func (c *Client) speedCandidates(cfg *config.Client) []platform.Iface {
	c.mu.Lock()
	ifs := append([]platform.Iface(nil), c.ifaces...)
	c.mu.Unlock()
	var out []platform.Iface
	for _, it := range ifs {
		if c.eligibility(cfg, it) == "bonded" {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// bondCounters returns the bond's byte counters for a link (fallback when the
// OS counters are unavailable).
func (c *Client) bondCounters(name string) (rx, tx uint64, ok bool) {
	c.mu.Lock()
	l := c.links[name]
	c.mu.Unlock()
	if l == nil {
		return 0, 0, false
	}
	return l.link.Rx.Bytes.Load(), l.link.Tx.Bytes.Load(), true
}

// speedTick runs once a second: measures each connection's traffic, notices
// address changes, decides what is due and starts at most one test.
func (c *Client) speedTick(now time.Time, cfg *config.Client) {
	t := c.speed
	sc := cfg.SpeedTest
	its := c.speedCandidates(cfg)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, it := range its {
		st := t.st[it.Name]
		if st == nil {
			st = &speedLinkState{firstSeen: now}
			t.st[it.Name] = st
		}
		// Traffic on this connection, all of it (the OS counters include
		// traffic that bypasses the bond).
		rx, tx, ok := platform.IfaceCounters(it.Name)
		if !ok {
			rx, tx, ok = c.bondCounters(it.Name)
		}
		if ok {
			if !st.countAt.IsZero() && rx >= st.rxB && tx >= st.txB {
				if dt := now.Sub(st.countAt).Seconds(); dt > 0 {
					m := float64(rx-st.rxB+tx-st.txB) * 8 / dt / 1e6
					st.rateMbps = 0.6*st.rateMbps + 0.4*m
				}
			}
			st.rxB, st.txB, st.countAt = rx, tx, now
		}
		if !sc.Enabled {
			continue
		}
		lc := cfg.Links[it.Name]
		// Address changes mean a new network: re-test.
		if ip := it.PrimaryIPv4().String(); st.localIP != ip {
			if st.localIP != "" && sc.OnAddressChange {
				t.markDue(st, "local address changed ("+ip+")", now)
			}
			st.localIP = ip
		}
		if !st.tracing && now.Sub(st.lastTrace) > 15*time.Minute && now.Sub(st.firstSeen) > 20*time.Second {
			st.tracing, st.lastTrace = true, now
			go func(it platform.Iface, st *speedLinkState) {
				r := tracePath(it)
				t.mu.Lock()
				defer t.mu.Unlock()
				st.tracing = false
				if r.IP == "" {
					return
				}
				if st.publicIP != "" && st.publicIP != r.IP && sc.OnAddressChange {
					t.markDue(st, "public address changed ("+r.IP+")", time.Now())
				}
				st.publicIP, st.provider = r.IP, r.Provider
			}(it, st)
		}
		if st.dueReason == "" {
			switch last := t.lastAttempt(it.Name); {
			case last.IsZero() && now.Sub(st.firstSeen) > time.Minute:
				t.markDue(st, "first test", now)
			case !last.IsZero() && now.Sub(last) >= time.Duration(sc.IntervalHours*float64(time.Hour)):
				t.markDue(st, "scheduled", now)
			}
		}
		if t.running != "" || st.dueReason == "" || (lc.Metered && !sc.Metered) {
			continue
		}
		if now.Sub(t.lastAttempt(it.Name)) < time.Duration(sc.MinGapMinutes)*time.Minute {
			continue // debounce
		}
		if st.rateMbps >= idleLimit(sc, now.Sub(st.dueSince)) {
			continue // busy: wait for a quiet moment
		}
		reason := st.dueReason
		st.dueReason = ""
		t.startLocked(c, it, reason, cfg)
	}
}

func (t *speedTester) markDue(st *speedLinkState, reason string, now time.Time) {
	if st.dueReason == "" {
		st.dueSince = now
	}
	st.dueReason = reason
}

// RunSpeedTest starts a test of one connection now (by hand: no idle wait).
func (c *Client) RunSpeedTest(name string) error {
	cfg := c.cfg.Get()
	for _, it := range c.speedCandidates(cfg) {
		if it.Name != name {
			continue
		}
		t := c.speed
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.running != "" {
			return fmt.Errorf("a test of %s is already running", t.running)
		}
		if st := t.st[name]; st != nil {
			st.dueReason = ""
		}
		t.startLocked(c, it, "manual", cfg)
		return nil
	}
	return fmt.Errorf("%s is not a bonded connection", name)
}

func (t *speedTester) startLocked(c *Client, it platform.Iface, reason string, cfg *config.Client) {
	t.running, t.phase = it.Name, "starting"
	sc := cfg.SpeedTest
	c.mu.Lock()
	cl := c.links[it.Name]
	c.mu.Unlock()
	if cl != nil {
		cl.testing.Store(true) // don't mistake our own load for a failing link
	}
	c.log.Info("speed test starting", "link", it.Name, "reason", reason)
	go func() {
		r := runSpeedTest(it, sc, func(p string) {
			t.mu.Lock()
			t.phase = p
			t.mu.Unlock()
		})
		r.Reason = reason
		if cl != nil {
			// Let queues drain before judging the link again.
			time.Sleep(2 * time.Second)
			cl.testing.Store(false)
		}
		if cfg.Links[it.Name].Metered && r.UsedMB > 0 {
			c.usage.Add(it.Name, uint64(r.UsedMB*1e6), time.Now(), cfg.Links[it.Name].CycleDay)
		}
		t.mu.Lock()
		h := append(t.hist[it.Name], r)
		if len(h) > speedHistoryLen {
			h = h[len(h)-speedHistoryLen:]
		}
		t.hist[it.Name] = h
		if st := t.st[it.Name]; st != nil && r.PublicIP != "" {
			st.publicIP, st.provider = r.PublicIP, r.Provider
		}
		t.running, t.phase = "", ""
		t.saveLocked()
		t.mu.Unlock()
		if r.Error != "" {
			c.log.Warn("speed test failed", "link", it.Name, "err", r.Error)
		} else {
			c.log.Info("speed test", "link", it.Name, "down_mbps", fmt.Sprintf("%.1f", r.DownMbps),
				"up_mbps", fmt.Sprintf("%.1f", r.UpMbps), "ping_ms", fmt.Sprintf("%.0f", r.PingMs), "used_mb", fmt.Sprintf("%.0f", r.UsedMB))
		}
	}()
}

// SpeedReport describes every connection's tests and what happens next.
func (c *Client) SpeedReport() SpeedReport {
	cfg := c.cfg.Get()
	sc := cfg.SpeedTest
	now := time.Now()
	rep := SpeedReport{Enabled: sc.Enabled, Links: []SpeedLink{}}
	t := c.speed
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, it := range c.speedCandidates(cfg) {
		lc := cfg.Links[it.Name]
		sl := SpeedLink{Link: it.Name, Label: lc.Label, Kind: it.Kind, Metered: lc.Metered,
			History: append([]SpeedResult{}, t.hist[it.Name]...), Last: t.last(it.Name)}
		for i := range sl.History {
			if r := &sl.History[i]; r.Error == "" && (sl.Best == nil || r.DownMbps > sl.Best.DownMbps) {
				sl.Best = r
			}
		}
		st := t.st[it.Name]
		if st != nil {
			sl.TrafficMbps = st.rateMbps
		}
		last := t.lastAttempt(it.Name)
		gapUntil := last.Add(time.Duration(sc.MinGapMinutes) * time.Minute)
		switch {
		case t.running == it.Name:
			sl.Status, sl.Phase = "testing", t.phase
			sl.Detail = "Testing now: " + t.phase
		case !sc.Enabled:
			sl.Status, sl.Detail = "off", "Automatic tests are off. Use Test now."
		case lc.Metered && !sc.Metered:
			sl.Status, sl.Detail = "manual", "Metered: tested only when you press Test now (saves data)."
		case st != nil && st.dueReason != "":
			lim := idleLimit(sc, now.Sub(st.dueSince))
			sl.LimitMbps = lim
			switch {
			case now.Before(gapUntil):
				sl.Status = "waiting"
				sl.NextAt = &gapUntil
				sl.Detail = fmt.Sprintf("Due (%s), but tested recently; waiting until %s.", st.dueReason, gapUntil.Format("15:04"))
			case t.running != "":
				sl.Status, sl.Detail = "waiting", fmt.Sprintf("Due (%s); waiting for the test of %s to finish.", st.dueReason, t.running)
			case st.rateMbps >= lim:
				sl.Status = "waiting"
				sl.Detail = fmt.Sprintf("Due (%s); waiting for a quiet moment: %.1f Mbit/s in use, starts below %.1f Mbit/s", st.dueReason, st.rateMbps, lim)
				if lim < sc.IdleMaxMbps {
					sl.Detail += fmt.Sprintf(" (limit rises %.0f Mbit/s every %d min, up to %.0f)", sc.IdleStepMbps, sc.IdleStepMinutes, sc.IdleMaxMbps)
				}
				sl.Detail += "."
			default:
				sl.Status, sl.Detail = "ready", "Due ("+st.dueReason+"); starting shortly."
			}
		default:
			sl.Status = "idle"
			if !last.IsZero() {
				next := last.Add(time.Duration(sc.IntervalHours * float64(time.Hour)))
				sl.NextAt = &next
				sl.Detail = "Next scheduled test " + next.Format("Jan 2 15:04") + " (or sooner if the address changes)."
			} else {
				sl.Detail = "First test soon, once the connection has settled."
			}
		}
		rep.Links = append(rep.Links, sl)
	}
	return rep
}

// ---------------------------------------------------------------- the test

const speedStreams = 4

// speedMaxBytes caps each direction of a test, so a fast connection is
// measured over a shorter time instead of burning gigabytes.
var speedMaxBytes int64 = 150_000_000

// boundClient makes an HTTP client whose connections leave through one
// interface. Each call gives a separate transport, so streams don't share a
// connection.
func boundClient(it platform.Iface, timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 8 * time.Second, Control: platform.BindControl(it.Name, it.Index)}
	if src := it.PrimaryIPv4(); src.IsValid() {
		d.LocalAddr = &net.TCPAddr{IP: src.AsSlice()}
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		TLSClientConfig:     &tls.Config{},
		TLSHandshakeTimeout: 8 * time.Second,
		MaxIdleConnsPerHost: 1,
	}}
}

func runSpeedTest(it platform.Iface, sc config.SpeedTest, phase func(string)) SpeedResult {
	r := SpeedResult{At: time.Now()}
	base := strings.TrimRight(sc.URL, "/")
	secs := time.Duration(max(sc.Seconds, 3)) * time.Second

	phase("finding the network")
	p := tracePath(it)
	r.PublicIP, r.Provider, r.Colo = p.IP, p.Provider, p.Colo
	if p.Error != "" {
		r.Error = "no internet on this connection: " + p.Error
		return r
	}

	phase("latency")
	o := speedtest.Options{
		URL: base, Duration: secs, Streams: speedStreams, MaxBytes: speedMaxBytes,
		NewClient: func() *http.Client { return boundClient(it, 0) },
	}
	r.PingMs = speedtest.Ping(speedtest.Options{URL: base, NewClient: func() *http.Client { return boundClient(it, 10*time.Second) }})

	phase("download")
	down, usedD, err := speedtest.Download(o)
	r.DownMbps = down
	if err != nil && down == 0 {
		r.Error = "download: " + err.Error()
		r.UsedMB = usedD / 1e6
		return r
	}

	phase("upload")
	up, usedU, err := speedtest.Upload(o)
	r.UpMbps = up
	r.UsedMB = (usedD + usedU) / 1e6
	if err != nil && up == 0 {
		r.Error = "upload: " + err.Error()
	}
	return r
}
