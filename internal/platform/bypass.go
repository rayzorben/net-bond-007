package platform

import "net/netip"

// Bypass routes: addresses that must leave through one physical link instead
// of the tunnel, whatever the bond is doing (sites that refuse the server's
// data-center address). Host routes are more specific than the tunnel's /1
// routes, and connection pinning lets more-specific main-table routes win,
// so they hold on a router with pinning on.

// BypassSpec is the full desired state; ApplyBypass replaces the previous.
type BypassSpec struct {
	Dev      string
	GW       netip.Addr // invalid: device route
	Prefixes []netip.Prefix
}

// bypassProto marks the routes as ours in the kernel table, so leftovers
// from a crashed process are swept on the next start.
const bypassProto = "77"
