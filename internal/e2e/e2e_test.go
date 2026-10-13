// Package e2e runs a real server and a real client in-process. Three bonded
// links are carried over loopback through impairment proxies (latency and
// loss), and a userspace TCP/IP stack plays the role of the operating
// system. No root privileges or TUN device are needed.
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/rayzorben/net-bond-007/internal/client"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
	"github.com/rayzorben/net-bond-007/internal/server"
)

// impair is a UDP relay between one client link and the server.
type impair struct {
	front    *net.UDPConn
	back     *net.UDPConn
	delay    time.Duration
	loss     float64
	dead     atomic.Bool
	mu       sync.Mutex
	rng      *rand.Rand
	clientAd atomic.Pointer[net.UDPAddr]
}

func newImpair(t *testing.T, srv netip.AddrPort, delay time.Duration, loss float64, seed int64) *impair {
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	back, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(srv))
	if err != nil {
		t.Fatal(err)
	}
	p := &impair{front: front, back: back, delay: delay, loss: loss, rng: rand.New(rand.NewSource(seed))}
	go p.pump(front, func(b []byte, from *net.UDPAddr) {
		p.clientAd.Store(from)
		back.Write(b)
	})
	go p.pump(back, func(b []byte, _ *net.UDPAddr) {
		if a := p.clientAd.Load(); a != nil {
			front.WriteToUDP(b, a)
		}
	})
	t.Cleanup(func() { front.Close(); back.Close() })
	return p
}

func (p *impair) pump(c *net.UDPConn, fwd func([]byte, *net.UDPAddr)) {
	buf := make([]byte, 65535)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		p.mu.Lock()
		drop := p.dead.Load() || p.rng.Float64() < p.loss
		p.mu.Unlock()
		if drop {
			continue
		}
		b := append([]byte(nil), buf[:n]...)
		time.AfterFunc(p.delay, func() { fwd(b, from) })
	}
}

func (p *impair) addr() netip.AddrPort { return p.front.LocalAddr().(*net.UDPAddr).AddrPort() }

// osStack is a userspace TCP/IP stack standing in for the client OS.
type osStack struct {
	s  *stack.Stack
	ep *channel.Endpoint
	// pktLog, when set, receives a one-line summary of each TCP packet
	// (debugging stalls).
	pktLog atomic.Pointer[func(string)]
}

func tcpSummary(dir string, pkt []byte) string {
	if len(pkt) < 40 || pkt[9] != 6 {
		return ""
	}
	ihl := int(pkt[0]&0x0f) * 4
	tcp := pkt[ihl:]
	off := int(tcp[12]>>4) * 4
	flags := tcp[13]
	var f string
	for i, n := range []string{"F", "S", "R", "P", "A"} {
		if flags&(1<<i) != 0 {
			f += n
		}
	}
	sack := ""
	for o := 20; o+1 < off && o < len(tcp); {
		k := tcp[o]
		if k == 0 {
			break
		}
		if k == 1 {
			o++
			continue
		}
		l := int(tcp[o+1])
		if k == 5 {
			sack = fmt.Sprintf(" sack%d", (l-2)/8)
		}
		if l < 2 {
			break
		}
		o += l
	}
	return fmt.Sprintf("%s seq=%d ack=%d len=%d win=%d %s%s", dir, binary.BigEndian.Uint32(tcp[4:]), binary.BigEndian.Uint32(tcp[8:]),
		len(pkt)-ihl-off, binary.BigEndian.Uint16(tcp[14:]), f, sack)
}

func newOSStack(t *testing.T, ip netip.Addr, out func([]byte)) *osStack {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	ep := channel.New(4096, 1380, "")
	if err := s.CreateNIC(1, ep); err != nil {
		t.Fatal(err)
	}
	s.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddrFrom4(ip.As4()).WithPrefix(),
	}, stack.AddressProperties{})
	s.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	// Receive buffers like a real OS (Linux tcp_rmem max is 6 MB). gVisor's
	// small default fills with out-of-order data after a burst of loss and
	// advertises a zero window, a stall real devices don't have.
	mod := tcpip.TCPModerateReceiveBufferOption(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &mod)
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 128 << 10, Max: 6 << 20}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
	// Modern loss recovery, as Linux, Windows and iOS have (RACK); gVisor's
	// default falls back to slow one-segment-per-RTO recovery after a burst
	// of loss, which no real client does.
	rack := tcpip.TCPRecovery(tcpip.TCPRACKLossDetection)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rack)
	cc := tcpip.CongestionControlOption("cubic")
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &cc)
	self := &osStack{s: s, ep: ep}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.Close() })
	go func() {
		for {
			pb := ep.ReadContext(ctx)
			if pb == nil {
				return
			}
			v := pb.ToView()
			if lf := self.pktLog.Load(); lf != nil {
				if s := tcpSummary("out", v.AsSlice()); s != "" {
					(*lf)(s)
				}
			}
			out(append([]byte(nil), v.AsSlice()...))
			v.Release()
			pb.DecRef()
		}
	}()
	return self
}

func (o *osStack) inject(pkt []byte) {
	if lf := o.pktLog.Load(); lf != nil {
		if s := tcpSummary("in ", pkt); s != "" {
			(*lf)(s)
		}
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), pkt...))})
	o.ep.InjectInbound(ipv4.ProtocolNumber, pb)
	pb.DecRef()
}

func (o *osStack) httpClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				ap, err := netip.ParseAddrPort(addr)
				if err != nil {
					return nil, err
				}
				return gonet.DialContextTCP(ctx, o.s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(ap.Addr().As4()), Port: ap.Port()}, ipv4.ProtocolNumber)
			},
		},
	}
}

type rig struct {
	srv     *server.Server
	cli     *client.Client
	cfg     *config.Store[config.Client]
	proxies map[string]*impair
	os      *osStack
}

func newRig(t *testing.T, links []struct {
	name, kind string
	delay      time.Duration
	loss       float64
}, mutate func(*config.Client)) *rig {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("E2E_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	scfg := config.DefaultServer()
	scfg.Listen = "127.0.0.1:0"
	scfg.Token = "s3cret"
	sstore := config.NewMemory(scfg)
	srv, err := server.New(sstore, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	r := &rig{srv: srv, proxies: map[string]*impair{}}
	var ifaces []platform.Iface
	for i, l := range links {
		r.proxies[l.name] = newImpair(t, srv.Addr(), l.delay, l.loss, int64(i+1))
		ifaces = append(ifaces, platform.Iface{
			Name: l.name, Kind: l.kind, Index: i + 100, Up: true,
			IPv4: []netip.Prefix{netip.MustParsePrefix(fmt.Sprintf("127.0.0.%d/8", i+2))},
		})
	}
	ccfg := config.DefaultClient()
	ccfg.Name = "e2e"
	ccfg.Server.Address = srv.Addr().String()
	ccfg.Server.Token = "s3cret"
	ccfg.Health.ProbeIntervalMs = 50
	ccfg.Health.ProbeTimeoutMs = 400
	ccfg.Health.DownAfterMs = 600
	ccfg.Health.DegradedRTTMs = 2000
	ccfg.Interfaces.Exclude = ""
	ccfg.SpeedTest.Enabled = false // fake interfaces: no real-network tests
	if mutate != nil {
		mutate(ccfg)
	}
	r.cfg = config.NewMemory(ccfg)
	c := client.New(r.cfg, log)
	c.NoTunnel = true
	c.Discover = func() ([]platform.Iface, error) { return ifaces, nil }
	c.Dial = func(it platform.Iface, _ netip.AddrPort) (*net.UDPConn, error) {
		return net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(r.proxies[it.Name].addr()))
	}
	var osRef atomic.Pointer[osStack]
	c.Deliver = func(p []byte) {
		if o := osRef.Load(); o != nil {
			o.inject(p)
		}
	}
	r.cli = c
	c.Start()
	t.Cleanup(c.Close)

	// Wait for every link to come up and an address to be assigned.
	deadline := time.Now().Add(10 * time.Second)
	var ip string
	for time.Now().Before(deadline) {
		st := c.Snapshot(0)
		up := 0
		for _, l := range st.Links {
			if l.State == "up" || l.State == "degraded" {
				up++
			}
		}
		if st.Server.Connected && up == len(links) {
			ip = srv.Snapshot(0).Sessions[0].IP
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ip == "" {
		t.Fatalf("client never connected: %+v", c.Snapshot(0))
	}
	r.os = newOSStack(t, netip.MustParseAddr(ip), c.SendPacket)
	osRef.Store(r.os)
	return r
}

func payload(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(42)).Read(b)
	return b
}

// fakeOrigin is the "internet" address the client OS requests. The server
// gateway's dialer maps it to the local test web server.
const fakeOrigin = "203.0.113.10:80"

func (r *rig) webServer(t *testing.T, body []byte) string {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		// Write in chunks so a mid-transfer link failure lands mid-stream.
		for off := 0; off < len(body); off += 64 << 10 {
			end := min(off+64<<10, len(body))
			if _, err := w.Write(body[off:end]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(ts.Close)
	real := ts.Listener.Addr().String()
	r.srv.Gateway().SetDialer(func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == fakeOrigin {
			addr = real
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	})
	return "http://" + fakeOrigin + "/"
}

type linkSpec = struct {
	name, kind string
	delay      time.Duration
	loss       float64
}

// The scenario from the brief: four links, the request arrives on all of
// them, ethernet wins the race, and the response is striped back with
// redundancy. The download must be byte-identical.
func TestBondedDownloadIntegrity(t *testing.T) {
	r := newRig(t, []linkSpec{
		{"eth0", "ethernet", 2 * time.Millisecond, 0},
		{"wlan0", "wifi", 8 * time.Millisecond, 0.02},
		{"usb0", "usb", 15 * time.Millisecond, 0.02},
		{"bnep0", "bluetooth", 30 * time.Millisecond, 0.05},
	}, nil)
	body := payload(4 << 20)
	url := r.webServer(t, body)

	resp, err := r.os.httpClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read: %v (got %d bytes)", err, len(got))
	}
	if sha256.Sum256(got) != sha256.Sum256(body) {
		t.Fatalf("payload corrupted: got %d bytes", len(got))
	}

	// Verify the bonding actually happened on both sides.
	st := r.srv.Snapshot(0)
	sess := st.Sessions[0]
	if len(sess.Links) != 4 {
		t.Fatalf("server sees %d links", len(sess.Links))
	}
	var used int
	for _, l := range sess.Links {
		if l.TxPackets > 100 {
			used++
		}
	}
	// How many copies go out depends on the links' pacing headroom (extra
	// copies are only sent where they fit) and on automatic scaling, which
	// drops to one copy once links prove reliable; on unlimited local links
	// that varies run to run. Integrity is what this test checks.
	t.Logf("downstream: %d links carried data, wire/unique = %.2f", used,
		float64(sess.Engine.WireTxBytes)/float64(sess.Engine.UniqueTxBytes))
	cs := r.cli.Snapshot(0)
	var wins, dups uint64
	for _, l := range cs.Links {
		wins += l.Wins
		dups += l.Dups
	}
	if dups == 0 {
		t.Errorf("expected redundant copies to be discarded on the client (wins=%d dups=%d)", wins, dups)
	}
	ratio := float64(cs.Engine.WireRxBytes) / float64(cs.Engine.UniqueRxBytes)
	t.Logf("downloaded %d bytes; wire/unique ratio %.2f; client wins=%d dups=%d", len(got), ratio, wins, dups)
	for _, l := range cs.Links {
		t.Logf("  %-6s state=%-8s rtt=%5.1fms loss=%4.1f%% wins=%6d dups=%6d", l.Name, l.State, l.RTTms, l.LossPct, l.Wins, l.Dups)
	}
}

// A link dies in the middle of a download: the transfer must complete intact.
func TestLinkFailureMidTransfer(t *testing.T) {
	r := newRig(t, []linkSpec{
		{"eth0", "ethernet", 3 * time.Millisecond, 0},
		{"wlan0", "wifi", 6 * time.Millisecond, 0},
		{"usb0", "usb", 10 * time.Millisecond, 0},
	}, nil)
	body := payload(6 << 20)
	url := r.webServer(t, body)

	resp, err := r.os.httpClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	buf := make([]byte, 32<<10)
	killed := false
	for {
		n, err := resp.Body.Read(buf)
		got.Write(buf[:n])
		if !killed && got.Len() > len(body)/3 {
			r.proxies["eth0"].dead.Store(true) // unplug the ethernet cable
			killed = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read after failover: %v (got %d bytes)", err, got.Len())
		}
	}
	resp.Body.Close()
	if !bytes.Equal(got.Bytes(), body) {
		t.Fatalf("payload corrupted after failover: %d bytes", got.Len())
	}
	time.Sleep(800 * time.Millisecond)
	for _, l := range r.cli.Snapshot(0).Links {
		if l.Name == "eth0" && l.State != "down" {
			t.Errorf("eth0 should be marked down, is %s", l.State)
		}
	}
}

// Changing settings must take effect live, without reconnecting.
func TestLiveConfigChange(t *testing.T) {
	r := newRig(t, []linkSpec{
		{"eth0", "ethernet", time.Millisecond, 0},
		{"wlan0", "wifi", time.Millisecond, 0},
		{"usb0", "usb", time.Millisecond, 0},
	}, nil)
	session := r.cli.Engine().Session
	if err := r.cfg.Mutate(func(c *config.Client) { c.Bonding.Mode = "mirror" }); err != nil {
		t.Fatal(err)
	}
	if r.cli.Engine().Policy().Mode != "mirror" {
		t.Fatal("client policy not applied")
	}
	// Server picks up the policy on the next policy push (immediately after change).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ss := r.srv.Snapshot(0).Sessions; len(ss) > 0 && ss[0].Engine.Mode == "mirror" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if m := r.srv.Snapshot(0).Sessions[0].Engine.Mode; m != "mirror" {
		t.Fatalf("server downstream mode = %q", m)
	}
	// Disable a link: it leaves the bond, session survives.
	if err := r.cfg.Mutate(func(c *config.Client) { c.Links["usb0"] = config.Link{Disabled: true} }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(r.cli.Snapshot(0).Links); n != 2 {
		t.Fatalf("expected 2 links after disabling usb0, have %d", n)
	}
	if r.cli.Engine().Session != session {
		t.Fatal("session was restarted by a non-server setting")
	}
}

// A wrong token is rejected and surfaced on the link.
func TestBadTokenRejected(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	scfg := config.DefaultServer()
	scfg.Listen = "127.0.0.1:0"
	scfg.Token = "right"
	srv, err := server.New(config.NewMemory(scfg), log)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	ccfg := config.DefaultClient()
	ccfg.Server.Address = srv.Addr().String()
	ccfg.Server.Token = "wrong"
	ccfg.Interfaces.Exclude = ""
	ccfg.SpeedTest.Enabled = false // fake interfaces: no real-network tests
	c := client.New(config.NewMemory(ccfg), log)
	c.NoTunnel = true
	c.Discover = func() ([]platform.Iface, error) {
		return []platform.Iface{{Name: "lo-test", Kind: "ethernet", Up: true, IPv4: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}}, nil
	}
	c.Dial = func(_ platform.Iface, s netip.AddrPort) (*net.UDPConn, error) {
		return net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(s))
	}
	c.Start()
	defer c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ls := c.Snapshot(0).Links; len(ls) == 1 && ls[0].LastError != "" && bytes.Contains([]byte(ls[0].LastError), []byte("bad-token")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("bad token not surfaced: %+v", c.Snapshot(0).Links)
}

// Starlink + metered phone: while Starlink works the phone carries only its
// ~10% share of the download; when Starlink dies mid-transfer the phone
// carries the rest and the download completes intact.
func TestMeteredPhoneShareAndFailover(t *testing.T) {
	r := newRig(t, []linkSpec{
		{"eth0", "ethernet", 20 * time.Millisecond, 0},
		{"eth3", "usb", 30 * time.Millisecond, 0},
	}, func(c *config.Client) {
		c.Links = map[string]config.Link{"eth3": {Metered: true, SharePct: 10}}
	})
	// Let the metered setting reach the server (policy resend).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		for _, l := range r.srv.Snapshot(0).Sessions[0].Links {
			ok = ok || (l.Name == "eth3" && l.Metered)
		}
		if ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	phoneRx := func() uint64 {
		for _, l := range r.cli.Snapshot(0).Links {
			if l.Name == "eth3" {
				return l.RxBytes
			}
		}
		return 0
	}
	body := payload(8 << 20)
	url := r.webServer(t, body)

	before := phoneRx()
	got, err := io.ReadAll(mustGet(t, r, url).Body)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("download with phone sharing: %v (%d bytes)", err, len(got))
	}
	share := float64(phoneRx()-before) / float64(len(body))
	t.Logf("phone carried %.1f%% of the download while Starlink worked", share*100)
	if share > 0.2 {
		t.Fatalf("metered phone carried %.0f%%, want about 10%%", share*100)
	}

	resp := mustGet(t, r, url)
	var buf bytes.Buffer
	chunk := make([]byte, 32<<10)
	killed := false
	for {
		n, err := resp.Body.Read(chunk)
		buf.Write(chunk[:n])
		if !killed && buf.Len() > len(body)/4 {
			r.proxies["eth0"].dead.Store(true) // Starlink drops out
			killed = true
			if os.Getenv("E2E_PKT") != "" {
				start := time.Now()
				var n atomic.Int32
				lf := func(s string) {
					if el := time.Since(start); el > 1500*time.Millisecond && n.Add(1) < 60 {
						t.Logf("PKT +%4dms %s", el.Milliseconds(), s)
					}
				}
				r.os.pktLog.Store(&lf)
			}
			if os.Getenv("E2E_TRACE") != "" {
				go func() {
					for i := 0; i < 20; i++ {
						time.Sleep(100 * time.Millisecond)
						sv := r.srv.Snapshot(0).Sessions[0]
						var sl string
						for _, l := range sv.Links {
							sl += fmt.Sprintf(" %s:%s/tx%d/q%d/rel%.2f", l.Name, l.State, l.TxBytes, l.Queue, l.Reliab)
						}
						cv := r.cli.Snapshot(0)
						var cl string
						for _, l := range cv.Links {
							cl += fmt.Sprintf(" %s:%s/tx%d", l.Name, l.State, l.TxBytes)
						}
						t.Logf("t+%dms srv[%s] rtx=%d reinj=%d | cli[%s] skip=%d urx=%d", (i+1)*100, sl, sv.Engine.Retransmits, sv.Engine.Reinjected, cl, cv.Engine.ReorderSkip, cv.Engine.UniqueRxBytes)
					}
					for i := 2; i < 70; i++ {
						time.Sleep(time.Second)
						sv := r.srv.Snapshot(0).Sessions[0]
						cv := r.cli.Snapshot(0)
						var sl, cl string
						for _, l := range sv.Links {
							sl += fmt.Sprintf(" %s:%s/tx%d/q%d/pace%.0fk/shape%.0fk", l.Name, l.State, l.TxBytes, l.Queue, l.PaceBps/1e3, l.ShapeBps/1e3)
						}
						for _, l := range cv.Links {
							cl += fmt.Sprintf(" %s:%s/tx%d", l.Name, l.State, l.TxBytes)
						}
						t.Logf("t+%ds TCP %v %v", i+1, r.srv.Gateway().TCPInfo(), r.srv.Gateway().TCPStats())
						t.Logf("t+%ds SRVRX gaps=%d gaveup=%d skip=%d held=%d hold=%.0f urx=%d inord=%d waited=%d late=%d nackSent=%d | CLI rtx=%d prem=%d nackRecv=%d noLink=%d", i+1, sv.Engine.GapsDetected, sv.Engine.GapsGaveUp, sv.Engine.ReorderSkip, sv.Engine.ReorderHeld, sv.Engine.ReorderHoldMs, sv.Engine.UniqueRxBytes, sv.Engine.ReorderInOrd, sv.Engine.ReorderWaited, sv.Engine.ReorderLate, sv.Engine.NacksSent, cv.Engine.Retransmits, cv.Engine.RtxPremature, cv.Engine.NacksRecv, cv.Engine.NoLinkDrops)
						t.Logf("t+%ds srv[%s] txBps=%.0f rtx=%d prem=%d nacks=%d | cli[%s] gaps=%d gaveup=%d skip=%d hold=%.0f urx=%d inord=%d waited=%d held=%d late=%d", i+1, sl, sv.Engine.TxBps, sv.Engine.Retransmits, sv.Engine.RtxPremature, sv.Engine.NacksRecv, cl, cv.Engine.GapsDetected, cv.Engine.GapsGaveUp, cv.Engine.ReorderSkip, cv.Engine.ReorderHoldMs, cv.Engine.UniqueRxBytes, cv.Engine.ReorderInOrd, cv.Engine.ReorderWaited, cv.Engine.ReorderHeld, cv.Engine.ReorderLate)
					}
				}()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			sv := r.srv.Snapshot(0).Sessions[0]
			for _, l := range sv.Links {
				t.Logf("server link %s state=%s metered=%v share=%.2f tx=%d queue=%d pace=%.0f benched=%v", l.Name, l.State, l.Metered, l.Share, l.TxBytes, l.Queue, l.PaceBps, l.Benched)
			}
			t.Logf("server engine: %+v", sv.Engine)
			cv := r.cli.Snapshot(0)
			for _, l := range cv.Links {
				t.Logf("client link %s state=%s rx=%d", l.Name, l.State, l.RxBytes)
			}
			t.Logf("client engine: %+v auto=%+v", cv.Engine, cv.Auto)
			t.Fatalf("failover to metered phone failed: %v after %d bytes", err, buf.Len())
		}
	}
	if !bytes.Equal(buf.Bytes(), body) {
		t.Fatalf("payload corrupted after failover: %d bytes", buf.Len())
	}
}

func mustGet(t *testing.T, r *rig, url string) *http.Response {
	t.Helper()
	resp, err := r.os.httpClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
