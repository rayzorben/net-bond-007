package platform

import "net/netip"

// Port forwarding (router mode): the server relays connections from public
// ports into the tunnel, addressed to the tunnel IP. The router hands them
// on to a LAN device (DNAT) and makes sure the device's replies go back
// through the tunnel, even while other LAN traffic goes direct.

type PortMapSpec struct {
	Tun   string     // tunnel device
	TunIP netip.Addr // tunnel address the server sends to
	LAN   []string   // LAN devices (outbound from a forwarded port is pinned to the tunnel)
	Rules []PortMapRule
}

type PortMapRule struct {
	Proto      string // tcp or udp
	Port       uint16 // public port = port on the tunnel address
	Device     netip.Addr
	DevicePort uint16
}

// Packet/conntrack mark for forwarded connections (pinning uses bits 24-27,
// GL.iNet 0xf000, Tailscale 0xff0400).
const (
	portMapMark  = 0x10000000
	portMapPref  = 5898 // ahead of pinning (5899+) and GL.iNet's rules
	portMapTable = 7090
)
