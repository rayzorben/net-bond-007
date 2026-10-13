//go:build windows

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
	"unsafe"

	"golang.org/x/sys/windows"
)

// describe classifies adapters using IfType and the driver description.
func describe() map[string]hint {
	m := map[string]hint{}
	var size uint32 = 15000
	for i := 0; i < 3; i++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_PREFIX, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return m
		}
		for a := aa; a != nil; a = a.Next {
			name := windows.UTF16PtrToString(a.FriendlyName)
			desc := windows.UTF16PtrToString(a.Description)
			m[name] = hint{kind: winKind(a.IfType, desc), desc: desc}
		}
		return m
	}
	return m
}

func winKind(ifType uint32, desc string) string {
	d := strings.ToLower(desc)
	switch {
	case strings.Contains(d, "bluetooth"):
		return "bluetooth"
	case strings.Contains(d, "remote ndis"), strings.Contains(d, "rndis"), strings.Contains(d, "apple mobile device"), strings.Contains(d, "usb"):
		return "usb"
	case strings.Contains(d, "wintun"), strings.Contains(d, "wireguard"), strings.Contains(d, "tap-windows"), strings.Contains(d, "vpn"):
		return "vpn"
	case strings.Contains(d, "hyper-v"), strings.Contains(d, "virtualbox"), strings.Contains(d, "vmware"):
		return "virtual"
	}
	switch ifType {
	case 71:
		return "wifi"
	case 243, 244:
		return "cellular"
	case 6:
		return "ethernet"
	case 24:
		return "virtual"
	}
	return "other"
}

func kindOf(ni net.Interface) string { return kindFromName(ni.Name) }

const ipUnicastIf = 31

func BindControl(ifName string, ifIndex int) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			if strings.HasSuffix(network, "6") {
				serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, ipUnicastIf, ifIndex)
			} else {
				// IPv4 expects the index in network byte order.
				idx := uint32(ifIndex)
				be := int(idx>>24 | (idx>>8)&0xff00 | (idx<<8)&0xff0000 | idx<<24)
				serr = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, be)
			}
		})
		if err != nil {
			return err
		}
		return serr
	}
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func ConfigureTunnel(t TunnelSpec) (func(), error) {
	idx := fmt.Sprint(t.Index)
	mask := net.IP(net.CIDRMask(t.Addr.Bits(), 32)).String()
	steps := [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + idx, "source=static", "address=" + t.Addr.Addr().String(), "mask=" + mask, "gateway=none"},
		{"netsh", "interface", "ipv4", "set", "subinterface", idx, fmt.Sprintf("mtu=%d", t.MTU), "store=active"},
		{"netsh", "interface", "ipv4", "set", "interface", idx, "metric=1"},
	}
	for _, s := range steps {
		if err := run(s[0], s[1:]...); err != nil {
			return func() {}, err
		}
	}
	for _, r := range t.Routes {
		run("netsh", "interface", "ipv4", "delete", "route", r.String(), idx)
		if t.Direct {
			continue // leave it to the OS default route
		}
		if err := run("netsh", "interface", "ipv4", "add", "route", r.String(), idx, t.Gateway.String(), "metric=1", "store=active"); err != nil {
			return func() {}, err
		}
	}
	if t.DNS.IsValid() {
		run("netsh", "interface", "ipv4", "set", "dnsservers", "name="+idx, "source=static", "address="+t.DNS.String(), "register=none", "validate=no")
	}
	// Routes and DNS disappear with the Wintun adapter.
	return func() {}, nil
}

func DefaultConfigPath(role string) string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Net-Bond-007", role+".json")
}

func TunName(requested string) string { return requested }

// DefaultGateway is not needed here: in direct mode the tunnel's routes are
// simply not installed, so traffic follows the OS default route.
func DefaultGateway(string) netip.Addr { return netip.Addr{} }
