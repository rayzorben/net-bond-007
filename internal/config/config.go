// Package config defines every user-tunable setting for client and server.
//
// Design rule: there are no hidden settings. Every field carries struct-tag
// metadata (label, help, range, enum, advanced) and the web UI renders its
// forms from the generated schema, so a setting cannot exist without being
// visible and editable. Changes are applied live by subscribers of Store.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/rayzorben/net-bond-007/internal/sched"
)

// ---------------------------------------------------------------- client

type Client struct {
	Name       string          `json:"name" label:"Device name" help:"How this device is shown on the server dashboard."`
	Server     ServerEndpoint  `json:"server" label:"Server" help:"Where the Net-Bond-007 server is running."`
	Bonding    Bonding         `json:"bonding" label:"Bonding" help:"How traffic is spread over your connections."`
	Health     Health          `json:"health" label:"Health monitoring" help:"How links are probed and judged."`
	Tunnel     Tunnel          `json:"tunnel" label:"Virtual network" help:"The virtual adapter your OS sends traffic into."`
	Interfaces Interfaces      `json:"interfaces" label:"Interface discovery" help:"Which network interfaces become bonded links."`
	SpeedTest  SpeedTest       `json:"speedTest" label:"Speed tests" help:"Measure each connection's real bandwidth now and then, only while it is idle."`
	Relay      Relay           `json:"relay" label:"Phone relays" help:"Phones on your network running the Net-Bond-007 Relay app share their cellular data as bonded links."`
	Links      map[string]Link `json:"links" schema:"-"`
	// Version of the saved settings format, used to migrate old defaults.
	Version int `json:"configVersion" schema:"-"`
	UI      UI  `json:"ui" label:"Dashboard" help:"This web interface."`
	Log     Log `json:"log" label:"Logging"`
}

type ServerEndpoint struct {
	Address string `json:"address" label:"Server address" help:"host:port of the server's UDP listener, e.g. bond.example.com:7007."`
	Token   string `json:"token" label:"Access token" help:"Must match the server's token." secret:"1"`
	Enabled bool   `json:"enabled" label:"Connect" help:"Turn bonding on or off without quitting the app."`
}

type Bonding struct {
	Mode                   string  `json:"mode" label:"Bonding mode" help:"Smart: mirror on 1-2 links, stripe with redundancy on 3+. Mirror: every packet on every link. Speed: no duplication. Custom: fixed redundancy." enum:"smart=Smart,mirror=Mirror (max reliability),speed=Speed (max throughput),custom=Custom redundancy"`
	Strategy               string  `json:"strategy" label:"Link selection" help:"Fastest arrival: each copy goes where it will arrive soonest, so fast links fill first (best for TCP). Round robin: copies rotate over links in proportion to their weights." enum:"latency=Fastest arrival,weighted=Weighted round robin"`
	Redundancy             int     `json:"redundancy" label:"Copies per packet" help:"With Smart (3+ links) or Custom: how many different links carry each packet." min:"1" max:"8"`
	SmallPacketMirrorBytes int     `json:"smallPacketMirrorBytes" label:"Mirror small packets up to" help:"Small packets (ACKs, DNS, keystrokes) are cheap to duplicate and latency critical. 0 disables." min:"0" max:"1500" unit:"bytes" adv:"1"`
	RealtimeMirror         bool    `json:"realtimeMirror" label:"Mirror real-time media" help:"Send voice/video calls (Teams, Meet, WebRTC) on every link for glitch-free calls."`
	RealtimePorts          string  `json:"realtimePorts" label:"Real-time UDP ports" help:"Comma-separated ports or ranges treated as real-time media." adv:"1"`
	ReorderMode            string  `json:"reorderMode" label:"Reorder buffer" help:"Hold early packets briefly so TCP sees them in order. Auto sizes the hold from the latency spread between links." enum:"auto=Auto,fixed=Fixed,off=Off" adv:"1"`
	ReorderMs              int     `json:"reorderMs" label:"Max reorder hold" help:"Upper bound (auto) or exact (fixed) hold time. With packet recovery on, auto holds about one round trip so repairs arrive in order. Packets in order are never delayed." min:"1" max:"2000" unit:"ms" adv:"1"`
	Retransmit             bool    `json:"retransmit" label:"Recover lost packets" help:"When a packet is lost on every path, ask for it again inside the tunnel within about one round trip, before apps (TCP) notice and slow down. Big win on lossy links like Starlink."`
	AutoScale              bool    `json:"autoScale" label:"Automatic scaling" help:"Smart mode: track each link's reliability over time. While links are flaky, every packet is mirrored across them; as they prove reliable, traffic is load-balanced for speed; a link that keeps failing next to a good one is benched (still monitored) until it recovers."`
	TargetDeliveryPct      float64 `json:"targetDeliveryPct" label:"Delivery target" help:"Automatic scaling adds copies until the chance of losing every copy of a packet is below 100% minus this." min:"90" max:"99.999" step:"0.1" unit:"%" adv:"1"`
	BenchBelowPct          float64 `json:"benchBelowPct" label:"Stop using a link below" help:"A link whose reliability falls below this stops carrying traffic while a good link exists. It keeps being monitored." min:"1" max:"99" step:"1" unit:"%"`
	RejoinAbovePct         float64 `json:"rejoinAbovePct" label:"Bring a link back above" help:"A stopped link rejoins once its reliability climbs back above this (must be higher than the stop threshold, so links don't bounce)." min:"2" max:"100" step:"1" unit:"%"`
	ReliabilityFallSec     int     `json:"reliabilityFallSec" label:"Reliability: react to failures within" help:"How quickly a link's reliability drops when it stops delivering." min:"1" max:"600" unit:"s" adv:"1"`
	ReliabilityRiseSec     int     `json:"reliabilityRiseSec" label:"Reliability: earn trust back over" help:"How slowly a link's reliability recovers once it works again." min:"1" max:"3600" unit:"s" adv:"1"`
	DirectWhenSingle       bool    `json:"directWhenSingle" label:"Go direct with one link" help:"When only one reliable link remains, send traffic straight out of it instead of through the tunnel (no detour via the server, never slower than bonding off). In router mode, connections already open stay on the path they started on."`
	DirectAfterSec         int     `json:"directAfterSec" label:"Go direct after" help:"Only one reliable link for this long before leaving the tunnel." min:"1" max:"600" unit:"s" adv:"1"`
	DirectGuard            bool    `json:"directGuard" label:"Never slower than direct" help:"Every so often, download a test file through the bond and directly over your best link. If the bond is clearly slower, new connections go direct until a later test shows the bond winning again. A direct link that fails sends traffic back to the bond at once."`
	DirectGuardMinutes     int     `json:"directGuardMinutes" label:"Compare bond and direct every" help:"How often the never-slower-than-direct check runs (it also runs a few minutes after links change)." min:"5" max:"1440" unit:"min" adv:"1"`
	BondAfterSec           int     `json:"bondAfterSec" label:"Re-bond after" help:"Two or more reliable links for this long before going back through the tunnel." min:"1" max:"3600" unit:"s" adv:"1"`
	CopiesDownAfterSec     int     `json:"copiesDownAfterSec" label:"Reduce copies after" help:"Fewer copies only once links have stayed reliable this long (more copies apply immediately)." min:"1" max:"600" unit:"s" adv:"1"`
	Shaping                bool    `json:"shaping" label:"Smooth bursts" help:"Send on each link at about the rate it actually delivers instead of in line-rate bursts that overflow small buffers (e.g. satellite downlinks)."`
	FEC                    bool    `json:"fec" label:"Loss protection (FEC)" help:"On links that keep losing packets (Starlink), also send a small XOR parity packet per group so a lost packet is rebuilt at once instead of waiting a round trip for a repair, which stalls TCP. Sized automatically from measured loss: about 3% extra data at 0.2% loss; off on clean links."`
	RepairPriority         bool    `json:"repairPriority" label:"Repairs jump the queue" help:"Send recovered packets ahead of data already waiting on a link. Behind a deep queue a repair arrives after the receiver has given up on it." adv:"1"`
	StormRepair            bool    `json:"stormRepair" label:"Route repairs around loss storms" help:"When a link is losing many packets at once (Starlink around some satellite handovers), send repairs over another link instead of back into the storm." adv:"1"`
	OutageReports          bool    `json:"outageReports" label:"Report outages instantly" help:"When a busy link goes silent for 50 ms while another still works, tell the other end at once so it resends what was lost there over the other link, instead of waiting for the outage to end." adv:"1"`
	LossTolerantTCP        bool    `json:"lossTolerantTCP" label:"Loss-tolerant TCP on the server (experimental)" help:"Experimental. The server's TCP toward you keeps its window at what the path delivers instead of cutting it on every loss (BBR-style). Lossy links like Starlink otherwise hold it far below what the link can carry. Off by default until it reliably beats the standard behaviour." adv:"1"`
	QueueDepth             int     `json:"queueDepth" label:"Per-link queue" help:"Packets buffered per link before it is considered congested." min:"16" max:"8192" unit:"packets" adv:"1"`
	LatencyWeighting       bool    `json:"latencyWeighting" label:"Favour low latency" help:"Automatic weights consider latency as well as bandwidth." adv:"1"`
	Pacing                 bool    `json:"pacing" label:"Adaptive link pacing" help:"Learn each link's real capacity (backs off when its latency rises or it drops packets) and never feed it more. Keeps slow links from drowning in duplicates."`
	PacingDelayMs          int     `json:"pacingDelayMs" label:"Pacing: queue delay limit" help:"Back off a link when its latency rises this far above its best (or half its base RTT, if larger)." min:"2" max:"1000" unit:"ms" adv:"1"`
	PacingLossPct          int     `json:"pacingLossPct" label:"Pacing: loss limit" help:"Back off a link when probe loss exceeds this. Kept high because Wi-Fi and cellular lose packets at random." min:"1" max:"100" unit:"%" adv:"1"`
	PacingStartMbps        int     `json:"pacingStartMbps" label:"Pacing start rate" help:"Initial per-link rate before the pacer has learned the link." min:"1" max:"10000" unit:"Mbit/s" adv:"1"`
}

type Health struct {
	ProbeIntervalMs int     `json:"probeIntervalMs" label:"Probe interval" help:"How often each link is pinged." min:"50" max:"5000" unit:"ms" adv:"1"`
	ProbeTimeoutMs  int     `json:"probeTimeoutMs" label:"Probe timeout" help:"A probe without reply after this long counts as lost." min:"100" max:"10000" unit:"ms" adv:"1"`
	LossWindow      int     `json:"lossWindow" label:"Loss window" help:"Number of recent probes used to compute loss." min:"5" max:"120" unit:"probes" adv:"1"`
	DegradedRTTMs   int     `json:"degradedRttMs" label:"Degraded above latency" help:"Links slower than this get less traffic." min:"10" max:"5000" unit:"ms"`
	DegradedLossPct float64 `json:"degradedLossPct" label:"Degraded above loss" min:"0" max:"100" step:"0.5" unit:"%"`
	DownLossPct     float64 `json:"downLossPct" label:"Down above loss" help:"Links losing more than this are taken out of rotation." min:"1" max:"100" unit:"%"`
	DownAfterMs     int     `json:"downAfterMs" label:"Down after silence" help:"A link with no replies for this long is marked down." min:"200" max:"30000" unit:"ms"`
	HistorySeconds  int     `json:"historySeconds" label:"History length" help:"How much per-second history the dashboard keeps." min:"30" max:"3600" unit:"s" adv:"1"`
}

type Tunnel struct {
	InterfaceName  string `json:"interfaceName" label:"Adapter name" help:"Name of the virtual adapter (macOS always uses utunN)." adv:"1"`
	MTU            int    `json:"mtu" label:"MTU" help:"Packet size inside the tunnel. Must leave room for the 16-byte Net-Bond-007 header plus IP/UDP headers on every link." min:"576" max:"9000" unit:"bytes" adv:"1"`
	FullTunnel     bool   `json:"fullTunnel" label:"Send all traffic" help:"Route all internet traffic through the bond. Turn off to bond only the networks listed below."`
	Routes         string `json:"routes" label:"Bonded networks" help:"Comma-separated CIDRs routed through the bond when 'Send all traffic' is off."`
	DNSOverride    bool   `json:"dnsOverride" label:"Bond DNS" help:"Point system DNS at the server so lookups are bonded too."`
	RouterMode     bool   `json:"routerMode" label:"Router mode" help:"Masquerade traffic from LAN devices into the bond (OpenWrt / Linux gateways)."`
	FailOpen       bool   `json:"failOpen" label:"Stay online if the server is down" help:"When the server stops answering on every link, send traffic straight out of your best connection instead of into a dead tunnel. Bonding resumes by itself once the server is back."`
	PinConnections bool   `json:"pinConnections" label:"Keep connections on their path" help:"Router mode: a connection stays on the path it started on (the bond, or going direct) until it ends, so switching between them only moves new connections and streams don't restart."`
	FailOpenSec    int    `json:"failOpenSec" label:"Bypass after" help:"How long the server must be silent on every link before bypassing the tunnel." min:"2" max:"300" unit:"s" adv:"1"`
}

type Interfaces struct {
	AutoAdd bool   `json:"autoAdd" label:"Bond new interfaces automatically" help:"Plug in a phone or join Wi-Fi and it joins the bond."`
	Exclude string `json:"exclude" label:"Ignore interfaces" help:"Comma-separated name patterns (* wildcard) never used as links." adv:"1"`
	ScanMs  int    `json:"scanMs" label:"Scan interval" help:"How often to look for added or removed interfaces." min:"500" max:"60000" unit:"ms" adv:"1"`
}

// Link holds per-interface overrides, keyed by interface name.
type Link struct {
	Disabled bool    `json:"disabled,omitempty"`
	Backup   bool    `json:"backup,omitempty"`  // only used when no primary link is up
	Weight   float64 `json:"weight,omitempty"`  // 0 = automatic
	Label    string  `json:"label,omitempty"`   // friendly name
	MaxMbps  float64 `json:"maxMbps,omitempty"` // capacity hint; 0 = measure
	// Metered (phone hotspot, capped plan): carries only SharePct of traffic
	// while other links work, and everything when they don't. Over the
	// monthly cap it becomes failover-only; it is never switched off.
	Metered      bool    `json:"metered,omitempty"`
	SharePct     float64 `json:"sharePct,omitempty"`     // 0 = failover only
	MonthlyCapGB float64 `json:"monthlyCapGB,omitempty"` // 0 = no cap
	CycleDay     int     `json:"cycleDay,omitempty"`     // billing cycle start day (1-28); 0 = 1
}

type SpeedTest struct {
	Enabled         bool    `json:"enabled" label:"Automatic speed tests" help:"Test each connection on its own (not through the bond) to learn its real download and upload speed."`
	IntervalHours   float64 `json:"intervalHours" label:"Test every" help:"Re-test each connection this often." min:"1" max:"168" unit:"hours"`
	OnAddressChange bool    `json:"onAddressChange" label:"Re-test when the address changes" help:"A new local or public address usually means a new network (another campground Wi-Fi, a different cell tower or Starlink gateway)."`
	MinGapMinutes   int     `json:"minGapMinutes" label:"Never more often than every" help:"Debounce: the shortest time between automatic tests of one connection." min:"5" max:"1440" unit:"min" adv:"1"`
	IdleMbps        float64 `json:"idleMbps" label:"Only while traffic is below" help:"A test waits until the connection carries less than this, so it neither disturbs you nor gets a wrong answer." min:"0.1" max:"100" unit:"Mbit/s"`
	IdleStepMbps    float64 `json:"idleStepMbps" label:"Raise that limit by" help:"If the connection never gets that quiet, the limit rises by this much every step until the maximum." min:"0" max:"50" unit:"Mbit/s" adv:"1"`
	IdleStepMinutes int     `json:"idleStepMinutes" label:"Raise it every" min:"1" max:"240" unit:"min" adv:"1"`
	IdleMaxMbps     float64 `json:"idleMaxMbps" label:"Up to at most" help:"Never test while the connection carries more than this." min:"0.1" max:"500" unit:"Mbit/s" adv:"1"`
	Seconds         int     `json:"seconds" label:"Test length" help:"Seconds of download, then of upload. Data used is roughly speed x length." min:"3" max:"30" unit:"s" adv:"1"`
	Metered         bool    `json:"metered" label:"Also test metered connections" help:"Off: metered (capped) connections are only tested when you press Test now. A test can use 10-100 MB."`
	URL             string  `json:"url" label:"Speed test server" help:"Cloudflare-compatible endpoint (/__down and /__up)." adv:"1" fill:"1"`
}

type Relay struct {
	Enabled bool   `json:"enabled" label:"Accept phone relays" help:"Listen for the relay app on your local network. Each phone appears as a link named relay-<name>."`
	Token   string `json:"token" label:"Pairing token" help:"Enter this in the relay app. Phones are refused until it is set." secret:"1"`
	Listen  string `json:"listen" label:"Listen address" help:"UDP address the relay app announces itself to. Only phones on networks that are not bonded links (your LAN) are accepted." adv:"1" fill:"1"`
}

type UI struct {
	Listen    string `json:"listen" label:"Listen address" help:"Where this dashboard is served. Use 0.0.0.0:7007 to reach it from other devices (set a password!)." adv:"1"`
	Password  string `json:"password" label:"Dashboard password" help:"Leave empty for no login (only safe on 127.0.0.1)." secret:"1"`
	RefreshMs int    `json:"refreshMs" label:"Live refresh" help:"How often the dashboard updates." min:"100" max:"5000" unit:"ms" adv:"1"`
}

type Log struct {
	Level string `json:"level" label:"Log level" enum:"debug=Debug,info=Info,warn=Warning,error=Error" adv:"1"`
}

func DefaultClient() *Client {
	return &Client{
		Name:   "",
		Server: ServerEndpoint{Address: "", Enabled: true},
		Bonding: Bonding{
			Mode: "smart", Strategy: "latency", Redundancy: 2, SmallPacketMirrorBytes: 160,
			RealtimeMirror: true, RealtimePorts: "3478-3481,50000-50059,19302-19309",
			ReorderMode: "auto", ReorderMs: 400, Retransmit: true, Shaping: true, FEC: true,
			RepairPriority: true, StormRepair: true, OutageReports: true,
			// LossTolerantTCP stays off until it beats CUBIC in the lab.
			AutoScale: true, TargetDeliveryPct: 99.5, BenchBelowPct: 60, RejoinAbovePct: 75,
			ReliabilityFallSec: 10, ReliabilityRiseSec: 60,
			DirectWhenSingle: true, DirectAfterSec: 60, BondAfterSec: 300, CopiesDownAfterSec: 30,
			DirectGuard: true, DirectGuardMinutes: 60, QueueDepth: 1024, LatencyWeighting: true,
			Pacing: true, PacingStartMbps: 20, PacingDelayMs: 15, PacingLossPct: 10,
		},
		Health: Health{
			ProbeIntervalMs: 250, ProbeTimeoutMs: 1000, LossWindow: 40,
			DegradedRTTMs: 300, DegradedLossPct: 3, DownLossPct: 60, DownAfterMs: 1200,
			HistorySeconds: 300,
		},
		Tunnel: Tunnel{InterfaceName: "net-bond-007", MTU: 1380, FullTunnel: true, FailOpen: true, FailOpenSec: 8, PinConnections: true},
		SpeedTest: SpeedTest{
			Enabled: true, IntervalHours: 6, OnAddressChange: true, MinGapMinutes: 30,
			IdleMbps: 1, IdleStepMbps: 1, IdleStepMinutes: 15, IdleMaxMbps: 5, Seconds: 8,
			URL: "https://speed.cloudflare.com",
		},
		Interfaces: Interfaces{
			AutoAdd: true, ScanMs: 2000,
			Exclude: "lo*,docker*,veth*,br-*,virbr*,vmnet*,vboxnet*,tailscale*,zt*,wg*,tun*,tap*,utun*,net-bond-007*,awdl*,llw*,anpi*,ap1,bridge*,gif*,stf*,ipsec*,ppp*,isatap*,Teredo*,*Loopback*,vEthernet*,lxc*,cni*,flannel*,kube*,ifb*,dummy*,sit*,ip6tnl*",
		},
		Relay: Relay{Listen: "0.0.0.0:7008"},
		Links: map[string]Link{},
		// Version stays 0 here: it must come from the saved file so old files
		// are recognised and migrated. Normalize stamps the current version.
		UI:  UI{Listen: "127.0.0.1:7007", RefreshMs: 500},
		Log: Log{Level: "info"},
	}
}

func (c *Client) Validate() error {
	var errs []error
	if c.Server.Address != "" {
		if _, _, err := splitHostPort(c.Server.Address); err != nil {
			errs = append(errs, fmt.Errorf("server.address: %w", err))
		}
	}
	errs = append(errs, checkRanges(c)...)
	if c.Relay.Enabled {
		if _, _, err := splitHostPort(c.Relay.Listen); err != nil {
			errs = append(errs, fmt.Errorf("relay.listen: %w", err))
		}
	}
	if _, err := sched.ParsePorts(c.Bonding.RealtimePorts); err != nil {
		errs = append(errs, fmt.Errorf("bonding.realtimePorts: %w", err))
	}
	if !c.Tunnel.FullTunnel {
		if _, err := ParseCIDRs(c.Tunnel.Routes); err != nil {
			errs = append(errs, fmt.Errorf("tunnel.routes: %w", err))
		}
	}
	if c.Bonding.RejoinAbovePct <= c.Bonding.BenchBelowPct {
		errs = append(errs, errors.New("bonding.rejoinAbovePct must be higher than bonding.benchBelowPct"))
	}
	if c.Health.ProbeTimeoutMs < c.Health.ProbeIntervalMs {
		errs = append(errs, errors.New("health.probeTimeoutMs must be at least the probe interval"))
	}
	for name, l := range c.Links {
		if l.Weight < 0 || l.MaxMbps < 0 {
			errs = append(errs, fmt.Errorf("links.%s: weight and maxMbps must be >= 0", name))
		}
	}
	return errors.Join(errs...)
}

const clientConfigVersion = 5

// Normalize fills zero values left by older or hand-edited config files and
// migrates defaults that changed between versions.
func (c *Client) Normalize() {
	d := DefaultClient()
	if c.Links == nil {
		c.Links = map[string]Link{}
	}
	fillZero(c, d)
	if c.Version < 2 {
		// v2 added in-tunnel retransmission, which needs a reorder hold of
		// about 2.5 round trips; the old 60ms default would defeat it.
		if c.Bonding.ReorderMs <= 60 {
			c.Bonding.ReorderMs = d.Bonding.ReorderMs
		}
	}
	if c.Version < 3 {
		// v3: the product was renamed; the tunnel adapter follows the new
		// name (a pre-rename default name is migrated, a custom one kept).
		if c.Tunnel.InterfaceName == platformLegacyName {
			c.Tunnel.InterfaceName = d.Tunnel.InterfaceName
		}
		c.Interfaces.Exclude = strings.ReplaceAll(c.Interfaces.Exclude, platformLegacyName+"*", d.Tunnel.InterfaceName+"*")
	}
	if c.Version < 4 {
		// v4 added fail-open; older files have no key for it, and it should
		// be on unless switched off.
		c.Tunnel.FailOpen = true
	}
	if c.Version < 5 {
		// v5 added connection pinning and the never-slower-than-direct guard
		// (on unless switched off), and slowed mode switching: the old 15 s /
		// 30 s / 10 s defaults flipped paths every few minutes on Starlink
		// next to a phone. Values the user changed are kept.
		c.Tunnel.PinConnections, c.Bonding.DirectGuard = true, true
		for _, m := range []struct {
			v       *int
			old, nu int
		}{
			{&c.Bonding.DirectAfterSec, 15, d.Bonding.DirectAfterSec},
			{&c.Bonding.BondAfterSec, 30, d.Bonding.BondAfterSec},
			{&c.Bonding.CopiesDownAfterSec, 10, d.Bonding.CopiesDownAfterSec},
		} {
			if *m.v == m.old {
				*m.v = m.nu
			}
		}
	}
	c.Version = clientConfigVersion
}

// platformLegacyName is the product's pre-rename name (see
// platform.LegacyName; config can't import platform).
const platformLegacyName = "bond" + "007"

// ---------------------------------------------------------------- server

type Server struct {
	Name     string         `json:"name" label:"Server name" help:"Shown to clients and on the dashboard."`
	Listen   string         `json:"listen" label:"Bond listener (UDP)" help:"Address clients connect to. Changing it rebinds live." `
	Token    string         `json:"token" label:"Access token" help:"Clients must present this token. Empty allows anyone (not recommended)." secret:"1"`
	Network  ServerNetwork  `json:"network" label:"Client network" help:"Addresses handed to clients inside the tunnel."`
	Egress   ServerEgress   `json:"egress" label:"Internet egress" help:"How the server reaches the internet on behalf of clients."`
	Sessions ServerSessions `json:"sessions" label:"Sessions"`
	History  ServerHistory  `json:"history" label:"History" help:"A record on disk of link performance, events and every connection, for looking back at what happened."`
	Speed    ServerSpeed    `json:"speedTest" label:"Speed test" help:"Measure the server's own internet connection now and then, so a slow or throttled host shows up on the dashboard."`
	UI       UI             `json:"ui" label:"Dashboard"`
	Log      Log            `json:"log" label:"Logging"`
}

type ServerNetwork struct {
	Pool string `json:"pool" label:"Client address pool" help:"IPv4 CIDR from which client tunnel addresses are assigned. The first address is the gateway." adv:"1"`
	MTU  int    `json:"mtu" label:"Tunnel MTU" min:"576" max:"9000" unit:"bytes" adv:"1"`
}

type ServerEgress struct {
	DNSUpstream     string `json:"dnsUpstream" label:"Upstream DNS" help:"Resolver used for clients that bond their DNS (host:port). Empty uses the server's resolver."`
	AllowPrivate    bool   `json:"allowPrivate" label:"Allow private destinations" help:"Let clients reach private/link-local addresses (incl. cloud metadata) from the server. Keep off unless you need it."`
	TCPDialTimeout  int    `json:"tcpDialTimeoutMs" label:"TCP connect timeout" min:"500" max:"60000" unit:"ms" adv:"1"`
	UDPIdleTimeout  int    `json:"udpIdleTimeoutSec" label:"UDP flow idle timeout" min:"5" max:"3600" unit:"s" adv:"1"`
	TCPIdleTimeout  int    `json:"tcpIdleTimeoutSec" label:"TCP idle timeout" help:"Close proxied TCP connections idle for this long. 0 = never." min:"0" max:"86400" unit:"s" adv:"1"`
	TCPBufferKB     int    `json:"tcpBufferKB" label:"TCP buffer size" help:"Per-connection receive/send buffer inside the userspace stack." min:"64" max:"16384" unit:"KB" adv:"1"`
	ICMPEcho        bool   `json:"icmpEcho" label:"Forward ping" help:"Forward ICMP echo (ping) for clients using unprivileged ping sockets." adv:"1"`
	MaxConnsPerUser int    `json:"maxConnsPerSession" label:"Max connections per client" min:"16" max:"1000000" adv:"1"`
}

type ServerSpeed struct {
	Enabled       bool   `json:"enabled" label:"Test the server's internet" help:"Run a download and upload test shortly after start and then every few hours."`
	IntervalHours int    `json:"intervalHours" label:"Test every" min:"1" max:"168" unit:"h"`
	Seconds       int    `json:"seconds" label:"Test length" help:"Per direction." min:"3" max:"30" unit:"s" adv:"1"`
	LimitedMbps   int    `json:"limitedMbps" label:"Warn below" help:"Report the server's internet as limited when download or upload tests slower than this." min:"1" max:"100000" unit:"Mbit/s"`
	URL           string `json:"url" label:"Test server" help:"A Cloudflare-style speed test endpoint (GET /__down?bytes=N, POST /__up)." adv:"1"`
}

type ServerHistory struct {
	Enabled       bool `json:"enabled" label:"Keep history" help:"Record samples, events and a summary of every connection."`
	RetentionDays int  `json:"retentionDays" label:"Keep for" min:"1" max:"365" unit:"days"`
	SampleSec     int  `json:"sampleSec" label:"Sample every" help:"How often link and throughput samples are recorded." min:"2" max:"300" unit:"s" adv:"1"`
}

type ServerSessions struct {
	IdleTimeoutSec  int  `json:"idleTimeoutSec" label:"Session idle timeout" help:"Forget a client after this long without any packet." min:"10" max:"86400" unit:"s" adv:"1"`
	LinkTimeoutMs   int  `json:"linkTimeoutMs" label:"Link silence timeout" help:"Stop sending on a link not heard from for this long." min:"200" max:"60000" unit:"ms" adv:"1"`
	MaxSessions     int  `json:"maxSessions" label:"Max clients" min:"1" max:"65000" adv:"1"`
	HistorySeconds  int  `json:"historySeconds" label:"History length" min:"30" max:"3600" unit:"s" adv:"1"`
	ForceMirror     bool `json:"forceMirror" label:"Force mirror downstream" help:"Ignore client policy and send every downstream packet on every link." adv:"1"`
	DefaultQueue    int  `json:"queueDepth" label:"Per-link queue" min:"16" max:"8192" unit:"packets" adv:"1"`
	TrustClientRate bool `json:"trustClientRate" label:"Use client capacity reports" help:"Weight downstream links by the bandwidth the client measured." adv:"1"`
}

func DefaultServer() *Server {
	return &Server{
		Name:   "net-bond-007",
		Listen: ":7007",
		Network: ServerNetwork{
			Pool: "10.77.0.0/16", MTU: 1380,
		},
		Egress: ServerEgress{
			TCPDialTimeout: 10000, UDPIdleTimeout: 60, TCPIdleTimeout: 0, TCPBufferKB: 4096,
			ICMPEcho: true, MaxConnsPerUser: 20000,
		},
		Sessions: ServerSessions{
			IdleTimeoutSec: 120, LinkTimeoutMs: 1200, MaxSessions: 1000, HistorySeconds: 300,
			DefaultQueue: 1024, TrustClientRate: true,
		},
		History: ServerHistory{Enabled: true, RetentionDays: 14, SampleSec: 10},
		Speed:   ServerSpeed{Enabled: true, IntervalHours: 6, Seconds: 6, LimitedMbps: 200, URL: "https://speed.cloudflare.com"},
		UI:      UI{Listen: ":8080", RefreshMs: 500},
		Log:     Log{Level: "info"},
	}
}

func (s *Server) Validate() error {
	var errs []error
	if _, _, err := splitHostPort(s.Listen); err != nil {
		errs = append(errs, fmt.Errorf("listen: %w", err))
	}
	if p, err := netip.ParsePrefix(s.Network.Pool); err != nil || !p.Addr().Is4() || p.Bits() > 30 {
		errs = append(errs, errors.New("network.pool: must be an IPv4 CIDR of /30 or larger"))
	}
	if s.Egress.DNSUpstream != "" {
		if _, _, err := splitHostPort(s.Egress.DNSUpstream); err != nil {
			errs = append(errs, fmt.Errorf("egress.dnsUpstream: %w", err))
		}
	}
	errs = append(errs, checkRanges(s)...)
	return errors.Join(errs...)
}

func (s *Server) Normalize() { fillZero(s, DefaultServer()) }

// ---------------------------------------------------------------- helpers

func splitHostPort(s string) (string, string, error) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", errors.New("expected host:port")
	}
	host, port := s[:i], s[i+1:]
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if port == "" {
		return "", "", errors.New("missing port")
	}
	return host, port, nil
}

// ParseCIDRs parses a comma-separated list of prefixes.
func ParseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, err
			}
			part = netip.PrefixFrom(a, a.BitLen()).String()
		}
		p, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// MatchPattern reports whether name matches any comma-separated glob
// pattern (only '*' is special; matching is case-insensitive).
func MatchPattern(patterns, name string) bool {
	name = strings.ToLower(name)
	for _, p := range strings.Split(patterns, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" && glob(p, name) {
			return true
		}
	}
	return false
}

func glob(p, s string) bool {
	for len(p) > 0 {
		if p[0] == '*' {
			p = p[1:]
			if p == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if glob(p, s[i:]) {
					return true
				}
			}
			return false
		}
		if s == "" || p[0] != s[0] {
			return false
		}
		p, s = p[1:], s[1:]
	}
	return s == ""
}
