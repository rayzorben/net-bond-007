//go:build darwin

package platform

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	hwMu    sync.Mutex
	hwCache map[string]hint
	hwAt    time.Time
)

// describe maps BSD names (en0) to hardware ports (Wi-Fi, iPhone USB, ...).
func describe() map[string]hint {
	hwMu.Lock()
	defer hwMu.Unlock()
	if hwCache != nil && time.Since(hwAt) < 30*time.Second {
		return hwCache
	}
	out, err := exec.Command("networksetup", "-listallhardwareports").Output()
	m := map[string]hint{}
	if err == nil {
		var port string
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "Hardware Port: "); ok {
				port = v
			} else if v, ok := strings.CutPrefix(line, "Device: "); ok && port != "" {
				m[v] = hint{kind: portKind(port), desc: port}
				port = ""
			}
		}
	}
	hwCache, hwAt = m, time.Now()
	return m
}

func portKind(p string) string {
	l := strings.ToLower(p)
	switch {
	case strings.Contains(l, "wi-fi"), strings.Contains(l, "airport"):
		return "wifi"
	case strings.Contains(l, "iphone"), strings.Contains(l, "usb"), strings.Contains(l, "android"):
		return "usb"
	case strings.Contains(l, "bluetooth"):
		return "bluetooth"
	case strings.Contains(l, "ethernet"), strings.Contains(l, "thunderbolt"), strings.Contains(l, "lan"):
		return "ethernet"
	}
	return "other"
}

func kindOf(ni net.Interface) string { return kindFromName(ni.Name) }

func BindControl(ifName string, ifIndex int) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if strings.HasSuffix(network, "6") {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
			} else {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
			}
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

func ConfigureTunnel(t TunnelSpec) (func(), error) {
	mask := net.CIDRMask(t.Addr.Bits(), 32)
	if err := run("ifconfig", t.Name, "inet", t.Addr.Addr().String(), t.Gateway.String(),
		"netmask", net.IP(mask).String(), "mtu", fmt.Sprint(t.MTU), "up"); err != nil {
		return func() {}, err
	}
	for _, r := range t.Routes {
		run("route", "-q", "-n", "delete", "-net", r.String())
		if err := run("route", "-q", "-n", "add", "-net", r.String(), "-interface", t.Name); err != nil {
			return func() {}, err
		}
	}
	// Routes vanish with the utun device; DNS override is not implemented
	// on macOS yet (see docs/ROADMAP.md).
	return func() {}, nil
}

func DefaultConfigPath(role string) string {
	return "/Library/Application Support/Bond007/" + role + ".json"
}

// TunName: macOS only allows utun devices; the kernel picks the number.
func TunName(string) string { return "utun" }
