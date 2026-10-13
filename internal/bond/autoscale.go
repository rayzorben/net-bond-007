package bond

import (
	"sort"
	"time"
)

// Automatic scaling (Smart mode): decide, from each link's reliability, how
// many copies of each packet to send, which links to bench (stop using but
// keep monitoring), and whether to leave the tunnel and go direct because
// only one reliable link remains.

type AutoParams struct {
	Target           float64       // delivery target, e.g. 0.995
	BenchBelow       float64       // bench a link whose reliability drops below this
	RejoinAbove      float64       // a benched link rejoins above this (hysteresis)
	DirectWhenSingle bool          // go direct when only one reliable link remains
	DirectAfter      time.Duration // ...for this long
	BondAfter        time.Duration // re-bond after 2+ reliable links for this long
	CopiesDownAfter  time.Duration // fewer copies only after the need has dropped this long
}

type LinkRel struct {
	ID     uint8
	Usable bool // up or degraded right now
	Rel    float64
	// Metered links (capped phone plans) are failover: they don't count
	// towards copies, benching or direct mode, and are only made active
	// when no other link is usable.
	Metered bool
	// NoDirect links can't carry traffic outside the tunnel (a phone relay
	// is not an interface of this machine), so they never become the
	// direct-bypass link.
	NoDirect bool
}

type Plan struct {
	Copies    int               `json:"copies"`
	Active    []uint8           `json:"active"`
	Benched   map[uint8]bool    `json:"-"`
	BenchedID []uint8           `json:"benched"`
	Direct    bool              `json:"direct"`
	DirectVia uint8             `json:"directVia,omitempty"`
	Mode      string            `json:"mode"` // balancing | mirroring | redundant | single | direct | none
	Rel       map[uint8]float64 `json:"-"`
}

type AutoScaler struct {
	benched     map[uint8]bool
	copies      int
	lowSince    time.Time
	direct      bool
	singleSince time.Time
	multiSince  time.Time
}

// Update computes the plan for this moment. Call it about once per second.
func (a *AutoScaler) Update(now time.Time, links []LinkRel, p AutoParams) Plan {
	if a.benched == nil {
		a.benched = map[uint8]bool{}
	}
	plan := Plan{Benched: map[uint8]bool{}, Rel: map[uint8]float64{}}
	var usable, meteredUp []LinkRel
	all := links
	links = links[:0:0]
	for _, l := range all {
		plan.Rel[l.ID] = l.Rel
		if l.Metered {
			if l.Usable {
				meteredUp = append(meteredUp, l)
			}
			continue
		}
		links = append(links, l)
	}
	for _, l := range links {
		switch {
		case a.benched[l.ID] && l.Rel >= p.RejoinAbove:
			a.benched[l.ID] = false
		case !a.benched[l.ID] && l.Rel < p.BenchBelow:
			a.benched[l.ID] = true
		}
		if l.Usable {
			usable = append(usable, l)
		}
	}
	// Bench only when there is a genuinely good alternative. If every link is
	// struggling (mountain passes), they all stay in and mirror each other:
	// two flaky links beat one.
	goodExists := false
	for _, l := range usable {
		if l.Rel >= p.RejoinAbove {
			goodExists = true
		}
	}
	isBenched := func(id uint8) bool { return a.benched[id] && goodExists }
	var active []LinkRel
	for _, l := range usable {
		if !isBenched(l.ID) {
			active = append(active, l)
		}
	}
	// Never leave the bond with nothing: keep the most reliable usable link.
	if len(active) == 0 && len(usable) > 0 {
		best := usable[0]
		for _, l := range usable[1:] {
			if l.Rel > best.Rel {
				best = l
			}
		}
		active = []LinkRel{best}
	}
	// Nothing else works: the metered links carry the traffic.
	if len(active) == 0 {
		active = meteredUp
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Rel > active[j].Rel })
	for _, l := range links {
		if isBenched(l.ID) && !(len(active) == 1 && active[0].ID == l.ID) {
			plan.Benched[l.ID] = true
			plan.BenchedID = append(plan.BenchedID, l.ID)
		}
	}
	for _, l := range active {
		plan.Active = append(plan.Active, l.ID)
	}

	// Copies: raise immediately, lower only once the need has stayed lower.
	rels := make([]float64, len(active))
	for i, l := range active {
		rels[i] = l.Rel
	}
	want := CopiesFor(rels, p.Target)
	switch {
	case want >= a.copies:
		a.copies, a.lowSince = want, time.Time{}
	case a.lowSince.IsZero():
		a.lowSince = now
	case now.Sub(a.lowSince) >= p.CopiesDownAfter:
		a.copies, a.lowSince = want, time.Time{}
	}
	if a.copies > len(active) {
		a.copies = len(active)
	}
	plan.Copies = a.copies

	// Direct bypass with hysteresis. It depends on how many links are
	// trusted (not benched), not on which happen to be up this second: a
	// trusted link in a brief dropout must not flip the client out of the
	// tunnel (every switch changes the public IP and restarts streams).
	n := 0
	for _, l := range links {
		if !isBenched(l.ID) {
			n++
		}
	}
	if len(active) == 0 {
		n = 0
	}
	switch {
	case !p.DirectWhenSingle || n == 0 || len(meteredUp) > 0:
		// A usable metered failover keeps us in the tunnel: leaving it would
		// make failover slow and change the public IP mid-stream.
		a.direct, a.singleSince, a.multiSince = false, time.Time{}, time.Time{}
	case n == 1:
		a.multiSince = time.Time{}
		if a.singleSince.IsZero() {
			a.singleSince = now
		}
		if !a.direct && now.Sub(a.singleSince) >= p.DirectAfter {
			a.direct = true
		}
	default:
		a.singleSince = time.Time{}
		if a.multiSince.IsZero() {
			a.multiSince = now
		}
		if a.direct && now.Sub(a.multiSince) >= p.BondAfter {
			a.direct = false
		}
	}
	plan.Direct = a.direct && len(active) >= 1 && len(meteredUp) == 0 && !active[0].NoDirect
	if plan.Direct {
		plan.DirectVia = active[0].ID
	}

	na := len(active)
	switch {
	case plan.Direct:
		plan.Mode = "direct"
	case na == 0:
		plan.Mode = "none"
	case na == 1:
		plan.Mode = "single"
	case plan.Copies <= 1:
		plan.Mode = "balancing"
	case plan.Copies >= na:
		plan.Mode = "mirroring"
	default:
		plan.Mode = "redundant"
	}
	return plan
}
