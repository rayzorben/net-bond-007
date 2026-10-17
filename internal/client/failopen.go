package client

import (
	"runtime"
	"sort"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// Fail-open: once the tunnel is up, its routes send everything into it, so a
// dead server would take the whole network offline. When no link has heard
// from the server for a few seconds, traffic is routed straight out of the
// best physical link instead. The links keep probing the server over their
// interface-bound sockets, and bonding resumes once one answers steadily.

// failOpenRecover is how long a link must keep answering before traffic goes
// back into the tunnel, so a flapping server doesn't flap the routes.
const failOpenRecover = 30 * time.Second

type failOpen struct {
	via        string    // interface carrying traffic directly; "" = bonding normally
	deadSince  time.Time // no usable link since (zero while one is usable)
	aliveSince time.Time // some link usable since (zero while none is)
}

// bypassVia is the interface fail-open routes through, or "".
func (c *Client) bypassVia() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fail.via
}

// failOpenTick runs once per second and enters or leaves the bypass.
func (c *Client) failOpenTick(now time.Time, cfg *config.Client, links []*clink) {
	alive := false
	for _, l := range links {
		if l.link.State().Usable() {
			alive = true
			break
		}
	}
	c.mu.Lock()
	f := &c.fail
	if !cfg.Server.Enabled || c.ack.Load() == nil {
		// No session: silence now says nothing about the server. Without
		// this, turning bonding on after a while off counted the whole off
		// time as silence and bypassed the tunnel the moment it came up.
		f.deadSince, f.aliveSince = time.Time{}, time.Time{}
	}
	if alive {
		f.deadSince = time.Time{}
		if f.aliveSince.IsZero() {
			f.aliveSince = now
		}
	} else {
		f.aliveSince = time.Time{}
		if f.deadSince.IsZero() {
			f.deadSince = now
		}
	}
	prev, want := f.via, f.via
	var cands []bypassCandidate
	switch {
	case !cfg.Tunnel.FailOpen || !cfg.Server.Enabled || c.ack.Load() == nil:
		want = "" // no tunnel to bypass, or the user turned it off
	case alive:
		if f.via != "" && now.Sub(f.aliveSince) >= failOpenRecover {
			want = ""
		}
	case f.via == "" && now.Sub(f.deadSince) >= time.Duration(cfg.Tunnel.FailOpenSec)*time.Second,
		f.via != "" && !c.bondableLocked(cfg, f.via):
		// Server silent long enough, or the bypass interface went away.
		cands = c.bypassCandidatesLocked(cfg, links)
	}
	c.mu.Unlock()

	if cands != nil {
		for i := range cands {
			cands[i].gateway = platform.DefaultGateway(cands[i].name).IsValid()
		}
		want = pickBypass(cands, runtime.GOOS == "linux")
	}
	if want == prev {
		return
	}
	c.mu.Lock()
	c.fail.via = want
	c.mu.Unlock()
	switch {
	case want == "":
		c.log.Info("fail-open: server is answering again; traffic returns to the bond")
	case prev == "":
		c.log.Warn("fail-open: server unreachable on every link; bypassing the tunnel", "via", want, "silentFor", now.Sub(f.deadSince).Round(time.Second))
	default:
		c.log.Warn("fail-open: bypass interface changed", "from", prev, "to", want)
	}
	go c.applyTunnel()
}

// bondableLocked reports whether a physical interface is still present and
// eligible for bonding. c.mu must be held.
func (c *Client) bondableLocked(cfg *config.Client, name string) bool {
	for _, it := range c.ifaces {
		if it.Name == name {
			return c.eligibility(cfg, it) == "bonded"
		}
	}
	return false
}

func (c *Client) bypassCandidatesLocked(cfg *config.Client, links []*clink) []bypassCandidate {
	byName := map[string]*clink{}
	for _, l := range links {
		byName[l.iface.Name] = l
	}
	cands := []bypassCandidate{}
	for _, it := range c.ifaces { // physical interfaces only; phone relays can't carry direct traffic
		if c.eligibility(cfg, it) != "bonded" {
			continue
		}
		lc := cfg.Links[it.Name]
		bc := bypassCandidate{name: it.Name, kind: it.Kind, backup: lc.Backup, metered: lc.Metered}
		if l, ok := byName[it.Name]; ok {
			l.link.Health(func(h *bond.Health) { bc.rel = h.Reliability })
		}
		cands = append(cands, bc)
	}
	return cands
}

// bypassCandidate is a physical interface fail-open could route through.
type bypassCandidate struct {
	name    string
	kind    string
	gateway bool    // has its own default gateway
	backup  bool    // user marked it backup-only
	metered bool    // capped data plan
	rel     float64 // reliability the bond measured before the server went silent
}

var bypassKindRank = map[string]int{"ethernet": 0, "usb": 1, "wifi": 2, "cellular": 3}

// pickBypass chooses the interface to route through: one with a gateway,
// primary over backup, unmetered over metered, then the one that was most
// reliable, then wired over wireless. Without a gateway an interface is used
// only where a device route alone works (Linux point-to-point links).
func pickBypass(cands []bypassCandidate, deviceRoutesOK bool) string {
	cs := make([]bypassCandidate, 0, len(cands))
	for _, c := range cands {
		if c.gateway || deviceRoutesOK {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return ""
	}
	kind := func(k string) int {
		if r, ok := bypassKindRank[k]; ok {
			return r
		}
		return len(bypassKindRank)
	}
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		switch {
		case a.gateway != b.gateway:
			return a.gateway
		case a.backup != b.backup:
			return !a.backup
		case a.metered != b.metered:
			return !a.metered
		case a.rel != b.rel:
			return a.rel > b.rel
		case kind(a.kind) != kind(b.kind):
			return kind(a.kind) < kind(b.kind)
		}
		return a.name < b.name
	})
	return cs[0].name
}
