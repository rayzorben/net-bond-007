//go:build linux

package platform

import (
	"os/exec"
	"strings"
	"testing"
)

// The router-mode ruleset must parse. nft --check needs privileges on most
// systems, so this test is skipped when nft can't run.
func TestRouterNftRulesParse(t *testing.T) {
	rules := RouterNftRules("bond007")
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
