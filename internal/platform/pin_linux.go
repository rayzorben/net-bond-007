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
var pinsActive bool             // ApplyPins has run since the last ClearPins

func PinsSupported() bool { return have("nft") && have("ip") }

// pinStickyTimeout is how long a LAN UDP socket stays tied to its path
// after its last packet (RFC 4787 asks NAT mappings to last at least 2 min).
const pinStickyTimeout = "300s"

// PinNftRules is the ruleset for one pinning state. It is a merge, not a
// replacement: the table, chains and sticky sets are created if missing, the
// chains are flushed and refilled, and the sets keep their contents across
// switches.
//
// UDP is pinned per LAN socket (source ip:port), not per connection: once a
// socket's first flow picks a path, every later flow from it follows, while
// it stays active. Otherwise one socket would leave from two public
// addresses (STUN on a call port via the tunnel, the peer flow direct), and
// peer-to-peer hole punching (Tailscale, WebRTC) breaks.
func PinNftRules(s PinSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %s {\n", pinNftTable)
	for i := 1; i <= MaxPinPaths; i++ {
		fmt.Fprintf(&b, "  set usrc_%d { type ipv4_addr . inet_service; flags dynamic, timeout; timeout %s; size 65535; }\n", i, pinStickyTimeout)
	}
	b.WriteString("  chain pin { type filter hook prerouting priority -155; policy accept; }\n")
	b.WriteString("  chain pin_dead { type filter hook forward priority -5; policy accept; }\n")
	b.WriteString("}\n")
	fmt.Fprintf(&b, "flush chain ip %[1]s pin\nflush chain ip %[1]s pin_dead\n", pinNftTable)
	live := map[int]bool{}
	for _, p := range s.Paths {
		if p.Index >= 1 && p.Index <= MaxPinPaths && !p.Dead {
			live[p.Index] = true
		}
	}
	for i := 1; i <= MaxPinPaths; i++ {
		if !live[i] {
			fmt.Fprintf(&b, "flush set ip %s usrc_%d\n", pinNftTable, i)
		}
	}
	rule := func(chain, format string, a ...any) {
		fmt.Fprintf(&b, "add rule ip %s %s ", pinNftTable, chain)
		fmt.Fprintf(&b, format, a...)
		b.WriteString("\n")
	}
	var lan string
	if len(s.LAN) > 0 && s.Current >= 1 && s.Current <= MaxPinPaths {
		quoted := make([]string, len(s.LAN))
		for i, d := range s.LAN {
			quoted[i] = fmt.Sprintf("%q", d)
		}
		lan = strings.Join(quoted, ", ")
		for _, p := range s.Paths {
			if live[p.Index] {
				rule("pin", "iifname { %s } ct state new ct mark & 0x%08x == 0 ip saddr . udp sport @usrc_%d ct mark set ct mark & 0x%08x | 0x%08x",
					lan, pinMask, p.Index, ^uint32(pinMask), pinMark(p.Index))
			}
		}
		if s.Realtime != "" && s.RealtimePath >= 1 && s.RealtimePath <= MaxPinPaths && s.RealtimePath != s.Current {
			for _, dir := range []string{"dport", "sport"} {
				rule("pin", "iifname { %s } ct state new ct mark & 0x%08x == 0 udp %s { %s } ct mark set ct mark & 0x%08x | 0x%08x",
					lan, pinMask, dir, s.Realtime, ^uint32(pinMask), pinMark(s.RealtimePath))
			}
		}
		rule("pin", "iifname { %s } ct state new ct mark & 0x%08x == 0 ct mark set ct mark & 0x%08x | 0x%08x",
			lan, pinMask, ^uint32(pinMask), pinMark(s.Current))
	}
	for _, p := range s.Paths {
		if lan != "" && live[p.Index] {
			rule("pin", "iifname { %s } ct direction original ct mark & 0x%08x == 0x%08x update @usrc_%d { ip saddr . udp sport }",
				lan, pinMask, pinMark(p.Index), p.Index)
		}
		rule("pin", "ct mark & 0x%08x == 0x%08x meta mark set meta mark & 0x%08x | 0x%08x",
			pinMask, pinMark(p.Index), ^uint32(pinMask), pinMark(p.Index))
	}
	for _, p := range s.Paths {
		if !p.Dead {
			continue
		}
		m := pinMark(p.Index)
		rule("pin_dead", "ct mark & 0x%08x == 0x%08x meta l4proto tcp reject with tcp reset", pinMask, m)
		rule("pin_dead", "ct mark & 0x%08x == 0x%08x reject", pinMask, m)
	}
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
	// One transaction: the chains are swapped atomically and the sticky
	// sets survive.
	script := PinNftRules(s)
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
	if !pinsActive {
		pinsActive = true
		flushLANUDP()
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
	if pinsActive {
		pinsActive = false
		flushLANUDP()
	}
}

// flushLANUDP forgets the LAN's UDP conntrack entries when pinning starts or
// stops. Entries from before keep their old path and NAT binding, and a busy
// socket (Tailscale pings every few seconds) refreshes them forever, so one
// socket would stay split across two public addresses. UDP flows just
// re-create on their next packet; TCP is left alone.
func flushLANUDP() {
	if !have("conntrack") {
		return
	}
	for _, dev := range LANDevices() {
		ni, err := net.InterfaceByName(dev)
		if err != nil {
			continue
		}
		addrs, _ := ni.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				sub := &net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}
				run("conntrack", "-D", "-p", "udp", "-s", sub.String())
			}
		}
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
