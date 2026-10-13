//go:build linux

package platform

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func describe() map[string]hint { return nil }

func kindOf(ni net.Interface) string {
	base := "/sys/class/net/" + ni.Name
	if exists(base+"/wireless") || exists(base+"/phy80211") {
		return "wifi"
	}
	if b, err := os.ReadFile(base + "/uevent"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			switch strings.TrimPrefix(line, "DEVTYPE=") {
			case "wlan":
				return "wifi"
			case "wwan":
				return "cellular"
			case "bluetooth":
				return "bluetooth"
			case "bridge", "vlan", "veth", "bond", "macvlan", "vxlan":
				return "virtual"
			}
		}
	}
	if drv, err := os.Readlink(base + "/device/driver"); err == nil {
		switch filepath.Base(drv) {
		case "rndis_host", "cdc_ether", "cdc_ncm", "ipheth", "cdc_eem", "cdc_subset":
			return "usb"
		case "qmi_wwan", "cdc_mbim", "option", "huawei_cdc_ncm", "sierra_net":
			return "cellular"
		}
	}
	k := kindFromName(ni.Name)
	if k == "other" && exists(base+"/device") {
		return "ethernet"
	}
	return k
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// BindControl pins a socket to one interface so its packets leave through
// that interface regardless of the routing table (which points at the tunnel).
func BindControl(ifName string, ifIndex int) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, ifName)
		})
		if err != nil {
			return err
		}
		return serr
	}
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func have(bin string) bool { _, err := exec.LookPath(bin); return err == nil }

// ConfigureTunnel assigns the tunnel address and routes. It returns a cleanup
// function that undoes anything not removed automatically with the device.
func ConfigureTunnel(t TunnelSpec) (func(), error) {
	dev := t.Name
	steps := [][]string{
		{"ip", "addr", "flush", "dev", dev},
		{"ip", "addr", "add", t.Addr.String(), "dev", dev},
		{"ip", "link", "set", "dev", dev, "mtu", fmt.Sprint(t.MTU), "up"},
	}
	var undo []func()
	for _, r := range t.Routes {
		switch {
		case t.Direct && t.DirectGW.IsValid():
			steps = append(steps, []string{"ip", "route", "replace", r.String(), "via", t.DirectGW.String(), "dev", t.DirectDev})
		case t.Direct:
			steps = append(steps, []string{"ip", "route", "replace", r.String(), "dev", t.DirectDev})
		default:
			steps = append(steps, []string{"ip", "route", "replace", r.String(), "dev", dev})
		}
		if t.Direct {
			r := r
			undo = append(undo, func() { run("ip", "route", "del", r.String(), "dev", t.DirectDev) })
		}
	}
	for _, s := range steps {
		if err := run(s[0], s[1:]...); err != nil {
			return func() {}, err
		}
	}
	if t.DNS.IsValid() && have("resolvectl") {
		if err := run("resolvectl", "dns", dev, t.DNS.String()); err == nil {
			run("resolvectl", "domain", dev, "~.")
			run("resolvectl", "default-route", dev, "true")
			undo = append(undo, func() { run("resolvectl", "revert", dev) })
		}
	}
	if len(t.ViaTunnel) > 0 && !t.Direct {
		// Table 7007 sends everything into the tunnel; priority-0 rules pick
		// it for these destinations ahead of GL.iNet's rule that routes
		// router-originated traffic around VPNs.
		const table = "7007"
		run("ip", "route", "flush", "table", table)
		if err := run("ip", "route", "replace", "default", "dev", dev, "table", table); err == nil {
			undo = append(undo, func() { run("ip", "route", "flush", "table", table) })
			for _, a := range t.ViaTunnel {
				dst := netip.PrefixFrom(a, a.BitLen()).String()
				for run("ip", "rule", "del", "to", dst, "lookup", table) == nil {
				}
				if run("ip", "rule", "add", "pref", "0", "to", dst, "lookup", table) == nil {
					undo = append(undo, func() { run("ip", "rule", "del", "to", dst, "lookup", table) })
				}
			}
		}
	}
	if t.RouterMode {
		os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)
		if have("nft") {
			rules := RouterNftRules(dev)
			run("nft", "delete", "table", "inet", "net_bond_007")
			cmd := exec.Command("nft", "-f", "-")
			cmd.Stdin = strings.NewReader(rules)
			if out, err := cmd.CombinedOutput(); err != nil {
				return func() {}, fmt.Errorf("nft: %v: %s", err, out)
			}
			undo = append(undo, func() { run("nft", "delete", "table", "inet", "net_bond_007") })
		} else if have("iptables") {
			run("iptables", "-t", "nat", "-A", "POSTROUTING", "-o", dev, "-j", "MASQUERADE")
			run("iptables", "-t", "mangle", "-A", "FORWARD", "-o", dev, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu")
			undo = append(undo, func() {
				run("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", dev, "-j", "MASQUERADE")
				run("iptables", "-t", "mangle", "-D", "FORWARD", "-o", dev, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu")
			})
		} else {
			return func() {}, fmt.Errorf("router mode needs nft or iptables")
		}
	}
	return func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}, nil
}

// RouterNftRules masquerades traffic leaving through the tunnel and clamps
// TCP MSS to the tunnel MTU. (Chain names must avoid nft keywords such as
// "fwd" or "nat".)
func RouterNftRules(dev string) string {
	return fmt.Sprintf(`table inet net_bond_007 {
  chain masq { type nat hook postrouting priority srcnat; oifname "%[1]s" masquerade; }
  chain mss_clamp { type filter hook forward priority mangle; oifname "%[1]s" tcp flags syn tcp option maxseg size set rt mtu; }
}`, dev)
}

// DefaultGateway returns the IPv4 default gateway of one interface.
func DefaultGateway(dev string) netip.Addr {
	out, err := exec.Command("ip", "-4", "route", "show", "default", "dev", dev).Output()
	if err != nil {
		return netip.Addr{}
	}
	f := strings.Fields(string(out))
	for i := 0; i+1 < len(f); i++ {
		if f[i] == "via" {
			if a, err := netip.ParseAddr(f[i+1]); err == nil {
				return a
			}
		}
	}
	return netip.Addr{}
}

// DefaultConfigPath is where the client stores its settings.
func DefaultConfigPath(role string) string { return "/etc/net-bond-007/" + role + ".json" }

// TunName returns the requested adapter name (Linux honours any name).
func TunName(requested string) string { return requested }
