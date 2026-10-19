//go:build linux

package platform

import (
	"bufio"
	"fmt"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
)

// The router-mode ruleset must parse. nft --check needs privileges on most
// systems, so this test is skipped when nft can't run.
func TestRouterNftRulesParse(t *testing.T) {
	rules := RouterNftRules("net-bond-007")
	for _, kw := range []string{"chain fwd ", "chain nat "} {
		if strings.Contains(rules, kw) {
			t.Fatalf("chain name is an nft keyword: %q", kw)
		}
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "Operation not permitted") {
			t.Skip("nft --check needs privileges")
		}
		t.Fatalf("nft rejected ruleset: %v\n%s", err, out)
	}
}

func TestParseAdGuardUpstreams(t *testing.T) {
	cfg := `dns:
  bind_hosts:
    - 0.0.0.0
  upstream_dns:
    - 8.8.8.8
    - tls://1.1.1.1
    - "[/lan/]192.168.8.1"
    - https://dns.quad9.net/dns-query
    - 9.9.9.9:53
  upstream_dns_file: ""
  bootstrap_dns:
    - 9.9.9.10
    - 2620:fe::10
  fallback_dns: []
  private_networks: []
`
	got := parseAdGuardUpstreams(bufio.NewScanner(strings.NewReader(cfg)))
	want := "[8.8.8.8 1.1.1.1 9.9.9.9 9.9.9.10]"
	if fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

func TestPinNftRulesRealtime(t *testing.T) {
	s := PinSpec{LAN: []string{"br-lan"}, Current: 2, Realtime: "3478-3481, 8801", RealtimePath: 1, Paths: []PinPath{
		{Index: 1, Dev: "net-bond-007"},
		{Index: 2, Dev: "eth0"},
	}}
	rules := PinNftRules(s)
	call := `iifname { "br-lan" } ct state new ct mark & 0x0f000000 == 0 udp dport { 3478-3481, 8801 } ct mark set ct mark & 0xf0ffffff | 0x01000000`
	bulk := `iifname { "br-lan" } ct state new ct mark & 0x0f000000 == 0 ct mark set ct mark & 0xf0ffffff | 0x02000000`
	i, j := strings.Index(rules, call), strings.Index(rules, bulk)
	if i < 0 || j < 0 || i > j || !strings.Contains(rules, "udp sport { 3478-3481, 8801 }") {
		t.Fatalf("calls must be pinned to the tunnel before the catch-all:\n%s", rules)
	}
	// Already in the tunnel: no exception needed.
	s.Current = 1
	if strings.Contains(PinNftRules(s), "dport") {
		t.Fatal("call rule emitted while everything uses the tunnel")
	}
}

func TestPinNftRules(t *testing.T) {
	s := PinSpec{LAN: []string{"br-lan"}, Current: 2, Paths: []PinPath{
		{Index: 1, Dev: "net-bond-007", Dead: true},
		{Index: 2, Dev: "eth0"},
	}}
	rules := PinNftRules(s)
	for _, want := range []string{
		`iifname { "br-lan" } ct state new ct mark & 0x0f000000 == 0 ct mark set ct mark & 0xf0ffffff | 0x02000000`,
		`ct mark & 0x0f000000 == 0x01000000 meta mark set meta mark & 0xf0ffffff | 0x01000000`,
		`ct mark & 0x0f000000 == 0x02000000 meta mark set meta mark & 0xf0ffffff | 0x02000000`,
		`ct mark & 0x0f000000 == 0x01000000 meta l4proto tcp reject with tcp reset`,
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q in\n%s", want, rules)
		}
	}
	if strings.Contains(rules, "0x02000000 meta l4proto tcp reject") {
		t.Fatal("a live path is being reset")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "Operation not permitted") {
			t.Skip("nft --check needs privileges")
		}
		t.Fatalf("nft rejected ruleset: %v\n%s", err, out)
	}
}

func TestPortMapNftRules(t *testing.T) {
	rules := PortMapNftRules(PortMapSpec{
		Tun: "net-bond-007", TunIP: netip.MustParseAddr("10.77.0.2"), LAN: []string{"br-lan"},
		Rules: []PortMapRule{
			{Proto: "tcp", Port: 40001, Device: netip.MustParseAddr("192.168.8.193"), DevicePort: 59220},
			{Proto: "udp", Port: 40001, Device: netip.MustParseAddr("192.168.8.193"), DevicePort: 59220},
		},
	})
	for _, want := range []string{
		`iifname "net-bond-007" ip daddr 10.77.0.2 udp dport 40001 dnat to 192.168.8.193:59220`,
		`oifname "net-bond-007" ip saddr 192.168.8.193 udp sport 59220 snat to 10.77.0.2:40001`,
		`iifname { "br-lan" } ct state new ip saddr 192.168.8.193 tcp sport 59220 ct mark set ct mark | 0x10000000`,
		`iifname != "net-bond-007" ct mark & 0x10000000 != 0 meta mark set meta mark | 0x10000000`,
	} {
		if !strings.Contains(rules, want) {
			t.Fatalf("missing %q in\n%s", want, rules)
		}
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	cmd := exec.Command("nft", "--check", "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "Operation not permitted") {
			t.Skip("nft --check needs privileges")
		}
		t.Fatalf("nft rejected ruleset: %v\n%s", err, out)
	}
}
