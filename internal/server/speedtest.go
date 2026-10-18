package server

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/speedtest"
)

// Server speed test: how fast is the server's own internet connection? Every
// client byte goes through it, so a slow or throttled host caps the bond for
// everyone, and it is the first thing to rule out when the bond is slow.

// SpeedResult is one test of the server's internet connection.
type SpeedResult struct {
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	DownMbps float64   `json:"downMbps"`
	UpMbps   float64   `json:"upMbps"`
	PingMs   float64   `json:"pingMs"`
	UsedMB   float64   `json:"usedMB"`
	Limited  bool      `json:"limited"`
	Error    string    `json:"error,omitempty"`
}

// SpeedView is the dashboard's view of the server speed test.
type SpeedView struct {
	Enabled bool          `json:"enabled"`
	Running bool          `json:"running"`
	NextAt  *time.Time    `json:"nextAt,omitempty"`
	Last    *SpeedResult  `json:"last,omitempty"`
	History []SpeedResult `json:"history"`
}

const (
	serverSpeedFirstAfter = time.Minute // after start, before the first test
	serverSpeedStreams    = 4
	serverSpeedMaxBytes   = 1_500_000_000 // per direction: a fast host finishes early
	serverSpeedHistory    = 28
)

type speedState struct {
	mu      sync.Mutex
	running bool
	nextAt  time.Time
	history []SpeedResult
}

var errSpeedBusy = errors.New("a speed test is already running")

// speedTick is called by the janitor about once a second.
func (s *Server) speedTick(now time.Time) {
	c := s.cfg.Get().Speed
	st := &s.speed
	st.mu.Lock()
	if st.nextAt.IsZero() {
		st.nextAt = s.started.Add(serverSpeedFirstAfter)
	}
	due := c.Enabled && !st.running && !now.Before(st.nextAt)
	st.mu.Unlock()
	if due {
		s.RunSpeedTest("scheduled")
	}
}

// RunSpeedTest starts a test in the background.
func (s *Server) RunSpeedTest(reason string) error {
	st := &s.speed
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return errSpeedBusy
	}
	st.running = true
	st.mu.Unlock()
	go func() {
		c := s.cfg.Get().Speed
		r := runServerSpeedTest(c.URL, time.Duration(max(c.Seconds, 3))*time.Second, float64(c.LimitedMbps))
		r.Reason = reason
		st.mu.Lock()
		st.running = false
		st.nextAt = time.Now().Add(time.Duration(max(c.IntervalHours, 1)) * time.Hour)
		st.history = append(st.history, r)
		if n := len(st.history); n > serverSpeedHistory {
			st.history = st.history[n-serverSpeedHistory:]
		}
		st.mu.Unlock()
		if r.Error != "" {
			s.log.Warn("server speed test failed", "err", r.Error)
			return
		}
		lvl := s.log.Info
		if r.Limited {
			lvl = s.log.Warn
		}
		lvl("server speed test", "down_mbps", fmt.Sprintf("%.0f", r.DownMbps), "up_mbps", fmt.Sprintf("%.0f", r.UpMbps),
			"ping_ms", fmt.Sprintf("%.1f", r.PingMs), "limited", r.Limited, "reason", reason)
	}()
	return nil
}

func runServerSpeedTest(url string, d time.Duration, limitedMbps float64) SpeedResult {
	r := SpeedResult{At: time.Now()}
	o := speedtest.Options{
		URL: url, Duration: d, Streams: serverSpeedStreams, MaxBytes: serverSpeedMaxBytes, RequestBytes: 100_000_000,
		NewClient: func() *http.Client { return &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 1}} },
	}
	r.PingMs = speedtest.Ping(speedtest.Options{URL: url, NewClient: func() *http.Client { return &http.Client{Timeout: 10 * time.Second} }})
	down, usedD, err := speedtest.Download(o)
	r.DownMbps = down
	if err != nil && down == 0 {
		r.Error, r.UsedMB = "download: "+err.Error(), usedD/1e6
		return r
	}
	up, usedU, err := speedtest.Upload(o)
	r.UpMbps, r.UsedMB = up, (usedD+usedU)/1e6
	if err != nil && up == 0 {
		r.Error = "upload: " + err.Error()
		return r
	}
	r.Limited = limitedMbps > 0 && (r.DownMbps < limitedMbps || r.UpMbps < limitedMbps)
	return r
}

func (s *Server) speedView() SpeedView {
	st := &s.speed
	st.mu.Lock()
	defer st.mu.Unlock()
	v := SpeedView{Enabled: s.cfg.Get().Speed.Enabled, Running: st.running, History: append([]SpeedResult{}, st.history...)}
	if !st.nextAt.IsZero() && v.Enabled {
		t := st.nextAt
		v.NextAt = &t
	}
	if n := len(st.history); n > 0 {
		last := st.history[n-1]
		v.Last = &last
	}
	return v
}

// speedWarning is the dashboard warning for a limited server, if any.
func (s *Server) speedWarning() string {
	v := s.speedView()
	if v.Last == nil || !v.Last.Limited {
		return ""
	}
	return fmt.Sprintf("The server's internet tested slow: %.0f Mbit/s down, %.0f up (warning below %d). It limits every client's bond.",
		v.Last.DownMbps, v.Last.UpMbps, s.cfg.Get().Speed.LimitedMbps)
}
