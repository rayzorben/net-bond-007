package client

import (
	"io"
	"log/slog"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/rayzorben/net-bond-007/internal/config"
)

// dnsAnswer builds a response to q with a CNAME chain then A records.
func dnsAnswer(t *testing.T, q string, cname string, owner string, ips ...[4]byte) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true, ID: 1})
	b.EnableCompression()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(b.StartQuestions())
	must(b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(q), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}))
	must(b.StartAnswers())
	if cname != "" {
		must(b.CNAMEResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(q), Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 60},
			dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(cname)}))
	}
	for _, ip := range ips {
		must(b.AResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(owner), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: ip}))
	}
	msg, err := b.Finish()
	must(err)
	return msg
}

func TestMatchDNS(t *testing.T) {
	rules := []config.Bypass{{Host: "seatgeek.com"}, {Host: "*.px-cloud.net"}}
	// A CDN name answers for the site: matched by the question.
	got := matchDNS(rules, dnsAnswer(t, "www.seatgeek.com.", "edge.akamai.net.", "edge.akamai.net.", [4]byte{1, 2, 3, 4}, [4]byte{5, 6, 7, 8}))
	if len(got) != 2 || got[netip.MustParseAddr("1.2.3.4")] != "seatgeek.com" {
		t.Fatalf("question match: %v", got)
	}
	// Wildcard covers names under it, not the apex.
	if got := matchDNS(rules, dnsAnswer(t, "collector.px-cloud.net.", "", "collector.px-cloud.net.", [4]byte{9, 9, 9, 9})); len(got) != 1 {
		t.Fatalf("wildcard: %v", got)
	}
	if got := matchDNS(rules, dnsAnswer(t, "px-cloud.net.", "", "px-cloud.net.", [4]byte{9, 9, 9, 9})); len(got) != 0 {
		t.Fatalf("wildcard apex should not match: %v", got)
	}
	// Unrelated names stay out; private answers are ignored.
	if got := matchDNS(rules, dnsAnswer(t, "example.com.", "", "example.com.", [4]byte{1, 1, 1, 1})); len(got) != 0 {
		t.Fatalf("unrelated: %v", got)
	}
	if got := matchDNS(rules, dnsAnswer(t, "seatgeek.com.", "", "seatgeek.com.", [4]byte{192, 168, 1, 1})); len(got) != 0 {
		t.Fatalf("private: %v", got)
	}
	// Queries (not responses) are ignored.
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 2})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName("seatgeek.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	q, _ := b.Finish()
	if got := matchDNS(rules, q); len(got) != 0 {
		t.Fatalf("query: %v", got)
	}
}

func TestDNSPayload(t *testing.T) {
	msg := dnsAnswer(t, "seatgeek.com.", "", "seatgeek.com.", [4]byte{1, 2, 3, 4})
	pkt := make([]byte, 28+len(msg))
	pkt[0] = 0x45
	pkt[9] = 17
	pkt[20], pkt[21] = 0, 53 // source port
	n := 8 + len(msg)
	pkt[24], pkt[25] = byte(n>>8), byte(n)
	copy(pkt[28:], msg)
	got, ok := dnsPayload(pkt)
	if !ok || len(got) != len(msg) {
		t.Fatalf("payload: ok=%v len=%d want %d", ok, len(got), len(msg))
	}
	pkt[21] = 54
	if _, ok := dnsPayload(pkt); ok {
		t.Fatal("port 54 should not parse as DNS")
	}
	pkt[21], pkt[9] = 53, 6
	if _, ok := dnsPayload(pkt); ok {
		t.Fatal("TCP should not parse as DNS")
	}
}

func TestBypassSnoopAndConfig(t *testing.T) {
	c := New(config.NewMemory(config.DefaultClient()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cfg := c.cfg.Clone()
	cfg.Bypass = []config.Bypass{{Host: "stubhub.com"}, {Host: "off.example", Disabled: true}, {Host: "10.0.0.0/8"}}
	c.bypassConfig(cfg)
	c.bypassSnoop(ipv4UDP(dnsAnswer(t, "www.stubhub.com.", "", "www.stubhub.com.", [4]byte{3, 3, 3, 3})))
	c.bypassSnoop(ipv4UDP(dnsAnswer(t, "off.example.", "", "off.example.", [4]byte{4, 4, 4, 4})))
	if len(c.bypass.addrs) != 1 || c.bypass.addrs[netip.MustParseAddr("3.3.3.3")] == nil {
		t.Fatalf("addrs: %v", c.bypass.addrs)
	}
	// Removing the entry drops its addresses.
	cfg.Bypass = cfg.Bypass[2:]
	c.bypassConfig(cfg)
	if len(c.bypass.addrs) != 0 {
		t.Fatalf("addrs after removal: %v", c.bypass.addrs)
	}
}

func ipv4UDP(msg []byte) []byte {
	pkt := make([]byte, 28+len(msg))
	pkt[0], pkt[9] = 0x45, 17
	pkt[21] = 53
	n := 8 + len(msg)
	pkt[24], pkt[25] = byte(n>>8), byte(n)
	copy(pkt[28:], msg)
	return pkt
}
