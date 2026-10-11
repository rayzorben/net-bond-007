// Package reorder restores sequence order for packets that were striped over
// links with different latencies.
//
// TCP reacts badly to heavy reordering (spurious fast-retransmits shrink the
// congestion window), so the receiver briefly holds packets that arrive ahead
// of a gap. If the missing packet does not arrive within the hold time (it was
// lost on every link it was sent on), the gap is skipped and the held packets
// are released. A packet arriving after its gap was skipped is delivered
// immediately: the inner TCP/UDP stack copes with that.
//
// The Buffer is a pure data structure driven by the caller's clock, which
// keeps it deterministic and easy to test.
package reorder

import "time"

const slots = 2048

type entry struct {
	seq      uint64
	pkt      []byte
	deadline time.Time
	used     bool
}

type Stats struct {
	InOrder uint64 // delivered without waiting
	Held    uint64 // held at least briefly, then delivered in order
	Skipped uint64 // gaps given up on (packet lost on all links)
	Late    uint64 // arrived after its gap was skipped
}

// Buffer is not safe for concurrent use; the caller serialises access.
type Buffer struct {
	MaxHold time.Duration // 0 disables reordering (pass-through)

	next  uint64 // next sequence number to release; 0 = uninitialised
	held  int
	ring  [slots]entry
	Stats Stats
}

// Push accepts packet seq (already de-duplicated) and calls deliver for every
// packet that can now be released, in order. Ownership of pkt passes to the
// buffer if it has to be held.
func (b *Buffer) Push(seq uint64, pkt []byte, now time.Time, deliver func([]byte)) {
	if b.MaxHold <= 0 {
		if b.held > 0 {
			b.drainAll(deliver)
		}
		if seq >= b.next {
			b.next = seq + 1
		}
		b.Stats.InOrder++
		deliver(pkt)
		return
	}
	if b.next == 0 {
		b.next = 1 // sequence numbers start at 1
	}
	switch {
	case seq < b.next:
		b.Stats.Late++
		deliver(pkt)
	case seq == b.next:
		b.Stats.InOrder++
		deliver(pkt)
		b.next++
		b.drainConsecutive(deliver)
	default:
		// Too far ahead to fit in the ring: give up on the oldest gaps.
		for seq-b.next >= slots {
			if low := b.lowestHeld(); low != 0 && seq-low >= slots {
				b.skipTo(low, deliver)
				continue
			}
			b.skipTo(seq-slots+1, deliver)
		}
		if seq == b.next {
			b.Stats.InOrder++
			deliver(pkt)
			b.next++
			b.drainConsecutive(deliver)
			return
		}
		e := &b.ring[seq%slots]
		if e.used && e.seq == seq {
			return // duplicate; dedup should have caught it
		}
		*e = entry{seq: seq, pkt: pkt, deadline: now.Add(b.MaxHold), used: true}
		b.held++
	}
}

// Expire releases held packets whose hold time has elapsed.
func (b *Buffer) Expire(now time.Time, deliver func([]byte)) {
	for b.held > 0 {
		low := b.lowestHeld()
		if low == 0 {
			return
		}
		e := &b.ring[low%slots]
		if now.Before(e.deadline) {
			return
		}
		b.skipTo(low, deliver)
	}
}

// NextDeadline returns when Expire next needs to run (zero if nothing held).
func (b *Buffer) NextDeadline() time.Time {
	if b.held == 0 {
		return time.Time{}
	}
	low := b.lowestHeld()
	if low == 0 {
		return time.Time{}
	}
	return b.ring[low%slots].deadline
}

func (b *Buffer) Held() int { return b.held }

func (b *Buffer) skipTo(seq uint64, deliver func([]byte)) {
	if seq > b.next {
		b.Stats.Skipped += seq - b.next
		b.next = seq
	}
	b.drainConsecutive(deliver)
}

func (b *Buffer) drainConsecutive(deliver func([]byte)) {
	for b.held > 0 {
		e := &b.ring[b.next%slots]
		if !e.used || e.seq != b.next {
			return
		}
		pkt := e.pkt
		*e = entry{}
		b.held--
		b.next++
		b.Stats.Held++
		deliver(pkt)
	}
}

func (b *Buffer) drainAll(deliver func([]byte)) {
	for b.held > 0 {
		b.skipTo(b.lowestHeld(), deliver)
	}
}

// lowestHeld scans forward from next for the first held packet. The scan is
// bounded by the ring size and in practice ends after a few slots.
func (b *Buffer) lowestHeld() uint64 {
	if b.held == 0 {
		return 0
	}
	for s := b.next; s < b.next+slots; s++ {
		e := &b.ring[s%slots]
		if e.used && e.seq == s {
			return s
		}
	}
	return 0
}

// WillHold reports whether Push(seq) would retain the packet (so the caller
// must hand over a buffer it won't reuse).
func (b *Buffer) WillHold(seq uint64) bool {
	if b.MaxHold <= 0 {
		return false
	}
	next := b.next
	if next == 0 {
		next = 1
	}
	return seq > next
}
