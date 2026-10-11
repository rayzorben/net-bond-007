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

	"github.com/rayzorben/bond007/internal/sched"
)

// ---------------------------------------------------------------- client

type Client struct {
	Name       string          `json:"name" label:"Device name" help:"How this device is shown on the server dashboard."`
	Server     ServerEndpoint  `json:"server" label:"Server" help:"Where the Bond007 server is running."`
	Bonding    Bonding         `json:"bonding" label:"Bonding" help:"How traffic is spread over your connections."`
	Health     Health          `json:"health" label:"Health monitoring" help:"How links are probed and judged."`
	Tunnel     Tunnel          `json:"tunnel" label:"Virtual network" help:"The virtual adapter your OS sends traffic into."`
	Interfaces Interfaces      `json:"interfaces" label:"Interface discovery" help:"Which network interfaces become bonded links."`
	Links      map[string]Link `json:"links" schema:"-"`
	UI         UI              `json:"ui" label:"Dashboard" help:"This web interface."`
	Log        Log             `json:"log" label:"Logging"`
}

type ServerEndpoint struct {
	Address string `json:"address" label:"Server address" help:"host:port of the server's UDP listener, e.g. bond.example.com:7007."`
	Token   string `json:"token" label:"Access token" help:"Must match the server's token." secret:"1"`
	Enabled bool   `json:"enabled" label:"Connect" help:"Turn bonding on or off without quitting the app."`
}

type Bonding struct {
	Mode                   string `json:"mode" label:"Bonding mode" help:"Smart: mirror on 1-2 links, stripe with redundancy on 3+. Mirror: every packet on every link. Speed: no duplication. Custom: fixed redundancy." enum:"smart=Smart,mirror=Mirror (max reliability),speed=Speed (max throughput),custom=Custom redundancy"`
	Strategy               string `json:"strategy" label:"Link selection" help:"Fastest arrival: each copy goes where it will arrive soonest, so fast links fill first (best for TCP). Round robin: copies rotate over links in proportion to their weights." enum:"latency=Fastest arrival,weighted=Weighted round robin"`
	Redundancy             int    `json:"redundancy" label:"Copies per packet" help:"With Smart (3+ links) or Custom: how many different links carry each packet." min:"1" max:"8"`
	SmallPacketMirrorBytes int    `json:"smallPacketMirrorBytes" label:"Mirror small packets up to" help:"Small packets (ACKs, DNS, keystrokes) are cheap to duplicate and latency critical. 0 disables." min:"0" max:"1500" unit:"bytes" adv:"1"`
	RealtimeMirror         bool   `json:"realtimeMirror" label:"Mirror real-time media" help:"Send voice/video calls (Teams, Meet, WebRTC) on every link for glitch-free calls."`
	RealtimePorts          string `json:"realtimePorts" label:"Real-time UDP ports" help:"Comma-separated ports or ranges treated as real-time media." adv:"1"`
	ReorderMode            string `json:"reorderMode" label:"Reorder buffer" help:"Hold early packets briefly so TCP sees them in order. Auto sizes the hold from the latency spread between links." enum:"auto=Auto,fixed=Fixed,off=Off" adv:"1"`
	ReorderMs              int    `json:"reorderMs" label:"Max reorder hold" help:"Upper bound (auto) or exact (fixed) hold time." min:"1" max:"500" unit:"ms" adv:"1"`
	QueueDepth             int    `json:"queueDepth" label:"Per-link queue" help:"Packets buffered per link before it is considered congested." min:"16" max:"8192" unit:"packets" adv:"1"`
	LatencyWeighting       bool   `json:"latencyWeighting" label:"Favour low latency" help:"Automatic weights consider latency as well as bandwidth." adv:"1"`
	Pacing                 bool   `json:"pacing" label:"Adaptive link pacing" help:"Learn each link's real capacity (backs off when its latency rises or it drops packets) and never feed it more. Keeps slow links from drowning in duplicates."`
	PacingDelayMs          int    `json:"pacingDelayMs" label:"Pacing: queue delay limit" help:"Back off a link when its latency rises this far above its best (or half its base RTT, if larger)." min:"2" max:"1000" unit:"ms" adv:"1"`
	PacingLossPct          int    `json:"pacingLossPct" label:"Pacing: loss limit" help:"Back off a link when probe loss exceeds this. Kept high because Wi-Fi and cellular lose packets at random." min:"1" max:"100" unit:"%" adv:"1"`
	PacingStartMbps        int    `json:"pacingStartMbps" label:"Pacing start rate" help:"Initial per-link rate before the pacer has learned the link." min:"1" max:"10000" unit:"Mbit/s" adv:"1"`
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
	InterfaceName string `json:"interfaceName" label:"Adapter name" help:"Name of the virtual adapter (macOS always uses utunN)." adv:"1"`
	MTU           int    `json:"mtu" label:"MTU" help:"Packet size inside the tunnel. Must leave room for the 16-byte Bond007 header plus IP/UDP headers on every link." min:"576" max:"9000" unit:"bytes" adv:"1"`
	FullTunnel    bool   `json:"fullTunnel" label:"Send all traffic" help:"Route all internet traffic through the bond. Turn off to bond only the networks listed below."`
	Routes        string `json:"routes" label:"Bonded networks" help:"Comma-separated CIDRs routed through the bond when 'Send all traffic' is off."`
	DNSOverride   bool   `json:"dnsOverride" label:"Bond DNS" help:"Point system DNS at the server so lookups are bonded too."`
	RouterMode    bool   `json:"routerMode" label:"Router mode" help:"Masquerade traffic from LAN devices into the bond (OpenWrt / Linux gateways)."`
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
			ReorderMode: "auto", ReorderMs: 60, QueueDepth: 1024, LatencyWeighting: true,
			Pacing: true, PacingStartMbps: 20, PacingDelayMs: 15, PacingLossPct: 10,
		},
		Health: Health{
			ProbeIntervalMs: 250, ProbeTimeoutMs: 1000, LossWindow: 40,
			DegradedRTTMs: 300, DegradedLossPct: 3, DownLossPct: 60, DownAfterMs: 1200,
			HistorySeconds: 300,
		},
		Tunnel: Tunnel{InterfaceName: "bond007", MTU: 1380, FullTunnel: true},
		Interfaces: Interfaces{
			AutoAdd: true, ScanMs: 2000,
			Exclude: "lo*,docker*,veth*,br-*,virbr*,vmnet*,vboxnet*,tailscale*,zt*,wg*,tun*,tap*,utun*,bond007*,awdl*,llw*,anpi*,ap1,bridge*,gif*,stf*,ipsec*,ppp*,isatap*,Teredo*,*Loopback*,vEthernet*,lxc*,cni*,flannel*,kube*,ifb*,dummy*,sit*,ip6tnl*",
		},
		Links: map[string]Link{},
		UI:    UI{Listen: "127.0.0.1:7007", RefreshMs: 500},
		Log:   Log{Level: "info"},
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
	if _, err := sched.ParsePorts(c.Bonding.RealtimePorts); err != nil {
		errs = append(errs, fmt.Errorf("bonding.realtimePorts: %w", err))
	}
	if !c.Tunnel.FullTunnel {
		if _, err := ParseCIDRs(c.Tunnel.Routes); err != nil {
			errs = append(errs, fmt.Errorf("tunnel.routes: %w", err))
		}
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

// Normalize fills zero values left by older or hand-edited config files.
func (c *Client) Normalize() {
	d := DefaultClient()
	if c.Links == nil {
		c.Links = map[string]Link{}
	}
	fillZero(c, d)
}

// ---------------------------------------------------------------- server

type Server struct {
	Name     string         `json:"name" label:"Server name" help:"Shown to clients and on the dashboard."`
	Listen   string         `json:"listen" label:"Bond listener (UDP)" help:"Address clients connect to. Changing it rebinds live." `
	Token    string         `json:"token" label:"Access token" help:"Clients must present this token. Empty allows anyone (not recommended)." secret:"1"`
	Network  ServerNetwork  `json:"network" label:"Client network" help:"Addresses handed to clients inside the tunnel."`
	Egress   ServerEgress   `json:"egress" label:"Internet egress" help:"How the server reaches the internet on behalf of clients."`
	Sessions ServerSessions `json:"sessions" label:"Sessions"`
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
		Name:   "bond007",
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
		UI:  UI{Listen: ":8080", RefreshMs: 500},
		Log: Log{Level: "info"},
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
