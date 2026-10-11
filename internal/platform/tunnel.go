package platform

import "net/netip"

// TunnelSpec is everything needed to configure the virtual adapter.
type TunnelSpec struct {
	Name       string
	Index      int // OS interface index, when known
	Addr       netip.Prefix
	Gateway    netip.Addr
	MTU        int
	Routes     []netip.Prefix
	DNS        netip.Addr // invalid = leave DNS alone
	RouterMode bool
}

// FullTunnelRoutes cover all of IPv4 with two /1s, which win over the default
// route without replacing it (so it is restored automatically on exit).
var FullTunnelRoutes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/1"),
	netip.MustParsePrefix("128.0.0.0/1"),
}
