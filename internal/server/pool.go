package server

import "net/netip"

// ipPool hands out client tunnel addresses. The first host address is the
// server's gateway. A device that reconnects gets its previous address back
// when it is still free ("sticky" by client name).
type ipPool struct {
	prefix  netip.Prefix
	gateway netip.Addr
	used    map[netip.Addr]bool
	sticky  map[string]netip.Addr
	next    netip.Addr
}

func newPool(p netip.Prefix) *ipPool {
	p = p.Masked()
	gw := p.Addr().Next()
	return &ipPool{prefix: p, gateway: gw, used: map[netip.Addr]bool{}, sticky: map[string]netip.Addr{}, next: gw.Next()}
}

func (p *ipPool) valid(a netip.Addr) bool {
	if !p.prefix.Contains(a) || a == p.gateway || a == p.prefix.Addr() {
		return false
	}
	// Exclude the broadcast address.
	b := a.As4()
	bits := p.prefix.Bits()
	host := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	mask := uint32(1)<<(32-bits) - 1
	return host&mask != mask
}

func (p *ipPool) alloc(name string) (netip.Addr, bool) {
	if a, ok := p.sticky[name]; ok && name != "" && !p.used[a] {
		p.used[a] = true
		return a, true
	}
	start := p.next
	for {
		a := p.next
		p.next = p.next.Next()
		if !p.prefix.Contains(p.next) {
			p.next = p.gateway.Next()
		}
		if p.valid(a) && !p.used[a] {
			p.used[a] = true
			if name != "" {
				p.sticky[name] = a
			}
			return a, true
		}
		if p.next == start {
			return netip.Addr{}, false
		}
	}
}

func (p *ipPool) free(a netip.Addr) { delete(p.used, a) }
