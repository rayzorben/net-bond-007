package client

import (
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
)

// udpIPOverhead approximates the IP+UDP header bytes each frame costs on the
// carrier's meter (our counters see only the UDP payload).
const udpIPOverhead = 28

// meter counts usage on metered links and sets each one's share: the
// configured percentage, or 0 (failover only) once the monthly cap is hit.
// A metered link is never disabled; it always carries traffic when nothing
// else can.
func (c *Client) meter(now time.Time, cfg *config.Client, links []*clink) {
	changed := false
	for _, l := range links {
		name := l.iface.Name
		lc := cfg.Links[name]
		wire := l.link.Tx.Bytes.Load() + l.link.Rx.Bytes.Load() +
			udpIPOverhead*(l.link.Tx.Packets.Load()+l.link.Rx.Packets.Load())
		delta := wire - l.meterSeen
		if wire < l.meterSeen {
			delta = wire
		}
		l.meterSeen = wire
		if !lc.Metered {
			if l.link.Metered() {
				l.link.SetMetered(false, 0)
				changed = true
			}
			continue
		}
		used := c.usage.Add(name, delta, now, lc.CycleDay)
		share := lc.SharePct / 100
		over := lc.MonthlyCapGB > 0 && float64(used) >= lc.MonthlyCapGB*1e9
		if over {
			share = 0
		}
		if !l.link.Metered() || l.link.MeterShare() != max(0, min(1, share)) {
			if over && l.link.MeterShare() > 0 {
				c.log.Warn("metered link reached its monthly cap; failover only", "link", name, "usedGB", float64(used)/1e9)
			}
			l.link.SetMetered(true, share)
			changed = true
		}
	}
	c.usage.Save(false)
	if changed {
		c.policySent.Store(0)
	}
}

// meteredShares is the per-link share sent to the server (downloads).
func (c *Client) meteredShares() map[uint8]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var m map[uint8]float64
	for _, l := range c.links {
		if l.link.Metered() {
			if m == nil {
				m = map[uint8]float64{}
			}
			m[l.id] = l.link.MeterShare()
		}
	}
	return m
}
