module gvisor.dev/gvisor

// A trimmed copy of upstream gVisor v0.0.0-20261010064756-cd110d9dec04: only the packages
// Net-Bond-007 builds, plus the patches listed in PATCHES.md.

go 1.27.1

require (
	github.com/google/btree v1.1.2
	golang.org/x/exp v0.0.0-20250711185948-6ae5c78190dc
	golang.org/x/sys v0.49.0
	golang.org/x/time v0.15.0
)
