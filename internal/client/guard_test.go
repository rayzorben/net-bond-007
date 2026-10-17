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
