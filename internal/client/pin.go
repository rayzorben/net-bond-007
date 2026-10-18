package client

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

// Connection pinning (router mode, Linux): see platform/pin.go. Path 1 is the
// tunnel; each physical link the bond has gone direct over gets its own index
// for the life of the process, so connections started over it stay there
// after the bond switches back.

const pinTunnel = 1

type pinState struct {
	idx       map[string]int       // direct device -> path index (guarded by tunMu)
	downSince map[string]time.Time // direct device -> unusable since
	last      string               // signature of the last applied spec
	on        bool
}

// pinDeadAfter is how long a link must stay unusable before connections
// pinned to it are reset: Starlink drops out for a second or two all the
// time, and those streams survive if left alone.
const pinDeadAfter = 15 * time.Second

// pinsWanted: pinning sends every new LAN connection down the current path,
// so it only makes sense while the tunnel carries all LAN traffic (full
// tunnel). With split routes, or none (testing the bond with traffic bound
// to its interface), it would pull the whole LAN into the tunnel.
func (c *Client) pinsWanted(cfg *config.Client) bool {
	return cfg.Tunnel.RouterMode && cfg.Tunnel.PinConnections && cfg.Tunnel.FullTunnel && platform.PinsSupported()
}

// refreshPinsLocked brings pinning in line with the current tunnel spec and
// link health. c.tunMu must be held and the tunnel configured.
func (c *Client) refreshPinsLocked(cfg *config.Client) {
	if c.tun == nil || !c.pinsWanted(cfg) {
		c.clearPinsLocked()
		return
	}
	spec := c.tunSpec
	if c.pins.idx == nil {
		c.pins.idx, c.pins.downSince = map[string]int{}, map[string]time.Time{}
	}
	if spec.Direct && c.pins.idx[spec.DirectDev] == 0 {
		if n := len(c.pins.idx) + 2; n <= platform.MaxPinPaths {
			c.pins.idx[spec.DirectDev] = n
		}
	}
	usable := map[string]bool{}
	c.mu.Lock()
	for _, l := range c.links {
		if !l.relayTo.IsValid() {
			usable[l.iface.Name] = l.link.State().Usable()
		}
	}
	bypass := c.fail.via
	c.mu.Unlock()

	ps := platform.PinSpec{LAN: platform.LANDevices(), Current: pinTunnel}
	// While fail-open bypasses a silent server, connections pinned to the
	// tunnel are dead: reset them so apps reconnect over the bypass.
	ps.Paths = append(ps.Paths, platform.PinPath{Index: pinTunnel, Dev: spec.Name, Dead: bypass != ""})
	devs := make([]string, 0, len(c.pins.idx))
	for d := range c.pins.idx {
		devs = append(devs, d)
	}
	slices.Sort(devs)
	now := time.Now()
	for _, d := range devs {
		current := spec.Direct && spec.DirectDev == d
		// Probes measure the path to the server, so while it is silent
		// (fail-open) they say nothing about a direct link; and the current
		// path is never reset, or new connections would be too.
		down := !usable[d] && bypass == "" && !current
		switch {
		case !down:
			delete(c.pins.downSince, d)
		case c.pins.downSince[d].IsZero():
			c.pins.downSince[d] = now
		}
		ps.Paths = append(ps.Paths, platform.PinPath{
			Index: c.pins.idx[d], Dev: d, GW: platform.DefaultGateway(d),
			Dead: down && now.Sub(c.pins.downSince[d]) >= pinDeadAfter,
		})
		if current {
			ps.Current = c.pins.idx[d]
		}
	}
	sig := fmt.Sprintf("%+v", ps)
	if c.pins.on && sig == c.pins.last {
		return
	}
	if err := platform.ApplyPins(ps); err != nil {
		c.log.Error("cannot pin connections to their path; mode switches will reset them", "err", err)
		c.pins.last = ""
		return
	}
	if sig != c.pins.last {
		var dead []string
		for _, p := range ps.Paths {
			if p.Dead {
				dead = append(dead, p.Dev)
			}
		}
		c.log.Info("connections pinned to their path", "new", pathName(ps, ps.Current), "lan", strings.Join(ps.LAN, ","), "resetting", strings.Join(dead, ","))
	}
	c.pins.last, c.pins.on = sig, true
}

func pathName(ps platform.PinSpec, idx int) string {
	for _, p := range ps.Paths {
		if p.Index == idx {
			return p.Dev
		}
	}
	return ""
}

func (c *Client) clearPinsLocked() {
	if c.pins.on {
		platform.ClearPins()
		c.pins.on, c.pins.last = false, ""
	}
}

// pinTick re-checks link health once per second, so connections pinned to a
// link that just died are reset promptly.
func (c *Client) pinTick(cfg *config.Client) {
	if !c.tunMu.TryLock() {
		return // a reconfiguration is running; it refreshes pins itself
	}
	defer c.tunMu.Unlock()
	if c.pins.on || (c.tun != nil && c.pinsWanted(cfg)) {
		c.refreshPinsLocked(cfg)
	}
}
