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
