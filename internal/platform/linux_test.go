//go:build linux

package platform

import (
	"bufio"
	"fmt"
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
