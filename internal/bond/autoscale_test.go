package bond

import (
	"testing"
	"time"
)

var params = AutoParams{
	Target: 0.995, BenchBelow: 0.6, RejoinAbove: 0.75,
	DirectWhenSingle: true, DirectAfter: 15 * time.Second, BondAfter: 30 * time.Second,
	CopiesDownAfter: 10 * time.Second,
}

// drive feeds the scaler one second at a time with each link's delivery
// pattern, returning the plan after the given number of seconds.
type pattern func(sec int) bool

func run(t *testing.T, a *AutoScaler, now time.Time, secs int, pats map[uint8]pattern, rels map[uint8]*Reliability) (Plan, time.Time) {
	t.Helper()
	var plan Plan
	for s := 0; s < secs; s++ {
		for q := 0; q < 4; q++ { // 250ms probes
			now = now.Add(250 * time.Millisecond)
			for id, p := range pats {
				rels[id].Observe(p(s), now, 10*time.Second, 60*time.Second)
			}
		}
		var ls []LinkRel
		for id := range pats {
			ls = append(ls, LinkRel{ID: id, Usable: pats[id](s), Rel: rels[id].Value()})
		}
		plan = a.Update(now, ls, params)
	}
	return plan, now
}

func TestRoadTripScenario(t *testing.T) {
	var a AutoScaler
	rels := map[uint8]*Reliability{1: {}, 2: {}}
	now := time.Now()
	starlink := uint8(1)
	phone := uint8(2)

	// Mountains: Starlink cuts out 20s of every 60, the phone 15s of every 50,
	// rarely at the same time. Both look unreliable -> mirror on both.
	mountains := map[uint8]pattern{
		starlink: func(s int) bool { return s%60 >= 20 },
		phone:    func(s int) bool { return (s+25)%50 >= 15 },
	}
	plan, now := run(t, &a, now, 300, mountains, rels)
	if plan.Direct {
		t.Fatalf("mountains: went direct while two links were alternating: %+v", plan)
	}
	if plan.Copies != len(plan.Active) || len(plan.Active) < 1 {
		t.Fatalf("mountains: expected mirroring, got %+v (rel %.2f / %.2f)", plan, rels[1].Value(), rels[2].Value())
	}

	// RV park: both links clean for 10 minutes -> load-balance (one copy).
	park := map[uint8]pattern{starlink: func(int) bool { return true }, phone: func(int) bool { return true }}
	plan, now = run(t, &a, now, 600, park, rels)
	if plan.Mode != "balancing" || plan.Copies != 1 || len(plan.Active) != 2 {
		t.Fatalf("rv park: expected balancing over both, got %+v (rel %.3f / %.3f)", plan, rels[1].Value(), rels[2].Value())
	}

	// The phone's Wi-Fi dies for good: it gets benched, and after the
	// single-link delay the client goes direct over Starlink.
	phoneGone := map[uint8]pattern{starlink: func(int) bool { return true }, phone: func(int) bool { return false }}
	plan, now = run(t, &a, now, 40, phoneGone, rels)
	if !plan.Benched[phone] || !plan.Direct || plan.DirectVia != starlink {
		t.Fatalf("phone gone: expected phone benched and direct via Starlink, got %+v", plan)
	}

	// Phone comes back: it must earn trust (rejoin threshold) and then hold
	// for BondAfter before we leave direct mode. No instant flapping.
	plan, now = run(t, &a, now, 5, park, rels)
	if !plan.Direct {
		t.Fatal("re-bonded instantly; expected to wait for the phone to prove itself")
	}
	plan, _ = run(t, &a, now, 180, park, rels)
	if plan.Direct || len(plan.Active) != 2 {
		t.Fatalf("phone recovered for 3 min: expected bonded again, got %+v (rel phone %.2f)", plan, rels[2].Value())
	}
}

func TestNeverBenchTheLastLink(t *testing.T) {
	var a AutoScaler
	// All links struggling: nobody is benched, they mirror each other.
	plan := a.Update(time.Now(), []LinkRel{{ID: 1, Usable: true, Rel: 0.3}, {ID: 2, Usable: true, Rel: 0.2}}, params)
	if len(plan.Active) != 2 || len(plan.BenchedID) != 0 || plan.Mode != "mirroring" {
		t.Fatalf("all-flaky links should all be used, got %+v", plan)
	}
	// One good link: the bad one is benched, but monitoring continues.
	plan = a.Update(time.Now(), []LinkRel{{ID: 1, Usable: true, Rel: 0.99}, {ID: 2, Usable: true, Rel: 0.2}}, params)
	if !plan.Benched[2] || len(plan.Active) != 1 || plan.Active[0] != 1 {
		t.Fatalf("expected link 2 benched next to a good link, got %+v", plan)
	}
}

func TestCopiesDropOnlyAfterDelay(t *testing.T) {
	var a AutoScaler
	t0 := time.Now()
	flaky := []LinkRel{{ID: 1, Usable: true, Rel: 0.8}, {ID: 2, Usable: true, Rel: 0.8}}
	clean := []LinkRel{{ID: 1, Usable: true, Rel: 0.999}, {ID: 2, Usable: true, Rel: 0.999}}
	if p := a.Update(t0, flaky, params); p.Copies != 2 {
		t.Fatalf("flaky: %d copies", p.Copies)
	}
	if p := a.Update(t0.Add(5*time.Second), clean, params); p.Copies != 2 {
		t.Fatal("copies dropped before the hold-down delay")
	}
	if p := a.Update(t0.Add(16*time.Second), clean, params); p.Copies != 1 {
		t.Fatalf("copies should have dropped after the delay, got %d", p.Copies)
	}
	if p := a.Update(t0.Add(17*time.Second), flaky, params); p.Copies != 2 {
		t.Fatal("copies must rise immediately when links get flaky")
	}
}

func TestDirectDisabled(t *testing.T) {
	var a AutoScaler
	p := params
	p.DirectWhenSingle = false
	plan := a.Update(time.Now(), []LinkRel{{ID: 1, Usable: true, Rel: 0.99}}, p)
	plan = a.Update(time.Now().Add(time.Minute), []LinkRel{{ID: 1, Usable: true, Rel: 0.99}}, p)
	if plan.Direct || plan.Mode != "single" {
		t.Fatalf("direct mode used while disabled: %+v", plan)
	}
}

// Starlink + metered iPhone: the phone doesn't count towards copies or
// benching, blocks direct mode (instant failover needs the tunnel), and
// becomes the active link when Starlink is gone.
func TestMeteredLinkIsFailover(t *testing.T) {
	var a AutoScaler
	now := time.Now()
	ls := []LinkRel{{ID: 1, Usable: true, Rel: 0.999}, {ID: 2, Usable: true, Rel: 0.999, Metered: true}}
	var p Plan
	for i := 0; i < 60; i++ {
		now = now.Add(time.Second)
		p = a.Update(now, ls, params)
	}
	if p.Direct || p.Mode != "single" || p.Copies != 1 || len(p.Active) != 1 || p.Active[0] != 1 {
		t.Fatalf("starlink + phone: %+v", p)
	}
	// A weak phone signal never gets benched (it's failover, not a choice).
	ls[1].Rel = 0.3
	p = a.Update(now.Add(time.Second), ls, params)
	if p.Benched[2] {
		t.Fatal("metered link benched")
	}
	// Starlink gone: the phone carries everything.
	ls[0].Usable, ls[1].Rel = false, 0.99
	p = a.Update(now.Add(2*time.Second), ls, params)
	if len(p.Active) != 1 || p.Active[0] != 2 || p.Direct || p.Copies != 1 {
		t.Fatalf("failover: %+v", p)
	}
	// Phone unplugged: Starlink alone may go direct again.
	ls = ls[:1]
	ls[0].Usable = true
	for i := 0; i < 20; i++ {
		now = now.Add(time.Second)
		p = a.Update(now.Add(3*time.Second), ls, params)
	}
	if !p.Direct {
		t.Fatalf("single starlink should go direct: %+v", p)
	}
}

// A phone relay is not an interface of this machine: when it is the only
// trusted link, stay in the tunnel instead of going direct through it.
func TestNoDirectViaRelay(t *testing.T) {
	var a AutoScaler
	now := time.Now()
	ls := []LinkRel{{ID: 1, Usable: true, Rel: 0.99, NoDirect: true}}
	var p Plan
	for i := 0; i < 60; i++ {
		p = a.Update(now.Add(time.Duration(i)*time.Second), ls, params)
	}
	if p.Direct {
		t.Fatalf("went direct via a relay link: %+v", p)
	}
	ls[0].NoDirect = false
	for i := 60; i < 120; i++ {
		p = a.Update(now.Add(time.Duration(i)*time.Second), ls, params)
	}
	if !p.Direct || p.DirectVia != 1 {
		t.Fatalf("a normal single link should go direct: %+v", p)
	}
}
