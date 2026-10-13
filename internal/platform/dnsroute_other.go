//go:build !linux

package platform

import "net/netip"

func RouterDNSUpstreams() []netip.Addr { return nil }
