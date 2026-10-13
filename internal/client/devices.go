package client

import (
	"net/netip"
	"sort"

	"github.com/rayzorben/net-bond-007/internal/bond"
)

// Device is a machine whose traffic runs through this client: the client
// itself, or (in router mode) a device on the LAN.
type Device struct {
	IP   string `json:"ip"`
	Name string `json:"name"`
	MAC  string `json:"mac,omitempty"`
}

// ConnView is a tracked connection with its owner and a readable host name.
type ConnView struct {
	bond.FlowView
	Host   string `json:"host,omitempty"`
	Device string `json:"device"`
	Local  string `json:"local"` // original source (LAN ip:port) when known
}

type DeviceView struct {
	Device
	Self        bool            `json:"self"`
	TxBps       float64         `json:"txBps"`
	RxBps       float64         `json:"rxBps"`
	TxBytes     uint64          `json:"txBytes"`
	RxBytes     uint64          `json:"rxBytes"`
	Links       []bond.FlowLink `json:"links"`
	Connections []ConnView      `json:"connections"`
}

// natEntry maps a masqueraded connection back to the LAN device that opened it.
type natKey struct {
	proto   uint8
	inside  netip.AddrPort // after masquerade (tunnel ip:port)
	outside netip.AddrPort
}

// Connections groups tracked flows by device.
func (c *Client) Connections() []DeviceView {
	e := c.engine.Load()
	flows := e.Flows()
	nat := lanOrigins()  // platform-specific; empty off Linux
	leases := lanNames() // ip -> Device
	self := Device{Name: c.deviceName()}
	if a := c.ack.Load(); a != nil {
		self.IP = a.ClientIP
	}
	byDev := map[string]*DeviceView{}
	get := func(d Device, isSelf bool) *DeviceView {
		k := d.IP
		if isSelf {
			k = "self"
		}
		v := byDev[k]
		if v == nil {
			v = &DeviceView{Device: d, Self: isSelf}
			byDev[k] = v
		}
		return v
	}
	for _, f := range flows {
		cv := ConnView{FlowView: f, Local: f.Inside}
		if ap, err := netip.ParseAddrPort(f.Outside); err == nil {
			cv.Host = f.Name
			if cv.Host == "" {
				cv.Host = c.names.Lookup(ap.Addr())
			}
		}
		var dv *DeviceView
		if orig, ok := nat[natKey{f.Key.Proto, f.Key.Inside, f.Key.Outside}]; ok {
			d, known := leases[orig.Addr().String()]
			if !known {
				d = Device{IP: orig.Addr().String(), Name: orig.Addr().String()}
			}
			cv.Local = orig.String()
			dv = get(d, false)
		} else {
			dv = get(self, true)
		}
		cv.Device = dv.IP
		dv.TxBps += f.TxBps
		dv.RxBps += f.RxBps
		dv.TxBytes += f.TxBytes
		dv.RxBytes += f.RxBytes
		dv.Links = mergeLinks(dv.Links, f.Links)
		dv.Connections = append(dv.Connections, cv)
	}
	out := make([]DeviceView, 0, len(byDev))
	for _, v := range byDev {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].RxBps+out[i].TxBps, out[j].RxBps+out[j].TxBps
		if ri != rj {
			return ri > rj
		}
		return out[i].RxBytes > out[j].RxBytes
	})
	return out
}

func mergeLinks(acc, add []bond.FlowLink) []bond.FlowLink {
	for _, a := range add {
		found := false
		for i := range acc {
			if acc[i].ID == a.ID {
				acc[i].Copies += a.Copies
				acc[i].Wins += a.Wins
				found = true
			}
		}
		if !found {
			acc = append(acc, a)
		}
	}
	sort.Slice(acc, func(i, j int) bool { return acc[i].ID < acc[j].ID })
	return acc
}
