//go:build linux

package client

import (
	"bufio"
	"net/netip"
	"strings"
	"testing"
)

func TestParseConntrackMasqueraded(t *testing.T) {
	in := `ipv4     2 tcp      6 7426 ESTABLISHED src=192.168.8.193 dst=104.16.1.1 sport=51482 dport=443 packets=8 bytes=3346 src=104.16.1.1 dst=10.77.0.3 sport=443 dport=51482 packets=7 bytes=5209 [ASSURED] mark=0 zone=0 use=2
ipv4     2 udp      17 48 src=10.77.0.3 dst=8.8.8.8 sport=5353 dport=53 src=8.8.8.8 dst=10.77.0.3 sport=53 dport=5353 mark=0 use=1
garbage line`
	m := parseConntrack(bufio.NewScanner(strings.NewReader(in)))
	k := natKey{6, netip.MustParseAddrPort("10.77.0.3:51482"), netip.MustParseAddrPort("104.16.1.1:443")}
	if got := m[k]; got != netip.MustParseAddrPort("192.168.8.193:51482") {
		t.Fatalf("LAN origin %v (map %v)", got, m)
	}
	if len(m) != 1 {
		t.Fatalf("router's own (untranslated) flow should be skipped: %v", m)
	}
}
