package client

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

// Port forwarding: the server opens public ports (see server/forward.go)
// and relays inbound connections to our tunnel address; in router mode the
// router hands them on to the LAN device (platform/portmap.go).

type portMapState struct {
	last    string // signature of the applied spec (guarded by tunMu)
	on      bool
	err     string
	checked time.Time // last fw4 check
}

type fwdStatus struct {
	st proto.ForwardStatus
	at time.Time
}

// wireForwards are the ports to ask the server for.
func wireForwards(cfg *config.Client) []proto.Forward {
	var out []proto.Forward
	for _, f := range cfg.Forwards {
		if !f.Disabled {
			out = append(out, proto.Forward{Proto: f.Proto, Port: f.Port})
		}
	}
	return out
}

func (c *Client) onForwardStatus(st proto.ForwardStatus) {
	c.fwd.Store(&fwdStatus{st: st, at: time.Now()})
}

func (c *Client) portMapWanted(cfg *config.Client) bool {
	return cfg.Tunnel.RouterMode && platform.PortMapSupported() && len(wireForwards(cfg)) > 0
}

// refreshPortMapLocked brings the router's forwarding rules in line with the
// configuration. c.tunMu must be held.
func (c *Client) refreshPortMapLocked(cfg *config.Client) {
	if c.tun == nil || !c.portMapWanted(cfg) || !c.tunSpec.Addr.IsValid() {
		c.clearPortMapLocked()
		return
	}
	spec := platform.PortMapSpec{Tun: c.tunSpec.Name, TunIP: c.tunSpec.Addr.Addr(), LAN: platform.LANDevices()}
	for _, f := range cfg.Forwards {
		dev, err := netip.ParseAddr(f.Device)
		if f.Disabled || err != nil {
			continue
		}
		for _, p := range f.Protos() {
			spec.Rules = append(spec.Rules, platform.PortMapRule{Proto: p, Port: f.Port, Device: dev, DevicePort: f.ToPort()})
		}
	}
	sig := fmt.Sprintf("%+v", spec)
	if c.portmap.on && sig == c.portmap.last {
		// A firewall reload (GL.iNet does them often) drops fw4's accept rule.
		if time.Since(c.portmap.checked) > 10*time.Second {
			c.portmap.checked = time.Now()
			if err := platform.EnsurePortMapFirewall(spec.Tun); err != nil {
				c.log.Warn("port forwarding: cannot open the firewall", "err", err)
			}
		}
		return
	}
	if err := platform.ApplyPortMap(spec); err != nil {
		c.log.Error("port forwarding: cannot install rules", "err", err)
		c.portmap.err, c.portmap.last, c.portmap.on = err.Error(), "", true
		return
	}
	c.log.Info("port forwarding rules installed", "forwards", len(spec.Rules), "tunnel", spec.TunIP)
	c.portmap = portMapState{last: sig, on: true, checked: time.Now()}
}

func (c *Client) clearPortMapLocked() {
	if c.portmap.on {
		platform.ClearPortMap()
		c.log.Info("port forwarding rules removed")
	}
	c.portmap = portMapState{}
}

// ForwardView is one forward as the dashboard shows it.
type ForwardView struct {
	config.Forward
	Public string `json:"public"` // host:port to give out
	// State: "open" (works end to end), "waiting" (no answer from the server
	// yet), "off", or the server's refusal ("taken", "outside", "error").
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// ServerState is the server's own answer, whatever the router's state.
	ServerState string `json:"serverState,omitempty"`
}

type ForwardsView struct {
	Forwards []ForwardView `json:"forwards"`
	// What the server allows (empty until it has answered).
	ServerPorts   string   `json:"serverPorts"`
	ServerEnabled bool     `json:"serverEnabled"`
	ServerAnswer  bool     `json:"serverAnswer"`
	PublicHost    string   `json:"publicHost"`
	Supported     bool     `json:"supported"`
	Problem       string   `json:"problem,omitempty"` // why nothing can work right now
	Devices       []Device `json:"devices"`
}

func (c *Client) Forwards() ForwardsView {
	cfg := c.cfg.Get()
	v := ForwardsView{Forwards: []ForwardView{}, Devices: []Device{}, Supported: cfg.Tunnel.RouterMode && platform.PortMapSupported()}
	host, _, _ := net.SplitHostPort(cfg.Server.Address)
	c.mu.Lock()
	if c.server.IsValid() {
		host = c.server.Addr().String()
	}
	c.mu.Unlock()
	v.PublicHost = host

	var st *proto.ForwardStatus
	if fs := c.fwd.Load(); fs != nil && time.Since(fs.at) < 20*time.Second {
		st = &fs.st
		v.ServerPorts, v.ServerEnabled, v.ServerAnswer = st.Ports, st.Enabled, true
	}
	c.tunMu.Lock()
	tunUp, pmErr := c.tun != nil, c.portmap.err
	c.tunMu.Unlock()
	switch {
	case !v.Supported:
		v.Problem = "Port forwarding needs router mode on a Linux router."
	case !cfg.Server.Enabled:
		v.Problem = "Bonding is off, so the server isn't connected."
	case c.ack.Load() == nil:
		v.Problem = "Not connected to the server."
	case !tunUp:
		v.Problem = "The tunnel is down."
	case pmErr != "":
		v.Problem = "The router's forwarding rules failed: " + pmErr
	case st == nil:
		v.Problem = "The server hasn't answered yet (it may run an older version without port forwarding)."
	case !st.Enabled:
		v.Problem = "Port forwarding is turned off on the server."
	}
	for _, f := range cfg.Forwards {
		fv := ForwardView{Forward: f, Public: net.JoinHostPort(host, fmt.Sprint(f.Port)), State: "waiting"}
		switch {
		case f.Disabled:
			fv.State = "off"
		case st != nil:
			for _, s := range st.Forwards {
				if s.Port == f.Port && s.Proto == f.Proto {
					fv.State, fv.Error, fv.ServerState = s.State, s.Error, s.State
				}
			}
		}
		if fv.State == "open" && v.Problem != "" {
			fv.State = "waiting"
		}
		v.Forwards = append(v.Forwards, fv)
	}
	for _, d := range lanNames() {
		v.Devices = append(v.Devices, d)
	}
	sort.Slice(v.Devices, func(i, j int) bool {
		a, _ := netip.ParseAddr(v.Devices[i].IP)
		b, _ := netip.ParseAddr(v.Devices[j].IP)
		return a.Less(b)
	})
	return v
}
