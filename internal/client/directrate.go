package client

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/metrics"
)

// DirectView is the traffic going straight out a physical link, outside the
// bond (guard / single-link direct, or fail-open). The kernel forwards it, so
// the client never sees the packets: it is the interface's own counters minus
// the bond's wire traffic on that interface. It also includes the router's
// own non-bond traffic (DNS upstreams, tailscale, speed tests).
type DirectView struct {
	Via   string  `json:"via"`
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
}

// wireOverhead is the per-packet bytes the interface counts that a bond
// frame length does not: Ethernet (14) + IPv4 (20) + UDP (8).
const wireOverhead = 42

type directMeter struct {
	mu             sync.Mutex
	dev            string
	ifRx, ifTx     uint64 // last interface counters
	bondRx, bondTx uint64 // last bond wire bytes on dev (incl. overhead)
	outRx, outTx   uint64 // accumulated non-bond bytes
	rx, tx         metrics.Rate
	readCounters   func(dev string) (rx, tx uint64, ok bool)
}

// sample takes one reading. dev is the direct interface ("" = not direct);
// bondRx/bondTx are the bond's cumulative wire bytes on it.
func (m *directMeter) sample(dev string, bondRx, bondTx uint64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if dev == "" {
		m.dev = ""
		return
	}
	read := m.readCounters
	if read == nil {
		read = sysfsCounters
	}
	ifRx, ifTx, ok := read(dev)
	if !ok {
		m.dev = ""
		return
	}
	if dev != m.dev {
		m.dev = dev
		m.ifRx, m.ifTx, m.bondRx, m.bondTx = ifRx, ifTx, bondRx, bondTx
		m.outRx, m.outTx = 0, 0
		m.rx, m.tx = metrics.Rate{}, metrics.Rate{}
		m.rx.Sample(0, now)
		m.tx.Sample(0, now)
		return
	}
	m.outRx += outside(ifRx, m.ifRx, bondRx, m.bondRx)
	m.outTx += outside(ifTx, m.ifTx, bondTx, m.bondTx)
	m.ifRx, m.ifTx, m.bondRx, m.bondTx = ifRx, ifTx, bondRx, bondTx
	m.rx.Sample(m.outRx, now)
	m.tx.Sample(m.outTx, now)
}

// outside is the interface delta not explained by the bond's delta. Bond
// counters restart when a link reconnects; then all of its bytes are new.
func outside(ifNow, ifPrev, bondNow, bondPrev uint64) uint64 {
	if ifNow < ifPrev {
		return 0 // interface counters reset (device re-created)
	}
	d := ifNow - ifPrev
	bd := bondNow
	if bondNow >= bondPrev {
		bd = bondNow - bondPrev
	}
	if bd >= d {
		return 0
	}
	return d - bd
}

func (m *directMeter) view() *DirectView {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dev == "" {
		return nil
	}
	return &DirectView{Via: m.dev, RxBps: m.rx.BPS, TxBps: m.tx.BPS}
}

func sysfsCounters(dev string) (rx, tx uint64, ok bool) {
	dir := filepath.Join("/sys/class/net", dev, "statistics")
	read := func(name string) (uint64, bool) {
		b, err := os.ReadFile(filepath.Join(dir, name))
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

// directTick feeds the direct meter once per second.
func (c *Client) directTick(now time.Time) {
	dev := c.bypassVia()
	if dev == "" {
		dev = c.directViaName()
	}
	var bondRx, bondTx uint64
	if dev != "" {
		c.mu.Lock()
		for _, l := range c.links {
			if l.iface.Name != dev || l.link == nil {
				continue
			}
			bondRx += l.link.Rx.Bytes.Load() + wireOverhead*l.link.Rx.Packets.Load()
			bondTx += l.link.Tx.Bytes.Load() + wireOverhead*l.link.Tx.Packets.Load()
		}
		c.mu.Unlock()
	}
	c.direct.sample(dev, bondRx, bondTx, now)
}
