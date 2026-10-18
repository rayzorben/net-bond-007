package client

import (
	"testing"

	"github.com/rayzorben/net-bond-007/internal/bond"
)

func TestApplyGuard(t *testing.T) {
	c := &Client{}
	p := bond.Plan{Active: []uint8{1, 2}, Mode: "mirroring", Copies: 2}
	c.applyGuard(&p)
	if p.Direct {
		t.Fatal("inactive guard changed the plan")
	}
	c.guard.active, c.guard.viaID = true, 2
	c.applyGuard(&p)
	if !p.Direct || p.DirectVia != 2 || p.Mode != "direct" {
		t.Fatalf("active guard not applied: %+v", p)
	}
	// The guard's link was benched: stay in the bond rather than route over it.
	p = bond.Plan{Active: []uint8{1}, Mode: "single", Copies: 1}
	c.applyGuard(&p)
	if p.Direct {
		t.Fatal("went direct over a link the bond is not using")
	}
}

// A refused download (rate limit) is not "the bond is slower".
func TestRefusedStatus(t *testing.T) {
	for e, want := range map[string]bool{
		"429 Too Many Requests": true, "403 Forbidden": true,
		"500 Internal Server Error": false, "dial tcp: i/o timeout": false, "": false, "4": false,
	} {
		if refusedStatus(e) != want {
			t.Errorf("refusedStatus(%q) = %v", e, !want)
		}
	}
}
