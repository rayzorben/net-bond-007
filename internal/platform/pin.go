package platform

import "net/netip"

// Connection pinning (router mode): every new LAN connection is tagged with
// the path it started on (the tunnel, or one physical link while the bond is
// bypassed), and policy routing keeps it there. Switching between the bond
// and going direct then only moves new connections; a stream that started on
// one path keeps its public IP instead of being reset mid-playback.

// PinSpec is the full desired pinning state; ApplyPins replaces the previous.
type PinSpec struct {
	LAN     []string  // devices whose new connections are pinned (e.g. br-lan)
	Current int       // path index that new connections take
	Paths   []PinPath // every path that may still carry pinned connections
	// Calls: new LAN UDP connections to or from these ports (nft set
	// elements, "3478-3481, 50000-50059") take RealtimePath instead of
	// Current, so a call rides the bond (mirrored on every link) even while
	// bulk traffic goes direct. Empty or RealtimePath 0: no exception.
	Realtime     string
	RealtimePath int
}

// PinPath is one way out. Index 1..MaxPinPaths identifies it in conntrack
// marks and routing tables.
type PinPath struct {
	Index int
	Dev   string
	GW    netip.Addr // invalid: device route
	// Dead paths can no longer carry their connections (the server went
	// silent, or the link is down). Their TCP connections are reset and UDP
	// is rejected, so apps reconnect at once over the current path instead of
	// hanging until their own timeouts.
	Dead bool
}

const MaxPinPaths = 15

// Conntrack/packet mark bits used for pinning. GL.iNet firmware uses 0xf000
// and Tailscale 0xff0400, so the pin index lives in bits 24-27.
const (
	pinMask  = 0x0f000000
	pinShift = 24
)

func pinMark(idx int) uint32 { return uint32(idx) << pinShift }
