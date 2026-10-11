// Package platform contains the OS-specific pieces: interface discovery and
// classification, binding sockets to an interface, configuring the virtual
// adapter, and installing as a system service.
package platform

import (
	"net"
	"net/netip"
	"sort"
)

// Iface is a candidate network interface.
type Iface struct {
	Name  string         `json:"name"`
	Index int            `json:"index"`
	Kind  string         `json:"kind"` // ethernet | wifi | cellular | usb | bluetooth | vpn | virtual | other
	Desc  string         `json:"desc,omitempty"`
	MAC   string         `json:"mac,omitempty"`
	Up    bool           `json:"up"`
	IPv4  []netip.Prefix `json:"ipv4"`
	IPv6  []netip.Prefix `json:"ipv6,omitempty"`
}

// PrimaryIPv4 returns the first IPv4 address (or invalid).
func (i Iface) PrimaryIPv4() netip.Addr {
	if len(i.IPv4) == 0 {
		return netip.Addr{}
	}
	return i.IPv4[0].Addr()
}

// Discover lists interfaces that are up and not loopback.
func Discover() ([]Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	extra := describe() // OS-specific kind/description hints
	var out []Iface
	for _, ni := range ifs {
		if ni.Flags&net.FlagLoopback != 0 {
			continue
		}
		it := Iface{
			Name:  ni.Name,
			Index: ni.Index,
			MAC:   ni.HardwareAddr.String(),
			Up:    ni.Flags&net.FlagUp != 0 && ni.Flags&net.FlagRunning != 0,
		}
		addrs, _ := ni.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			ones, _ := ipn.Mask.Size()
			p := netip.PrefixFrom(ip, ones)
			if ip.Is4() {
				if !ip.IsLinkLocalUnicast() { // 169.254/16: no DHCP lease
					it.IPv4 = append(it.IPv4, p)
				}
			} else if !ip.IsLinkLocalUnicast() {
				it.IPv6 = append(it.IPv6, p)
			}
		}
		if h, ok := extra[ni.Name]; ok {
			it.Kind, it.Desc = h.kind, h.desc
		}
		if it.Kind == "" {
			it.Kind = kindOf(ni)
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type hint struct{ kind, desc string }

// kindFromName is the portable fallback classifier.
func kindFromName(name string) string {
	n := lower(name)
	switch {
	case hasPrefix(n, "wl"), hasPrefix(n, "wifi"), hasPrefix(n, "wi-fi"), hasPrefix(n, "ath"), hasPrefix(n, "ra"), hasPrefix(n, "apcli"), contains(n, "wireless"):
		return "wifi"
	case hasPrefix(n, "wwan"), hasPrefix(n, "ww"), hasPrefix(n, "rmnet"), hasPrefix(n, "cellular"), hasPrefix(n, "mobile"), hasPrefix(n, "pdp_ip"):
		return "cellular"
	case hasPrefix(n, "usb"), hasPrefix(n, "rndis"), contains(n, "iphone"), contains(n, "android"):
		return "usb"
	case hasPrefix(n, "bnep"), hasPrefix(n, "bt"), contains(n, "bluetooth"):
		return "bluetooth"
	case hasPrefix(n, "tun"), hasPrefix(n, "tap"), hasPrefix(n, "wg"), hasPrefix(n, "utun"), hasPrefix(n, "tailscale"), hasPrefix(n, "zt"), hasPrefix(n, "ppp"):
		return "vpn"
	case hasPrefix(n, "eth"), hasPrefix(n, "en"), hasPrefix(n, "lan"), hasPrefix(n, "wan"), contains(n, "ethernet"):
		return "ethernet"
	case hasPrefix(n, "docker"), hasPrefix(n, "br"), hasPrefix(n, "veth"), hasPrefix(n, "virbr"), hasPrefix(n, "vmnet"), hasPrefix(n, "vboxnet"):
		return "virtual"
	}
	return "other"
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
