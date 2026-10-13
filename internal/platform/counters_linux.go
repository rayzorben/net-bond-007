package platform

import (
	"os"
	"strconv"
	"strings"
)

// IfaceCounters returns an interface's total received and sent bytes, all
// traffic included (not only the bond's).
func IfaceCounters(name string) (rx, tx uint64, ok bool) {
	read := func(f string) (uint64, bool) {
		b, err := os.ReadFile("/sys/class/net/" + name + "/statistics/" + f)
		if err != nil {
			return 0, false
		}
		v, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		return v, err == nil
	}
	rx, ok1 := read("rx_bytes")
	tx, ok2 := read("tx_bytes")
	return rx, tx, ok1 && ok2
}
