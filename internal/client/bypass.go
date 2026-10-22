package client

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// Bypass: hosts that must never be reached through the bond. Ticket sellers
// and other sites behind bot checks refuse or challenge the server's
// data-center address, so their traffic leaves straight out of a physical
// link instead.
//
// Names become addresses two ways. DNS answers coming back through the
// tunnel are read as they pass (a LAN device's lookup via the router's
// resolver, or the PC's own when DNS is bonded): an answer whose question,
// CNAME chain or record owner falls under a bypassed name puts every A
// record into the set, before the answer is delivered, so the connection
// that follows already goes direct. That catches wildcards and CDN hosts.
// Exact names are also looked up now and then, so an entry works even when
// the answer is cached somewhere and never crosses the tunnel again.
// Addresses stay bypassed for a day after they were last seen.

const (
	bypassHold          = 24 * time.Hour
	bypassPrefetchEvery = 10 * time.Minute
	bypassMaxAddrs      = 4096
)

type bypassAddr struct {
	host string
	how  string // dns | lookup
	seen time.Time
}

type bypassState struct {
	rules atomic.Pointer[[]config.Bypass] // enabled domain entries, for the packet path

	mu         sync.Mutex
	addrs      map[netip.Addr]*bypassAddr
	prefetchAt time.Time
	resolving  bool
	gwDev      string // DefaultGateway cache
	gw         netip.Addr
	gwAt       time.Time

	// Applied state (guarded by tunMu, like the tunnel).
	last string
	on   bool
	via  string
	err  string
}

// bypassConfig reloads the rule set after a settings change.
func (c *Client) bypassConfig(cfg *config.Client) {
	var rules []config.Bypass
	for _, b := range cfg.Bypass {
		if !b.Disabled && !b.Prefix().IsValid() {
			rules = append(rules, b)
		}
	}
	c.bypass.rules.Store(&rules)
	c.bypass.mu.Lock()
	for a, v := range c.bypass.addrs {
		if !slices.ContainsFunc(rules, func(b config.Bypass) bool { return config.NormalizeHost(b.Host) == v.host }) {
			delete(c.bypass.addrs, a)
		}
	}
	c.bypass.prefetchAt = time.Time{} // look the names up now
	c.bypass.mu.Unlock()
}

// bypassSnoop reads one packet on its way from the tunnel to the OS: a DNS
// answer naming a bypassed host adds its addresses to the set.
func (c *Client) bypassSnoop(pkt []byte) {
	rp := c.bypass.rules.Load()
	if rp == nil || len(*rp) == 0 {
		return
	}
	payload, ok := dnsPayload(pkt)
	if !ok {
		return
	}
	hits := matchDNS(*rp, payload)
	if len(hits) == 0 {
		return
	}
	now := time.Now()
	c.bypass.mu.Lock()
	for a, host := range hits {
		c.bypassAddLocked(a, host, "dns", now)
	}
	c.bypass.mu.Unlock()
}

func (c *Client) bypassAddLocked(a netip.Addr, host, how string, now time.Time) {
	if c.bypass.addrs == nil {
		c.bypass.addrs = map[netip.Addr]*bypassAddr{}
	}
	if v := c.bypass.addrs[a]; v != nil {
		v.seen, v.host = now, host
		if how == "dns" {
			v.how = how
		}
		return
	}
	if len(c.bypass.addrs) >= bypassMaxAddrs {
		var oldest netip.Addr
		var t time.Time
		for k, v := range c.bypass.addrs {
			if t.IsZero() || v.seen.Before(t) {
				oldest, t = k, v.seen
			}
		}
		delete(c.bypass.addrs, oldest)
	}
	c.bypass.addrs[a] = &bypassAddr{host: host, how: how, seen: now}
}

// dnsPayload returns the DNS message inside an IPv4/UDP packet from port 53.
func dnsPayload(pkt []byte) ([]byte, bool) {
	if len(pkt) < 28 || pkt[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || pkt[9] != 17 || len(pkt) < ihl+8 {
		return nil, false
	}
	if binary.BigEndian.Uint16(pkt[6:8])&0x1fff != 0 { // fragment
		return nil, false
	}
	udp := pkt[ihl:]
	if binary.BigEndian.Uint16(udp[0:2]) != 53 {
		return nil, false
	}
	n := int(binary.BigEndian.Uint16(udp[4:6]))
	if n < 8+12 || n > len(udp) {
		return nil, false
	}
	return udp[8:n], true
}

// matchDNS returns the A records of a DNS answer that falls under one of the
// rules, keyed by address, with the rule's host as value.
func matchDNS(rules []config.Bypass, msg []byte) map[netip.Addr]string {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response {
		return nil
	}
	rule := func(name string) string {
		for _, r := range rules {
			if r.Matches(name) {
				return config.NormalizeHost(r.Host)
			}
		}
		return ""
	}
	matched := ""
	for {
		q, err := p.Question()
		if err != nil {
			break
		}
		if matched == "" {
			matched = rule(q.Name.String())
		}
	}
	var addrs []netip.Addr
	for {
		rh, err := p.AnswerHeader()
		if err != nil {
			break
		}
		if matched == "" {
			matched = rule(rh.Name.String())
		}
		switch rh.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return nil
			}
			addrs = append(addrs, netip.AddrFrom4(r.A))
		case dnsmessage.TypeCNAME:
			r, err := p.CNAMEResource()
			if err != nil {
				return nil
			}
			if matched == "" {
				matched = rule(r.CNAME.String())
			}
		default:
			if err := p.SkipAnswer(); err != nil {
				return nil
			}
		}
	}
	if matched == "" || len(addrs) == 0 {
		return nil
	}
	out := make(map[netip.Addr]string, len(addrs))
	for _, a := range addrs {
		if !a.IsPrivate() && !a.IsLoopback() && !a.IsUnspecified() {
			out[a] = matched
		}
	}
	return out
}

// bypassPrefetch looks the exact (non-wildcard) names up through the system
// resolver, in the background, so an entry takes effect before the first
// lookup crosses the tunnel.
func (c *Client) bypassPrefetch(rules []config.Bypass) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range rules {
		host := config.NormalizeHost(r.Host)
		if strings.HasPrefix(host, "*.") {
			continue
		}
		addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		if err != nil {
			continue
		}
		now := time.Now()
		c.bypass.mu.Lock()
		for _, a := range addrs {
			a = a.Unmap()
			if a.Is4() && !a.IsPrivate() && !a.IsLoopback() {
				c.bypassAddLocked(a, host, "lookup", now)
			}
		}
		c.bypass.mu.Unlock()
	}
	c.bypass.mu.Lock()
	c.bypass.resolving = false
	c.bypass.mu.Unlock()
}

// bypassTick runs once per second: expires old addresses, schedules
// lookups and keeps the kernel routes in line with the set and the best way
// out.
func (c *Client) bypassTick(now time.Time, cfg *config.Client) {
	rules := c.bypass.rules.Load()
	if rules == nil {
		c.bypassConfig(cfg)
		rules = c.bypass.rules.Load()
	}
	c.bypass.mu.Lock()
	for a, v := range c.bypass.addrs {
		if now.Sub(v.seen) > bypassHold {
			delete(c.bypass.addrs, a)
		}
	}
	if len(*rules) > 0 && !c.bypass.resolving && now.Sub(c.bypass.prefetchAt) >= bypassPrefetchEvery {
		c.bypass.resolving, c.bypass.prefetchAt = true, now
		go c.bypassPrefetch(*rules)
	}
	c.bypass.mu.Unlock()

	if !c.tunMu.TryLock() {
		return
	}
	defer c.tunMu.Unlock()
	c.refreshBypassLocked(cfg)
}

// bypassWanted: there is something to keep out of the bond, and the tunnel
// is up to keep it out of.
func (c *Client) bypassWanted(cfg *config.Client) bool {
	if c.tun == nil || c.NoTunnel {
		return false
	}
	for _, b := range cfg.Bypass {
		if !b.Disabled {
			return true
		}
	}
	return false
}

// refreshBypassLocked installs the routes. c.tunMu must be held.
func (c *Client) refreshBypassLocked(cfg *config.Client) {
	if !c.bypassWanted(cfg) {
		c.clearBypassLocked()
		return
	}
	if !platform.BypassSupported() {
		c.bypass.err = "bypass routes are only supported on Linux"
		return
	}
	dev := c.tunSpec.DirectDev
	if !c.tunSpec.Direct {
		dev = ""
		if l := c.bestPhysical(); l != nil {
			dev = l.iface.Name
		}
	}
	if dev == "" {
		// No link can carry it: keep whatever is installed, it may come back.
		return
	}
	spec := platform.BypassSpec{Dev: dev, GW: c.bypassGateway(dev, time.Now())}
	c.bypass.mu.Lock()
	for a := range c.bypass.addrs {
		spec.Prefixes = append(spec.Prefixes, netip.PrefixFrom(a, 32))
	}
	c.bypass.mu.Unlock()
	for _, b := range cfg.Bypass {
		if p := b.Prefix(); !b.Disabled && p.IsValid() && p.Addr().Is4() {
			spec.Prefixes = append(spec.Prefixes, p)
		}
	}
	sort.Slice(spec.Prefixes, func(i, j int) bool { return spec.Prefixes[i].Addr().Less(spec.Prefixes[j].Addr()) })
	sig := fmt.Sprintf("%s %s %v", spec.Dev, spec.GW, spec.Prefixes)
	if c.bypass.on && sig == c.bypass.last {
		return
	}
	err := platform.ApplyBypass(spec)
	wasVia, wasN := c.bypass.via, strings.Count(c.bypass.last, "/")
	c.bypass.on, c.bypass.last, c.bypass.via = true, sig, dev
	if err != nil {
		c.bypass.err = err.Error()
		c.log.Error("cannot install bypass routes", "via", dev, "err", err)
		return
	}
	c.bypass.err = ""
	if wasVia != dev || wasN != len(spec.Prefixes) {
		c.log.Info("bypass routes installed", "via", dev, "gateway", spec.GW, "addresses", len(spec.Prefixes))
	}
}

func (c *Client) clearBypassLocked() {
	if c.bypass.on {
		platform.ClearBypass()
		c.bypass.on, c.bypass.last, c.bypass.via = false, "", ""
	}
}

// bypassGateway caches DefaultGateway (it runs a command) per device.
func (c *Client) bypassGateway(dev string, now time.Time) netip.Addr {
	c.bypass.mu.Lock()
	defer c.bypass.mu.Unlock()
	if c.bypass.gwDev != dev || now.Sub(c.bypass.gwAt) > 30*time.Second {
		c.bypass.gwDev, c.bypass.gw, c.bypass.gwAt = dev, platform.DefaultGateway(dev), now
	}
	return c.bypass.gw
}

// ---------------------------------------------------------------- dashboard

// BypassRuleView is one entry as the dashboard shows it.
type BypassRuleView struct {
	config.Bypass
	Addrs int   `json:"addrs"`          // addresses currently routed for it
	Seen  int64 `json:"seen,omitempty"` // last time one was seen (unix ms)
}

type BypassAddrView struct {
	IP   string `json:"ip"`
	Host string `json:"host"`
	How  string `json:"how"` // dns | lookup
	Seen int64  `json:"seen"`
}

type BypassView struct {
	Rules []BypassRuleView `json:"rules"`
	// Version identifies the list; PUT with If-Match checks it.
	Version   string           `json:"version"`
	Via       string           `json:"via,omitempty"` // link carrying bypassed traffic
	Supported bool             `json:"supported"`
	Problem   string           `json:"problem,omitempty"`
	Active    []BypassAddrView `json:"active"`
}

func (c *Client) Bypasses() BypassView {
	cfg := c.cfg.Get()
	v := BypassView{Rules: []BypassRuleView{}, Active: []BypassAddrView{}, Supported: platform.BypassSupported(), Version: bypassVersion(cfg.Bypass)}
	c.tunMu.Lock()
	tunUp, via, berr := c.tun != nil, c.bypass.via, c.bypass.err
	c.tunMu.Unlock()
	v.Via = via
	switch {
	case !v.Supported:
		v.Problem = "Bypassing needs a Linux client or router."
	case !cfg.Server.Enabled:
		v.Problem = "Bonding is off, so everything already goes direct."
	case !tunUp:
		v.Problem = "The tunnel is down, so everything already goes direct."
	case berr != "":
		v.Problem = "The bypass routes failed: " + berr
	}
	count := map[string]int{}
	seen := map[string]time.Time{}
	c.bypass.mu.Lock()
	for a, b := range c.bypass.addrs {
		count[b.host]++
		if b.seen.After(seen[b.host]) {
			seen[b.host] = b.seen
		}
		v.Active = append(v.Active, BypassAddrView{IP: a.String(), Host: b.host, How: b.how, Seen: b.seen.UnixMilli()})
	}
	c.bypass.mu.Unlock()
	sort.Slice(v.Active, func(i, j int) bool {
		if v.Active[i].Host != v.Active[j].Host {
			return v.Active[i].Host < v.Active[j].Host
		}
		return v.Active[i].IP < v.Active[j].IP
	})
	for _, b := range cfg.Bypass {
		h := config.NormalizeHost(b.Host)
		rv := BypassRuleView{Bypass: b, Addrs: count[h]}
		if b.Prefix().IsValid() && !b.Disabled {
			rv.Addrs = 1
		}
		if t := seen[h]; !t.IsZero() {
			rv.Seen = t.UnixMilli()
		}
		v.Rules = append(v.Rules, rv)
	}
	return v
}

var errBypassStale = errors.New("The bypass list was changed somewhere else. The list has been reloaded; try again.")

func bypassVersion(bs []config.Bypass) string {
	b, _ := json.Marshal(bs)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}
