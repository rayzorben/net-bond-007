//go:build !linux

package platform

// IfaceCounters is not available here; callers fall back to the bond's own
// per-link counters.
func IfaceCounters(name string) (rx, tx uint64, ok bool) { return 0, 0, false }
