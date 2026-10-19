package client

import (
	"crypto/subtle"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/proto"
)

// Phone relays: phones on the LAN running the Net-Bond-007 Relay app share
// their cellular data. Each phone becomes a link whose socket talks to the
// phone instead of the server; the phone copies frames to and from the
// server over cellular without looking inside them, so the engine, probes and
// retransmission treat it like any other link and the server sees that link
// arrive from the carrier's address.
//
//	link socket --LAN--> phone :7008 --cellular--> server
//	link socket <--LAN-- phone :7008 <--cellular-- server
//
// The phone announces itself with RELAY_HELLO every 2 s; the client answers
// RELAY_ACK with the server address. A phone silent for relayExpiry is
// dropped and its link closed.

const (
	relayExpiry   = 10 * time.Second
	relayPrefix   = "relay-"
	relayNameMax  = 24
	relayKind     = "cellular"
	relayMaxPeers = 8
)

type relayPeer struct {
	link  string         // link name, relay-<name>
	addr  netip.AddrPort // the phone's relay socket
	hello proto.RelayHello
	seen  time.Time
}

type relays struct {
	mu     sync.Mutex
	conn   *net.UDPConn
	listen string // address conn is bound to ("" = not listening)
	err    string
	peers  map[string]*relayPeer // by link name
}

// relayApply starts, rebinds or stops the relay listener to match cfg.
func (c *Client) relayApply(cfg *config.Client) {
	r := &c.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	want := ""
	if cfg.Relay.Enabled {
		want = cfg.Relay.Listen
	}
	if want == r.listen && (r.conn != nil || want == "") {
		return
	}
	if r.conn != nil {
		r.conn.Close()
		r.conn = nil
	}
	r.listen, r.err = want, ""
	if want == "" {
		r.peers = nil
		c.poke()
		return
	}
	ua, err := net.ResolveUDPAddr("udp", want)
	if err == nil {
		r.conn, err = net.ListenUDP("udp", ua)
	}
	if err != nil {
		r.err = err.Error()
		c.log.Warn("phone relay listener failed", "listen", want, "err", err)
		return
	}
	c.log.Info("listening for phone relays", "udp", r.conn.LocalAddr().String())
	go c.relayRead(r.conn)
}

func (c *Client) relayClose() {
	c.relays.mu.Lock()
	if c.relays.conn != nil {
		c.relays.conn.Close()
		c.relays.conn = nil
	}
	c.relays.listen = ""
	c.relays.mu.Unlock()
}

func (c *Client) relayRead(conn *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		n, from, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			if strings.Contains(err.Error(), "use of closed") {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		f := buf[:n]
		h, err := proto.ParseHeader(f)
		if err != nil || h.Type != proto.TypeRelayHello {
			continue
		}
		hello := proto.RelayHello{Battery: -1, Signal: -1} // absent = unknown
		if proto.ParseJSON(f, &hello) != nil {
			continue
		}
		ack := c.relayHello(from, hello, time.Now())
		if out, err := proto.AppendJSON(nil, proto.TypeRelayAck, 0, 0, ack); err == nil {
			conn.WriteToUDPAddrPort(out, from)
		}
	}
}

// relayHello registers or refreshes a phone and returns the reply.
func (c *Client) relayHello(from netip.AddrPort, h proto.RelayHello, now time.Time) proto.RelayAck {
	cfg := c.cfg.Get()
	from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
	switch {
	case !cfg.Relay.Enabled:
		return proto.RelayAck{Reason: "phone relays are switched off on the router"}
	case cfg.Relay.Token == "":
		return proto.RelayAck{Reason: "set a pairing token on the router first"}
	case subtle.ConstantTimeCompare([]byte(h.Token), []byte(cfg.Relay.Token)) != 1:
		return proto.RelayAck{Reason: "wrong pairing token"}
	case !c.relayFromLAN(from.Addr()):
		return proto.RelayAck{Reason: "not on the local network"}
	}
	link := relayLinkName(h.Name, from.Addr())
	r := &c.relays
	r.mu.Lock()
	if r.peers == nil {
		r.peers = map[string]*relayPeer{}
	}
	p, ok := r.peers[link]
	if !ok && len(r.peers) >= relayMaxPeers {
		r.mu.Unlock()
		return proto.RelayAck{Reason: "too many phones"}
	}
	if !ok {
		p = &relayPeer{link: link}
		r.peers[link] = p
	}
	changed := !ok || p.addr != from || p.hello.Cellular != h.Cellular
	power := !ok || flag(p.hello.Awake) != flag(h.Awake) || flag(p.hello.BatteryExempt) != flag(h.BatteryExempt)
	p.addr, p.hello, p.seen = from, h, now
	r.mu.Unlock()
	if changed {
		c.log.Info("phone relay", "link", link, "phone", from, "model", h.Model, "cellular", h.Cellular)
		c.poke() // add or update the link now rather than at the next scan
	}
	if power {
		c.log.Info("phone relay power", "link", link, "awake", flag(h.Awake), "battery_exempt", flag(h.BatteryExempt))
	}
	return proto.RelayAck{OK: true, Server: cfg.Server.Address}
}

// flag renders an optional phone report: "yes", "no" or "unknown".
func flag(b *bool) string {
	switch {
	case b == nil:
		return "unknown"
	case *b:
		return "yes"
	}
	return "no"
}

// relayWarnings explains phones that will stall a call: off screen, Android
// puts their Wi-Fi in power save and holds traffic for up to ~2 s.
func (c *Client) relayWarnings() []string {
	r := &c.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.peers))
	for n := range r.peers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		h := r.peers[n].hello
		if flag(h.Awake) == "no" {
			out = append(out, "Phone relay "+n+": the phone is not keeping awake, so Android puts its Wi-Fi to sleep and holds traffic for up to 2 s (calls stall). Plug the phone in and leave the relay app open; it dims itself.")
		}
		if flag(h.BatteryExempt) == "no" {
			out = append(out, "Phone relay "+n+": battery optimisation is on for the relay app, so Android may pause it. Tap Fix in the app.")
		}
	}
	return out
}

// relayFromLAN accepts phones only on networks this machine serves (LAN
// bridges and other interfaces that are not bonded links), never on a WAN
// such as campground Wi-Fi or the tunnel.
func (c *Client) relayFromLAN(ip netip.Addr) bool {
	cfg := c.cfg.Get()
	c.mu.Lock()
	ifs := c.ifaces
	c.mu.Unlock()
	for _, it := range ifs {
		if st := c.eligibility(cfg, it); st == "bonded" || st == "tunnel" {
			continue
		}
		for _, p := range it.IPv4 {
			if p.Bits() < 32 && p.Masked().Contains(ip) {
				return true
			}
		}
	}
	return false
}

// relayLinkName turns the phone's chosen name into a link name.
func relayLinkName(name string, ip netip.Addr) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-', r == '_', r == '.':
			if s := b.String(); s != "" && !strings.HasSuffix(s, "-") {
				b.WriteByte('-')
			}
		}
		if b.Len() >= relayNameMax {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = strings.ReplaceAll(ip.String(), ".", "-")
	}
	return relayPrefix + s
}

// relayLinks expires silent phones and returns the ones that can carry
// traffic now, as interface entries plus the phone address for each.
func (c *Client) relayLinks(now time.Time) ([]platform.Iface, map[string]netip.AddrPort) {
	r := &c.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	var ifs []platform.Iface
	addrs := map[string]netip.AddrPort{}
	for name, p := range r.peers {
		if now.Sub(p.seen) > relayExpiry {
			c.log.Info("phone relay gone", "link", name, "phone", p.addr)
			delete(r.peers, name)
			continue
		}
		it := platform.Iface{
			Name: name, Kind: relayKind, Desc: "Phone relay: " + p.hello.Model,
			Up: p.hello.Cellular, IPv4: []netip.Prefix{netip.PrefixFrom(p.addr.Addr(), 32)},
		}
		ifs = append(ifs, it)
		addrs[name] = p.addr
	}
	sort.Slice(ifs, func(i, j int) bool { return ifs[i].Name < ifs[j].Name })
	return ifs, addrs
}

// relayInfo is the dashboard's view of the phone behind a relay link.
func (c *Client) relayInfo(link string, now time.Time) *bond.RelayInfo {
	r := &c.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.peers[link]
	if p == nil {
		return nil
	}
	h := p.hello
	return &bond.RelayInfo{
		Phone: p.addr.Addr().String(), Model: h.Model, Carrier: h.Carrier, Version: h.Version,
		Cellular: h.Cellular, Battery: h.Battery, Charging: h.Charging, TempC: h.TempC,
		Thermal: h.Thermal, Signal: h.Signal, SeenMs: now.Sub(p.seen).Milliseconds(),
		Awake: h.Awake, BatteryExempt: h.BatteryExempt,
	}
}

func (c *Client) relayStatus() (listening string, err string) {
	c.relays.mu.Lock()
	defer c.relays.mu.Unlock()
	if c.relays.conn != nil {
		listening = c.relays.conn.LocalAddr().String()
	}
	return listening, c.relays.err
}

// dialRelay opens a link socket to a phone over the LAN.
func dialRelay(phone netip.AddrPort) (*net.UDPConn, error) {
	uc, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(phone))
	if err != nil {
		return nil, err
	}
	uc.SetReadBuffer(4 << 20)
	uc.SetWriteBuffer(4 << 20)
	return uc, nil
}
