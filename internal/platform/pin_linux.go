//go:build linux

package platform

import (
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// Rule preferences and tables for pinning. GL.iNet's own policy rules run at
// 6000-9920 and must not see pinned packets first; 7007 is the DNS table.
const (
	pinSuppressPref = 5899 // more-specific main routes (LAN, tailnet) still win
	pinRulePref     = 5900 // + path index
	pinTable        = 7100 // + path index
	pinNftTable     = "net_bond_007_pin"
)

var pinMu sync.Mutex
var pinApplied = map[int]bool{} // path indexes with a rule installed

func PinsSupported() bool { return have("nft") && have("ip") }

// PinNftRules is the ruleset for one pinning state.
func PinNftRules(s PinSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %s {\n", pinNftTable)
	b.WriteString("  chain pin {\n    type filter hook prerouting priority -155; policy accept;\n")
	if len(s.LAN) > 0 && s.Current >= 1 && s.Current <= MaxPinPaths {
		quoted := make([]string, len(s.LAN))
		for i, d := range s.LAN {
			quoted[i] = fmt.Sprintf("%q", d)
		}
		fmt.Fprintf(&b, "    iifname { %s } ct state new ct mark & 0x%08x == 0 ct mark set ct mark & 0x%08x | 0x%08x\n",
			strings.Join(quoted, ", "), pinMask, ^uint32(pinMask), pinMark(s.Current))
	}
	for _, p := range s.Paths {
		fmt.Fprintf(&b, "    ct mark & 0x%08x == 0x%08x meta mark set meta mark & 0x%08x | 0x%08x\n",
			pinMask, pinMark(p.Index), ^uint32(pinMask), pinMark(p.Index))
	}
	b.WriteString("  }\n")
	b.WriteString("  chain pin_dead {\n    type filter hook forward priority -5; policy accept;\n")
	for _, p := range s.Paths {
		if !p.Dead {
			continue
		}
		m := pinMark(p.Index)
		fmt.Fprintf(&b, "    ct mark & 0x%08x == 0x%08x meta l4proto tcp reject with tcp reset\n", pinMask, m)
		fmt.Fprintf(&b, "    ct mark & 0x%08x == 0x%08x reject\n", pinMask, m)
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// ApplyPins installs s, replacing the previous state. Routing tables are
// replaced in place and the nft table atomically, so pinned connections never
// see a gap.
func ApplyPins(s PinSpec) error {
	pinMu.Lock()
	defer pinMu.Unlock()
	want := map[int]bool{}
	for _, p := range s.Paths {
		if p.Index < 1 || p.Index > MaxPinPaths {
			return fmt.Errorf("pin path index %d out of range", p.Index)
		}
		want[p.Index] = true
		t := fmt.Sprint(pinTable + p.Index)
		var err error
		if p.GW.IsValid() {
			err = run("ip", "route", "replace", "default", "via", p.GW.String(), "dev", p.Dev, "table", t)
		} else {
			err = run("ip", "route", "replace", "default", "dev", p.Dev, "table", t)
		}
		if err != nil {
			// The device is gone: its connections fall through to the main
			// table (the current path) and, if dead, are reset below.
			run("ip", "route", "flush", "table", t)
		}
		if !pinApplied[p.Index] {
			pref := fmt.Sprint(pinRulePref + p.Index)
			for run("ip", "rule", "del", "pref", pref) == nil {
			}
			if err := run("ip", "rule", "add", "pref", pref, "fwmark", fmt.Sprintf("0x%x/0x%x", pinMark(p.Index), pinMask), "lookup", t); err != nil {
				return err
			}
			pinApplied[p.Index] = true
		}
	}
	if len(want) > 0 {
		if out, _ := exec.Command("ip", "rule", "show", "pref", fmt.Sprint(pinSuppressPref)).Output(); len(strings.TrimSpace(string(out))) == 0 {
			if err := run("ip", "rule", "add", "pref", fmt.Sprint(pinSuppressPref), "lookup", "main", "suppress_prefixlength", "1"); err != nil {
				return err
			}
		}
	}
	// Ensure-then-delete-then-create in one transaction: atomic replace.
	script := fmt.Sprintf("table ip %[1]s\ndelete table ip %[1]s\n", pinNftTable) + PinNftRules(s)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %v: %s", err, out)
	}
	for idx := range pinApplied {
		if !want[idx] {
			removePinPath(idx)
		}
	}
	return nil
}

// ClearPins removes all pinning; pinned connections fall back to the main
// routing table.
func ClearPins() {
	pinMu.Lock()
	defer pinMu.Unlock()
	run("nft", "delete", "table", "ip", pinNftTable)
	idxs := make([]int, 0, len(pinApplied))
	for idx := range pinApplied {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	for _, idx := range idxs {
		removePinPath(idx)
	}
	for run("ip", "rule", "del", "pref", fmt.Sprint(pinSuppressPref)) == nil {
	}
}

func removePinPath(idx int) {
	for run("ip", "rule", "del", "pref", fmt.Sprint(pinRulePref+idx)) == nil {
	}
	run("ip", "route", "flush", "table", fmt.Sprint(pinTable+idx))
	delete(pinApplied, idx)
}

// LANDevices are the router's LAN-side devices: up bridges and lan* ports
// with an IPv4 address (br-lan on OpenWrt).
func LANDevices() []string {
	nis, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ni := range nis {
		if ni.Flags&net.FlagUp == 0 || !(strings.HasPrefix(ni.Name, "br-") || strings.HasPrefix(ni.Name, "lan")) {
			continue
		}
		addrs, _ := ni.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, ni.Name)
				break
			}
		}
	}
	return out
}
