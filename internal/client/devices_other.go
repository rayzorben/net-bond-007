//go:build !linux

package client

import "net/netip"

func lanOrigins() map[natKey]netip.AddrPort { return nil }
func lanNames() map[string]Device           { return nil }
