package client

import (
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/platform"
)

// The 2026-10-22 freeze: applyTunnel held tunMu and waited for mu while
// reconcile held mu and asked eligibility -> tunName for tunMu. Neither
// eligibility nor deliver may wait for tunMu.
func TestTunnelLockNeverBlocksMuHolders(t *testing.T) {
	c := relayTestClient(t, "")
	c.tunMu.Lock() // applyTunnel mid-configuration
	defer c.tunMu.Unlock()

	done := make(chan struct{})
	go func() {
		c.mu.Lock() // reconcile
		c.eligibility(c.cfg.Get(), platform.Iface{Name: "sta0", Up: true})
		c.mu.Unlock()
		c.deliver([]byte{0x45}) // the receive path
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("eligibility or deliver waited for tunMu: deadlock with applyTunnel")
	}
}
