//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const portMapNftTable = "net_bond_007_ports"

var portMapMu sync.Mutex
var portMapRoute string // tunnel device the routing table points at

func PortMapSupported() bool { return have("nft") && have("ip") }

// PortMapNftRules is the ruleset for one forwarding state. (Chain names
// must avoid nft keywords such as "fwd" or "nat".)
func PortMapNftRules(s PortMapSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %s {\n", portMapNftTable)
	// Inbound: public port on the tunnel address -> LAN device.
	b.WriteString("  chain portmap_in {\n    type nat hook prerouting priority dstnat - 5; policy accept;\n")
	for _, r := range s.Rules {
		fmt.Fprintf(&b, "    iifname %q ip daddr %s %s dport %d dnat to %s:%d\n", s.Tun, s.TunIP, r.Proto, r.Port, r.Device, r.DevicePort)
	}
	b.WriteString("  }\n")
	// The device's own packets from a forwarded port leave from the public
	// port too (one stable address for peer-to-peer protocols).
	b.WriteString("  chain portmap_out {\n    type nat hook postrouting priority srcnat - 5; policy accept;\n")
	for _, r := range s.Rules {
		fmt.Fprintf(&b, "    oifname %q ip saddr %s %s sport %d snat to %s:%d\n", s.Tun, r.Device, r.Proto, r.DevicePort, s.TunIP, r.Port)
	}
	b.WriteString("  }\n")
	// Forwarded connections, and new ones from a forwarded port, stay in the
	// tunnel whatever path the rest of the LAN takes.
	b.WriteString("  chain portmap_mark {\n    type filter hook prerouting priority -160; policy accept;\n")
	fmt.Fprintf(&b, "    iifname %q ct state new ct mark set ct mark | 0x%08x\n", s.Tun, portMapMark)
	if len(s.LAN) > 0 {
		quoted := make([]string, len(s.LAN))
		for i, d := range s.LAN {
			quoted[i] = fmt.Sprintf("%q", d)
		}
		for _, r := range s.Rules {
			fmt.Fprintf(&b, "    iifname { %s } ct state new ip saddr %s %s sport %d ct mark set ct mark | 0x%08x\n",
				strings.Join(quoted, ", "), r.Device, r.Proto, r.DevicePort, portMapMark)
		}
	}
	fmt.Fprintf(&b, "    iifname != %q ct mark & 0x%08x != 0 meta mark set meta mark | 0x%08x\n", s.Tun, portMapMark, portMapMark)
	b.WriteString("  }\n}\n")
	return b.String()
}

// ApplyPortMap installs s, replacing the previous state atomically.
func ApplyPortMap(s PortMapSpec) error {
	portMapMu.Lock()
	defer portMapMu.Unlock()
	t := fmt.Sprint(portMapTable)
	if portMapRoute != s.Tun {
		if err := run("ip", "route", "replace", "default", "dev", s.Tun, "table", t); err != nil {
			return err
		}
		pref := fmt.Sprint(portMapPref)
		for run("ip", "rule", "del", "pref", pref) == nil {
		}
		if err := run("ip", "rule", "add", "pref", pref, "fwmark", fmt.Sprintf("0x%x/0x%x", portMapMark, portMapMark), "lookup", t); err != nil {
			return err
		}
		portMapRoute = s.Tun
	}
	script := fmt.Sprintf("table ip %[1]s\ndelete table ip %[1]s\n", portMapNftTable) + PortMapNftRules(s)
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %v: %s", err, out)
	}
	return EnsurePortMapFirewall(s.Tun)
}

// ClearPortMap removes every forward.
func ClearPortMap() {
	portMapMu.Lock()
	defer portMapMu.Unlock()
	run("nft", "delete", "table", "ip", portMapNftTable)
	for run("ip", "rule", "del", "pref", fmt.Sprint(portMapPref)) == nil {
	}
	run("ip", "route", "flush", "table", fmt.Sprint(portMapTable))
	portMapRoute = ""
	removeFw4Accept()
}

// OpenWrt's firewall (fw4) rejects anything the tunnel's zone forwards to
// the LAN. Port forwards are let through with the rule fw4 itself uses for
// its own: accept connections that were DNATed. It goes into the zone's
// chain now, and into an include file so fw4 keeps it across reloads.
const fw4Comment = "net-bond-007 port forwards"

var fw4Include string // include file written, for removal

var zoneJump = regexp.MustCompile(`iifname "([^"]+)" jump (forward_\S+)`)

// fw4ForwardChain finds the fw4 chain handling traffic forwarded from dev.
func fw4ForwardChain(dev string) string {
	out, err := exec.Command("nft", "list", "chain", "inet", "fw4", "forward").Output()
	if err != nil {
		return ""
	}
	for _, m := range zoneJump.FindAllStringSubmatch(string(out), -1) {
		if m[1] == dev {
			return m[2]
		}
	}
	return ""
}

// EnsurePortMapFirewall (re)inserts the fw4 accept rule if a firewall
// reload removed it. A no-op without fw4.
func EnsurePortMapFirewall(dev string) error {
	chain := fw4ForwardChain(dev)
	if chain == "" {
		return nil
	}
	out, err := exec.Command("nft", "list", "chain", "inet", "fw4", chain).Output()
	if err != nil {
		return nil
	}
	if !strings.Contains(string(out), fw4Comment) {
		if err := run("nft", "insert", "rule", "inet", "fw4", chain, "ct", "status", "dnat", "accept", "comment", fmt.Sprintf("%q", fw4Comment)); err != nil {
			return err
		}
	}
	if _, err := os.Stat("/usr/share/nftables.d"); err == nil {
		dir := filepath.Join("/usr/share/nftables.d/chain-pre", chain)
		file := filepath.Join(dir, "50-net-bond-007-ports.nft")
		if fw4Include != file {
			removeFw4Include()
			if os.MkdirAll(dir, 0o755) == nil && os.WriteFile(file, []byte(fmt.Sprintf("ct status dnat accept comment %q\n", fw4Comment)), 0o644) == nil {
				fw4Include = file
			}
		}
	}
	return nil
}

func removeFw4Accept() {
	removeFw4Include()
	out, err := exec.Command("nft", "-a", "list", "table", "inet", "fw4").Output()
	if err != nil {
		return
	}
	chain := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "chain ") {
			chain = strings.Fields(line)[1]
			continue
		}
		if strings.Contains(line, fw4Comment) {
			if i := strings.LastIndex(line, "# handle "); i >= 0 {
				run("nft", "delete", "rule", "inet", "fw4", chain, "handle", strings.TrimSpace(line[i+len("# handle "):]))
			}
		}
	}
}

func removeFw4Include() {
	if fw4Include == "" {
		// Left behind by an earlier run that didn't exit cleanly.
		matches, _ := filepath.Glob("/usr/share/nftables.d/chain-pre/*/50-net-bond-007-ports.nft")
		for _, m := range matches {
			os.Remove(m)
			os.Remove(filepath.Dir(m)) // only if empty
		}
		return
	}
	os.Remove(fw4Include)
	os.Remove(filepath.Dir(fw4Include))
	fw4Include = ""
}
