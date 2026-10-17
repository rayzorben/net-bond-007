// Package dedup implements the "first copy wins" filter. When the same
// sequence number arrives over several links, only the first arrival is
// accepted; every later copy is reported as a duplicate and dropped.
//
// It is a sliding bitmap window in the style of the IPsec / WireGuard replay
// window: O(1) per packet with a fixed memory footprint.
package dedup

import "sync"

// WindowBits is how far behind the newest sequence number a packet may arrive
// and still be accepted. 16384 packets is ~23MB of 1400-byte packets, far more
// than any realistic latency spread between links.
const WindowBits = 1 << 14

const words = WindowBits / 64

type Result uint8

const (
	Fresh     Result = iota // first time we've seen this sequence number
	Duplicate               // already accepted via another (faster) link
	TooOld                  // fell behind the window; treat as duplicate
)

// Window tracks which sequence numbers have been seen. Sequence numbers start
// at 1; 0 is never valid. Safe for concurrent use.
type Window struct {
	mu   sync.Mutex
	top  uint64 // highest sequence number seen
	bits [words]uint64
}

// Check reports whether seq is fresh and marks it as seen.
func (w *Window) Check(seq uint64) Result {
	if seq == 0 {
		return TooOld
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if seq > w.top {
		w.advance(seq)
		w.set(seq)
		return Fresh
	}
	if w.top-seq >= WindowBits {
		return TooOld
	}
	if w.isSet(seq) {
		return Duplicate
	}
	w.set(seq)
	return Fresh
}

// Seen reports whether seq was already accepted (or fell behind the window),
// without marking it.
func (w *Window) Seen(seq uint64) bool {
	if seq == 0 {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq > w.top {
		return false
	}
	return w.top-seq >= WindowBits || w.isSet(seq)
}

// Reset forgets all history (used when a session restarts).
func (w *Window) Reset() {
	w.mu.Lock()
	w.top = 0
	w.bits = [words]uint64{}
	w.mu.Unlock()
}

// advance moves the window so that seq becomes top, clearing the bits of
// every sequence number that is newly inside the window.
func (w *Window) advance(seq uint64) {
	diff := seq - w.top
	if diff >= WindowBits {
		w.bits = [words]uint64{}
	} else {
		for s := w.top + 1; s <= seq; s++ {
			// Clear whole words when we can for speed.
			idx := (s / 64) % words
			if s%64 == 0 && seq-s >= 63 {
				w.bits[idx] = 0
				s += 63
				continue
			}
			w.bits[idx] &^= 1 << (s % 64)
		}
	}
	w.top = seq
}

func (w *Window) set(seq uint64)        { w.bits[(seq/64)%words] |= 1 << (seq % 64) }
func (w *Window) isSet(seq uint64) bool { return w.bits[(seq/64)%words]&(1<<(seq%64)) != 0 }
