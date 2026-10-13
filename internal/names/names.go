// Package names caches reverse-DNS names for addresses shown on the
// Connections dashboard. Lookups run in the background so the API never
// blocks on DNS.
package names

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type entry struct {
	name string
	at   time.Time
}

type Cache struct {
	mu       sync.Mutex
	m        map[netip.Addr]entry
	inflight int
	Resolver *net.Resolver
}

const (
	ttl         = 10 * time.Minute
	maxInflight = 8
	maxEntries  = 4096
)

// Lookup returns the cached name for ip ("" if unknown yet) and starts a
// background lookup when needed.
func (c *Cache) Lookup(ip netip.Addr) string {
	if !ip.IsValid() || ip.IsPrivate() || ip.IsLoopback() {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[netip.Addr]entry{}
	}
	e, ok := c.m[ip]
	if ok && time.Since(e.at) < ttl {
		return e.name
	}
	if c.inflight >= maxInflight || len(c.m) >= maxEntries {
		return e.name
	}
	c.inflight++
	c.m[ip] = entry{name: e.name, at: time.Now()} // don't re-query while in flight
	go c.resolve(ip)
	return e.name
}

func (c *Cache) resolve(ip netip.Addr) {
	r := c.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var name string
	if ns, err := r.LookupAddr(ctx, ip.String()); err == nil && len(ns) > 0 {
		name = strings.TrimSuffix(ns[0], ".")
	}
	c.mu.Lock()
	c.m[ip] = entry{name: name, at: time.Now()}
	c.inflight--
	c.mu.Unlock()
}
