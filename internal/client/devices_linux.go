//go:build linux

package client

import (
	"bufio"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// lanOrigins reads the kernel's connection tracking table: for connections
// masqueraded into the tunnel, the reply tuple's destination is the tunnel
// address and the original tuple's source is the LAN device.
func lanOrigins() map[natKey]netip.AddrPort {
	f, err := os.Open("/proc/net/nf_conntrack")
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseConntrack(bufio.NewScanner(f))
}

func parseConntrack(sc *bufio.Scanner) map[natKey]netip.AddrPort {
	out := map[natKey]netip.AddrPort{}
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 6 {
			continue
		}
		proto, err := strconv.Atoi(fs[3])
		if err != nil {
			continue
		}
		var tuples [2]map[string]string
		n := -1
		for _, kv := range fs[4:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			if k == "src" {
				n++
				if n > 1 {
					break
				}
				tuples[n] = map[string]string{}
			}
			if n >= 0 && n <= 1 {
				tuples[n][k] = v
			}
		}
		if n < 1 {
			continue
		}
		orig, reply := tuples[0], tuples[1]
		oSrc, err1 := addrPort(orig["src"], orig["sport"])
		rSrc, err2 := addrPort(reply["src"], reply["sport"])
		rDst, err3 := addrPort(reply["dst"], reply["dport"])
		if err1 != nil || err2 != nil || err3 != nil || oSrc == rDst {
			continue // not translated: the client's own traffic
		}
		out[natKey{uint8(proto), rDst, rSrc}] = oSrc
	}
	return out
}

func addrPort(a, p string) (netip.AddrPort, error) {
	ip, err := netip.ParseAddr(a)
	if err != nil {
		return netip.AddrPort{}, err
	}
	port, _ := strconv.Atoi(p)
	return netip.AddrPortFrom(ip, uint16(port)), nil
}

// lanNames reads DHCP leases (OpenWrt dnsmasq format:
// "expiry mac ip hostname clientid") for device names.
func lanNames() map[string]Device {
	out := map[string]Device{}
	for _, path := range []string{"/tmp/dhcp.leases", "/var/lib/misc/dnsmasq.leases"} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			fs := strings.Fields(line)
			if len(fs) < 4 {
				continue
			}
			name := fs[3]
			if name == "*" {
				name = fs[2]
			}
			out[fs[2]] = Device{IP: fs[2], Name: name, MAC: fs[1]}
		}
	}
	return out
}
