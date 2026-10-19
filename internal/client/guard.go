package client

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/sched"
)

// Never slower than direct: bonding exists to beat a single link, so when it
// measurably loses to the best physical link on its own, new connections go
// direct over that link. A download through the bond and one over the link
// are timed back to back a few minutes after bonding starts, at the daily
// test time, and a few minutes after a link's address changes (a new
// network), at most hourly and never during a call (see schedule.go). A link
// dropping out and coming back with the same address is not a reason to
// test. Leaving takes a clear bond win (hysteresis), and a direct link that
// stops working sends traffic back to the bond at once.

const (
	guardURL = "https://speed.cloudflare.com/__down?bytes=25000000"
	// guardFallbackURL is used when either path is refused the first URL
	// (Cloudflare rate-limits busy datacenter addresses, such as a bond
	// server's): both paths must be measured against the same source.
	guardFallbackURL = "https://cachefly.cachefly.net/100mb.test"
	guardRoundLimit  = 6 * time.Second
	guardRounds      = 2
	guardSettle      = 3 * time.Minute // after links change, before testing
	guardEnterRatio  = 0.85            // go direct when bonded < 85% of direct
	guardLeaveRatio  = 1.05            // back to the bond above 105%
	guardFailAfter   = 5 * time.Second // direct link unusable this long: back to the bond
)

type guardState struct {
	// guarded by c.mu
	active    bool
	viaID     uint8
	via       string
	running   bool
	started   bool              // enabled and waiting for or past its first test
	due       string            // why a test is wanted ("" = none)
	nextAt    time.Time         // a wanted test waits until then (settling)
	lastRun   time.Time         // last test started
	addrs     map[string]string // link -> last address seen
	badSince  time.Time
	bonded    float64 // Mbit/s, last test
	direct    float64
	testedAt  time.Time
	lastError string
}

// GuardView is the dashboard's view of the guard.
type GuardView struct {
	Active     bool    `json:"active"`
	Via        string  `json:"via,omitempty"`
	BondedMbps float64 `json:"bondedMbps,omitempty"`
	DirectMbps float64 `json:"directMbps,omitempty"`
	TestedAt   int64   `json:"testedAt,omitempty"`
	NextAt     int64   `json:"nextAt,omitempty"`
	Error      string  `json:"error,omitempty"`
}

func (c *Client) guardEnabled(cfg *config.Client) bool {
	b := cfg.Bonding
	return b.DirectGuard && b.Mode == string(sched.ModeSmart) && b.AutoScale && cfg.Server.Enabled && !c.NoTunnel
}

// guardOverride returns the link the guard sends traffic over, if active.
func (c *Client) guardOverride() (uint8, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.guard.viaID, c.guard.active
}

// guardTick runs once per second: it drops the override when its link fails
// and schedules comparisons.
func (c *Client) guardTick(now time.Time, cfg *config.Client, links []*clink) {
	if !c.guardEnabled(cfg) || c.ack.Load() == nil {
		c.mu.Lock()
		if c.guard.active {
			c.log.Info("never slower than direct: off; traffic returns to the bond")
		}
		c.guard.active, c.guard.started, c.guard.due, c.guard.nextAt = false, false, "", time.Time{}
		c.mu.Unlock()
		return
	}
	var via *clink
	addrs := map[string]string{}
	for _, l := range links {
		if ip := l.iface.PrimaryIPv4(); ip.IsValid() {
			addrs[l.iface.Name] = ip.String()
		}
	}

	c.mu.Lock()
	g := &c.guard
	if g.active {
		for _, l := range links {
			if l.id == g.viaID {
				via = l
			}
		}
		ok := via != nil && via.link.State().Usable()
		switch {
		case ok:
			g.badSince = time.Time{}
		case g.badSince.IsZero():
			g.badSince = now
		case now.Sub(g.badSince) >= guardFailAfter:
			c.log.Warn("never slower than direct: the direct link stopped working; traffic returns to the bond", "via", g.via)
			g.active, g.badSince = false, time.Time{}
		}
	}
	want := func(reason string) {
		if g.due == "" {
			g.due, g.nextAt = reason, now.Add(guardSettle)
		}
	}
	if !g.started {
		g.started = true
		want("bonding started")
	}
	if moved := g.noteAddrs(addrs); len(moved) > 0 {
		want("address changed: " + strings.Join(moved, ", "))
	}
	if g.due == "" && !g.lastRun.IsZero() && dailyDue(g.lastRun, now, cfg.SpeedTest.DailyAt) {
		want("daily")
	}
	due := g.due != "" && !g.running && !now.Before(g.nextAt) &&
		(g.lastRun.IsZero() || now.Sub(g.lastRun) >= minGap(cfg.SpeedTest)) && !c.callActive(now)
	if due {
		c.log.Info("never slower than direct: comparing", "reason", g.due)
		g.running, g.due, g.lastRun = true, "", now
	}
	c.mu.Unlock()
	if due {
		go c.guardTest()
	}
}

// noteAddrs records each link's address and returns the links whose address
// is new: a new network. A link that drops out and returns with the same
// address is not one, and neither are the links present at start.
func (g *guardState) noteAddrs(addrs map[string]string) []string {
	if g.addrs == nil {
		g.addrs = map[string]string{}
	}
	first := len(g.addrs) == 0
	var moved []string
	for name, ip := range addrs {
		if old, seen := g.addrs[name]; !seen || old != ip {
			if !first {
				moved = append(moved, name+" "+ip)
			}
			g.addrs[name] = ip
		}
	}
	slices.Sort(moved)
	return moved
}

// guardTest compares the bond with the best physical link and updates the
// override.
func (c *Client) guardTest() {
	defer func() {
		c.mu.Lock()
		c.guard.running = false
		c.mu.Unlock()
	}()
	c.tunMu.Lock()
	tun := c.tun
	c.tunMu.Unlock()
	direct := c.bestPhysical()
	if tun == nil || direct == nil || c.bypassVia() != "" || c.ABResult().Running {
		return
	}
	if p := c.plan.Load(); p == nil || len(p.Active) < 2 {
		// One link: automatic scaling already goes direct on its own.
		if _, active := c.guardOverride(); !active {
			return
		}
	}
	var tunIP netip.Addr
	if a := c.ack.Load(); a != nil {
		tunIP, _ = netip.ParseAddr(a.ClientIP)
	}
	var rounds []ABRound
	for _, u := range []string{guardURL, guardFallbackURL} {
		rounds = rounds[:0]
		refused := false
		for i := 0; i < guardRounds && !refused; i++ {
			d := abDownload(u, direct.iface.Name, direct.iface.Index, direct.iface.PrimaryIPv4(), guardRoundLimit)
			d.Path = "direct"
			b := abDownload(u, tun.Name(), tun.Index(), tunIP, guardRoundLimit)
			b.Path = "bonded"
			refused = refusedStatus(d.Error) || refusedStatus(b.Error)
			rounds = append(rounds, d, b)
		}
		if !refused {
			break
		}
		// A refusal says nothing about speed: never let it count as "the
		// bond is slower" (that sent the whole LAN direct). Try the next
		// source; if every source refuses, the comparison is skipped below.
		rounds = rounds[:0]
	}
	bs, ds := summarize(rounds, "bonded"), summarize(rounds, "direct")

	c.mu.Lock()
	defer c.mu.Unlock()
	g := &c.guard
	g.testedAt, g.bonded, g.direct, g.lastError = time.Now(), bs.Mbps, ds.Mbps, ""
	if ds.OK == 0 {
		g.lastError = "the direct download failed"
		return // can't claim direct is better
	}
	ratio := 0.0
	if bs.OK > 0 && ds.Mbps > 0 {
		ratio = bs.Mbps / ds.Mbps
	}
	switch {
	case !g.active && ratio < guardEnterRatio:
		g.active, g.viaID, g.via, g.badSince = true, direct.id, direct.iface.Name, time.Time{}
		c.log.Warn("never slower than direct: the bond is slower; new connections go direct", "via", g.via,
			"bonded_mbps", fmt.Sprintf("%.1f", bs.Mbps), "direct_mbps", fmt.Sprintf("%.1f", ds.Mbps))
	case g.active && ratio >= guardLeaveRatio:
		g.active = false
		c.log.Info("never slower than direct: the bond is faster again; new connections use it",
			"bonded_mbps", fmt.Sprintf("%.1f", bs.Mbps), "direct_mbps", fmt.Sprintf("%.1f", ds.Mbps))
	case g.active && direct.id != g.viaID:
		g.viaID, g.via = direct.id, direct.iface.Name
	default:
		c.log.Info("never slower than direct: compared", "direct", g.active,
			"bonded_mbps", fmt.Sprintf("%.1f", bs.Mbps), "direct_mbps", fmt.Sprintf("%.1f", ds.Mbps))
	}
}

// refusedStatus reports whether a download error is the server refusing
// (an HTTP 4xx status such as 429 rate limiting) rather than the path failing.
func refusedStatus(err string) bool {
	return len(err) >= 3 && err[0] == '4' && err[1] >= '0' && err[1] <= '9' && err[2] >= '0' && err[2] <= '9'
}

// applyGuard turns the plan direct while the guard is active.
func (c *Client) applyGuard(p *bond.Plan) {
	id, active := c.guardOverride()
	if !active || p.Direct {
		return
	}
	for _, a := range p.Active {
		if a == id {
			p.Direct, p.DirectVia, p.Mode = true, id, "direct"
			return
		}
	}
}

func (c *Client) guardView() *GuardView {
	c.mu.Lock()
	defer c.mu.Unlock()
	g := c.guard
	if g.testedAt.IsZero() && !g.active {
		return nil
	}
	v := &GuardView{Active: g.active, BondedMbps: g.bonded, DirectMbps: g.direct, Error: g.lastError}
	if g.active {
		v.Via = g.via
	}
	if !g.testedAt.IsZero() {
		v.TestedAt = g.testedAt.UnixMilli()
	}
	sc := c.cfg.Get().SpeedTest
	next := nextDaily(time.Now(), sc.DailyAt)
	if g.due != "" {
		next = g.nextAt
		if gap := g.lastRun.Add(minGap(sc)); !g.lastRun.IsZero() && gap.After(next) {
			next = gap
		}
	}
	if !next.IsZero() {
		v.NextAt = next.UnixMilli()
	}
	return v
}
