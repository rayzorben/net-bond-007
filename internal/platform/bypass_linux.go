//go:build linux

package platform

import (
	"net/netip"
	"sync"
)

var bypassMu sync.Mutex
var bypassHave = map[netip.Prefix]string{} // installed prefix -> "dev gw"
var bypassSwept bool

func BypassSupported() bool { return have("ip") }

// ApplyBypass brings the kernel's main table in line with s: new prefixes
// get a route, ones no longer wanted (or whose way out changed) are replaced
// or removed. Only the differences touch the kernel.
func ApplyBypass(s BypassSpec) error {
	bypassMu.Lock()
	defer bypassMu.Unlock()
	if !bypassSwept {
		run("ip", "route", "flush", "proto", bypassProto)
		bypassSwept = true
	}
	way := s.Dev + " " + s.GW.String()
	want := map[netip.Prefix]bool{}
	var firstErr error
	for _, p := range s.Prefixes {
		want[p] = true
		if bypassHave[p] == way {
			continue
		}
		args := []string{"route", "replace", p.String()}
		if s.GW.IsValid() {
			args = append(args, "via", s.GW.String())
		}
		args = append(args, "dev", s.Dev, "proto", bypassProto)
		if err := run("ip", args...); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			delete(bypassHave, p)
			continue
		}
		bypassHave[p] = way
	}
	for p := range bypassHave {
		if !want[p] {
			run("ip", "route", "del", p.String(), "proto", bypassProto)
			delete(bypassHave, p)
		}
	}
	return firstErr
}

// ClearBypass removes every bypass route.
func ClearBypass() {
	bypassMu.Lock()
	defer bypassMu.Unlock()
	run("ip", "route", "flush", "proto", bypassProto)
	clear(bypassHave)
}
