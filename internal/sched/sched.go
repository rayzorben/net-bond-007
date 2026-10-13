// Package sched decides which links carry each packet.
//
// The default "smart" policy mirrors every packet on all links when there are
// one or two, and for three or more it sends each packet over Redundancy
// (default 2) links. It rotates the links using a multi-pick smooth weighted
// round robin, so faster links carry proportionally more copies. With three
// equal links and redundancy 2, the result is the rotation
// {1,2} {2,3} {1,3} ...: every chunk travels two paths, and every link carries
// two thirds of the traffic.
package sched

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeSmart  Mode = "smart"  // mirror on <=2 links, stripe with redundancy on >=3
	ModeMirror Mode = "mirror" // every packet on every link (max reliability)
	ModeSpeed  Mode = "speed"  // every packet on exactly one link (max throughput)
	ModeCustom Mode = "custom" // fixed redundancy factor
)

// Candidate describes one link at scheduling time.
type Candidate struct {
	ID      uint8
	Weight  float64 // relative share; <=0 is treated as 1
	Usable  bool    // link is up and its queue has room
	Backup  bool    // only used when no primary link is usable
	Benched bool    // automatic scaling stopped using it (still monitored)
	Metered bool    // over its metered budget: failover only
	Degrade bool    // healthy enough to use, but weight is reduced
	// Delay is the estimated time for a packet sent now to arrive: one-way
	// latency plus the time to drain what is already queued on the link.
	Delay time.Duration
}

type Strategy string

const (
	// StrategyLatency sends each copy on the links where it will arrive
	// soonest. Fast links fill first and slow links absorb only the
	// overflow, which keeps reordering small.
	StrategyLatency Strategy = "latency"
	// StrategyWeighted rotates copies over links in proportion to their
	// weights (smooth weighted round robin).
	StrategyWeighted Strategy = "weighted"
)

// Policy is the subset of bonding settings the scheduler needs.
type Policy struct {
	Mode                   Mode
	Strategy               Strategy
	Redundancy             int
	SmallPacketMirrorBytes int
	RealtimeMirror         bool
	RealtimePorts          PortSet
}

// PacketInfo is what the scheduler knows about a packet.
type PacketInfo struct {
	Len      int
	UDP      bool
	SrcPort  uint16
	DstPort  uint16
	Priority bool // e.g. TCP SYN; mirrored like small packets
}

// Copies returns how many links a packet should travel given n usable links.
func (p Policy) Copies(n int, pkt PacketInfo) int {
	if n <= 0 {
		return 0
	}
	if pkt.Priority || (p.SmallPacketMirrorBytes > 0 && pkt.Len <= p.SmallPacketMirrorBytes) {
		return n
	}
	if p.RealtimeMirror && pkt.UDP && (p.RealtimePorts.Has(pkt.DstPort) || p.RealtimePorts.Has(pkt.SrcPort)) {
		return n
	}
	var r int
	switch p.Mode {
	case ModeMirror:
		r = n
	case ModeSpeed:
		r = 1
	case ModeCustom:
		r = p.Redundancy
	default: // smart
		if n <= 2 {
			r = n
		} else {
			r = p.Redundancy
		}
	}
	if r < 1 {
		r = 1
	}
	if r > n {
		r = n
	}
	return r
}

// Scheduler keeps the round-robin credit state. Safe for concurrent use.
type Scheduler struct {
	mu     sync.Mutex
	credit map[uint8]float64
	tmp    []scored
}

type scored struct {
	id     uint8
	credit float64
	w      float64
}

func New() *Scheduler { return &Scheduler{credit: make(map[uint8]float64)} }

// Eligible filters candidates to the set that may carry traffic now: usable
// primaries, or usable backups if no primary is usable.
func Eligible(cands []Candidate, dst []Candidate) []Candidate {
	dst = dst[:0]
	for _, c := range cands {
		if c.Usable && !c.Backup && !c.Benched && !c.Metered {
			dst = append(dst, c)
		}
	}
	if len(dst) == 0 {
		for _, c := range cands {
			if c.Usable && c.Backup && !c.Benched && !c.Metered {
				dst = append(dst, c)
			}
		}
	}
	// Metered links over budget: failover when nothing else is usable.
	if len(dst) == 0 {
		for _, c := range cands {
			if c.Usable && c.Metered && !c.Benched {
				dst = append(dst, c)
			}
		}
	}
	// Benched links are the last resort: better than dropping the packet.
	if len(dst) == 0 {
		for _, c := range cands {
			if c.Usable {
				dst = append(dst, c)
			}
		}
	}
	return dst
}

// Pick appends the ids of r distinct links chosen from eligible to dst.
func (s *Scheduler) Pick(eligible []Candidate, r int, dst []uint8) []uint8 {
	dst = dst[:0]
	n := len(eligible)
	if n == 0 || r <= 0 {
		return dst
	}
	if r >= n {
		for _, c := range eligible {
			dst = append(dst, c.ID)
		}
		return dst
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tmp = s.tmp[:0]
	var total float64
	for _, c := range eligible {
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		if c.Degrade {
			w *= 0.25
		}
		total += w
		cr := s.credit[c.ID] + w
		s.tmp = append(s.tmp, scored{c.ID, cr, w})
	}
	// Highest credit first; ties broken by id for determinism.
	sort.Slice(s.tmp, func(i, j int) bool {
		if s.tmp[i].credit != s.tmp[j].credit {
			return s.tmp[i].credit > s.tmp[j].credit
		}
		return s.tmp[i].id < s.tmp[j].id
	})
	share := total / float64(r)
	for i := range s.tmp {
		if i < r {
			s.tmp[i].credit -= share
			dst = append(dst, s.tmp[i].id)
		}
		// A link whose weight exceeds 1/r of the total can't be picked more
		// than once per packet; clamp so its credit doesn't grow forever.
		if s.tmp[i].credit > total {
			s.tmp[i].credit = total
		}
		if s.tmp[i].credit < -total {
			s.tmp[i].credit = -total
		}
		s.credit[s.tmp[i].id] = s.tmp[i].credit
	}
	sort.Slice(dst, func(i, j int) bool { return dst[i] < dst[j] })
	return dst
}

// PickFastest appends the ids of the r links with the earliest estimated
// arrival. Degraded links are penalised as if 4x slower.
func (s *Scheduler) PickFastest(eligible []Candidate, r int, dst []uint8) []uint8 {
	dst = dst[:0]
	n := len(eligible)
	if n == 0 || r <= 0 {
		return dst
	}
	if r >= n {
		for _, c := range eligible {
			dst = append(dst, c.ID)
		}
		return dst
	}
	var buf [16]Candidate
	cs := append(buf[:0], eligible...)
	eff := func(c Candidate) time.Duration {
		if c.Degrade {
			return c.Delay*4 + 20*time.Millisecond
		}
		return c.Delay
	}
	sort.Slice(cs, func(i, j int) bool {
		di, dj := eff(cs[i]), eff(cs[j])
		if di != dj {
			return di < dj
		}
		return cs[i].ID < cs[j].ID
	})
	for _, c := range cs[:r] {
		dst = append(dst, c.ID)
	}
	sort.Slice(dst, func(i, j int) bool { return dst[i] < dst[j] })
	return dst
}

// Choose dispatches to the policy's strategy.
func (s *Scheduler) Choose(p *Policy, eligible []Candidate, r int, dst []uint8) []uint8 {
	if p.Strategy == StrategyWeighted {
		return s.Pick(eligible, r, dst)
	}
	return s.PickFastest(eligible, r, dst)
}

// Forget drops credit state for links that no longer exist.
func (s *Scheduler) Forget(id uint8) {
	s.mu.Lock()
	delete(s.credit, id)
	s.mu.Unlock()
}

// PortSet is a set of UDP port ranges, parsed from "3478-3481,5004".
type PortSet []portRange

type portRange struct{ lo, hi uint16 }

func ParsePorts(s string) (PortSet, error) {
	var ps PortSet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		l, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 16)
		if err != nil {
			return nil, err
		}
		h := l
		if isRange {
			h, err = strconv.ParseUint(strings.TrimSpace(hi), 10, 16)
			if err != nil {
				return nil, err
			}
		}
		if h < l {
			l, h = h, l
		}
		ps = append(ps, portRange{uint16(l), uint16(h)})
	}
	return ps, nil
}

func (ps PortSet) Has(p uint16) bool {
	for _, r := range ps {
		if p >= r.lo && p <= r.hi {
			return true
		}
	}
	return false
}

// Inspect extracts scheduling hints from a raw IPv4/IPv6 packet.
func Inspect(pkt []byte) PacketInfo {
	info := PacketInfo{Len: len(pkt)}
	if len(pkt) < 1 {
		return info
	}
	var proto byte
	var l4 []byte
	switch pkt[0] >> 4 {
	case 4:
		if len(pkt) < 20 {
			return info
		}
		ihl := int(pkt[0]&0x0f) * 4
		// Only the first fragment carries ports.
		if ihl < 20 || len(pkt) < ihl || (pkt[6]&0x1f != 0 || pkt[7] != 0) {
			return info
		}
		proto, l4 = pkt[9], pkt[ihl:]
	case 6:
		if len(pkt) < 40 {
			return info
		}
		proto, l4 = pkt[6], pkt[40:]
	default:
		return info
	}
	switch proto {
	case 17: // UDP
		if len(l4) >= 4 {
			info.UDP = true
			info.SrcPort = uint16(l4[0])<<8 | uint16(l4[1])
			info.DstPort = uint16(l4[2])<<8 | uint16(l4[3])
		}
	case 6: // TCP
		if len(l4) >= 14 {
			info.SrcPort = uint16(l4[0])<<8 | uint16(l4[1])
			info.DstPort = uint16(l4[2])<<8 | uint16(l4[3])
			flags := l4[13]
			// SYN, FIN, RST: connection setup/teardown is latency critical.
			info.Priority = flags&0x07 != 0
		}
	case 1, 58: // ICMP / ICMPv6
		info.Priority = true
	}
	return info
}
