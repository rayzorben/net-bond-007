package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// A/B test: download the same file through the bond and directly over the
// best physical link, alternating, and compare. Sockets are pinned to an
// interface (the tunnel adapter, or the physical link) so the comparison is
// valid regardless of how the OS routes the client's own traffic (GL.iNet
// firmware, for example, sends router-originated traffic around the tunnel).

type ABRound struct {
	Path      string  `json:"path"` // bonded | direct
	Via       string  `json:"via"`
	Mbps      float64 `json:"mbps"`
	Bytes     int64   `json:"bytes"`
	Seconds   float64 `json:"seconds"`
	ConnectMs float64 `json:"connectMs"` // TCP handshake time ~ round-trip latency
	TTFBMs    float64 `json:"ttfbMs"`
	Error     string  `json:"error,omitempty"`
}

type ABResult struct {
	Running   bool      `json:"running"`
	Started   int64     `json:"started,omitempty"`
	Finished  int64     `json:"finished,omitempty"`
	URL       string    `json:"url"`
	Rounds    []ABRound `json:"rounds"`
	Bonded    ABSummary `json:"bonded"`
	Direct    ABSummary `json:"direct"`
	Verdict   string    `json:"verdict,omitempty"`
	Error     string    `json:"error,omitempty"`
	DirectVia string    `json:"directVia,omitempty"`
}

type ABSummary struct {
	Mbps      float64 `json:"mbps"`
	ConnectMs float64 `json:"connectMs"`
	OK        int     `json:"ok"`
}

type abState struct {
	mu  sync.Mutex
	res ABResult
}

// DefaultABURL is a CDN download that works through any network.
const DefaultABURL = "https://speed.cloudflare.com/__down?bytes=10000000"

// StartAB launches the test in the background. It returns an error if one is
// already running or the bond has no tunnel.
func (c *Client) StartAB(url string, rounds int) error {
	if url == "" {
		url = DefaultABURL
	}
	rounds = min(max(rounds, 1), 5)
	c.tunMu.Lock()
	tun := c.tun
	c.tunMu.Unlock()
	if tun == nil {
		return errors.New("bonding is off or not connected: turn on Connect first (the test does not change your routing)")
	}
	direct := c.bestPhysical()
	if direct == nil {
		return errors.New("no healthy physical link to compare against")
	}
	c.ab.mu.Lock()
	if c.ab.res.Running {
		c.ab.mu.Unlock()
		return errors.New("a test is already running")
	}
	c.ab.res = ABResult{Running: true, Started: time.Now().UnixMilli(), URL: url, Rounds: []ABRound{}, DirectVia: direct.iface.Name}
	c.ab.mu.Unlock()

	go func() {
		tunName, tunIdx := tun.Name(), tun.Index()
		var tunIP netip.Addr
		if a := c.ack.Load(); a != nil {
			tunIP, _ = netip.ParseAddr(a.ClientIP)
		}
		for i := 0; i < rounds; i++ {
			for _, path := range []string{"direct", "bonded"} {
				var r ABRound
				if path == "bonded" {
					r = abDownload(url, tunName, tunIdx, tunIP)
					r.Via = tunName
				} else {
					r = abDownload(url, direct.iface.Name, direct.iface.Index, direct.iface.PrimaryIPv4())
					r.Via = direct.iface.Name
				}
				r.Path = path
				c.ab.mu.Lock()
				c.ab.res.Rounds = append(c.ab.res.Rounds, r)
				c.ab.mu.Unlock()
			}
		}
		c.ab.mu.Lock()
		defer c.ab.mu.Unlock()
		res := &c.ab.res
		res.Running, res.Finished = false, time.Now().UnixMilli()
		res.Bonded, res.Direct = summarize(res.Rounds, "bonded"), summarize(res.Rounds, "direct")
		res.Verdict = verdict(res.Bonded, res.Direct)
		c.log.Info("A/B test finished", "bonded_mbps", fmt.Sprintf("%.1f", res.Bonded.Mbps), "direct_mbps", fmt.Sprintf("%.1f", res.Direct.Mbps))
	}()
	return nil
}

func (c *Client) ABResult() ABResult {
	c.ab.mu.Lock()
	defer c.ab.mu.Unlock()
	r := c.ab.res
	r.Rounds = append([]ABRound(nil), r.Rounds...)
	return r
}

// bestPhysical is the healthiest, fastest non-benched link.
func (c *Client) bestPhysical() *clink {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best *clink
	var bestRTT time.Duration
	for _, l := range c.links {
		if !l.link.State().Usable() || l.link.Benched() || l.relayTo.IsValid() {
			continue
		}
		var rtt time.Duration
		l.link.Health(func(h *bond.Health) { rtt = h.RTT.SRTT })
		if best == nil || rtt < bestRTT {
			best, bestRTT = l, rtt
		}
	}
	return best
}

// abDownload fetches url with every socket pinned to one interface.
func abDownload(url, ifName string, ifIndex int, src netip.Addr) ABRound {
	var r ABRound
	d := &net.Dialer{Timeout: 10 * time.Second, Control: platform.BindControl(ifName, ifIndex)}
	if src.IsValid() {
		d.LocalAddr = &net.TCPAddr{IP: src.AsSlice()}
	}
	var connectStart time.Time
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			connectStart = time.Now()
			conn, err := d.DialContext(ctx, "tcp4", addr) // the bond carries IPv4
			if err == nil {
				r.ConnectMs = float64(time.Since(connectStart)) / float64(time.Millisecond)
			}
			return conn, err
		},
		TLSClientConfig:   &tls.Config{},
		DisableKeepAlives: true,
		ForceAttemptHTTP2: false,
	}
	cl := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	start := time.Now()
	resp, err := cl.Get(url)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.TTFBMs = float64(time.Since(start)) / float64(time.Millisecond)
	if resp.StatusCode != http.StatusOK {
		r.Error = resp.Status
		return r
	}
	n, err := io.Copy(io.Discard, resp.Body)
	el := time.Since(start).Seconds()
	r.Bytes, r.Seconds = n, el
	if el > 0 {
		r.Mbps = float64(n) * 8 / el / 1e6
	}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func summarize(rs []ABRound, path string) ABSummary {
	var s ABSummary
	for _, r := range rs {
		if r.Path != path || r.Error != "" {
			continue
		}
		s.OK++
		s.Mbps += r.Mbps
		s.ConnectMs += r.ConnectMs
	}
	if s.OK > 0 {
		s.Mbps /= float64(s.OK)
		s.ConnectMs /= float64(s.OK)
	}
	return s
}

func verdict(b, d ABSummary) string {
	switch {
	case b.OK == 0 && d.OK == 0:
		return "Both paths failed: check the test URL."
	case b.OK == 0:
		return "The bonded path failed every round."
	case d.OK == 0:
		return "The direct path failed every round; bonding is carrying you."
	}
	ratio := b.Mbps / d.Mbps
	switch {
	case ratio >= 1.15:
		return fmt.Sprintf("Bonding is %.0f%% faster than going direct.", (ratio-1)*100)
	case ratio >= 0.85:
		return "Bonding and direct are about the same speed."
	default:
		return fmt.Sprintf("Bonding is %.0f%% slower than going direct. With a single good link, automatic scaling will route you direct.", (1-ratio)*100)
	}
}
