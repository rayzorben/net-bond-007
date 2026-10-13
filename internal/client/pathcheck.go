package client

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/platform"
)

// Path check: which network does each link really reach the internet
// through? A phone hotspot on Wi-Fi, or a campground Wi-Fi fed by the same
// Starlink, exits from the same provider as another link and fails with it.

type PathResult struct {
	Link     string  `json:"link"`
	Kind     string  `json:"kind"`
	IP       string  `json:"ip,omitempty"`
	Colo     string  `json:"colo,omitempty"` // Cloudflare data centre that answered
	Country  string  `json:"country,omitempty"`
	PTR      string  `json:"ptr,omitempty"`
	Provider string  `json:"provider,omitempty"`
	Ms       float64 `json:"ms"`
	Error    string  `json:"error,omitempty"`
}

type PathReport struct {
	At       time.Time    `json:"at"`
	Results  []PathResult `json:"results"`
	Warnings []string     `json:"warnings"`
}

// Known egress ranges and reverse-DNS hints. Best effort.
var providerPrefixes = []struct {
	p    netip.Prefix
	name string
}{
	{netip.MustParsePrefix("98.97.0.0/16"), "Starlink (SpaceX)"},
	{netip.MustParsePrefix("129.222.0.0/16"), "Starlink (SpaceX)"},
	{netip.MustParsePrefix("143.105.0.0/16"), "Starlink (SpaceX)"},
	{netip.MustParsePrefix("172.56.0.0/14"), "T-Mobile"},
	{netip.MustParsePrefix("172.32.0.0/11"), "T-Mobile"},
	{netip.MustParsePrefix("208.54.0.0/16"), "T-Mobile"},
	{netip.MustParsePrefix("100.128.0.0/10"), "Verizon Wireless"},
	{netip.MustParsePrefix("174.192.0.0/10"), "Verizon Wireless"},
	{netip.MustParsePrefix("166.128.0.0/9"), "AT&T Mobility"},
	{netip.MustParsePrefix("107.64.0.0/10"), "AT&T Mobility"},
}

var ptrHints = []struct{ sub, name string }{
	{"starlink", "Starlink (SpaceX)"},
	{"tmodns", "T-Mobile"}, {"t-mobile", "T-Mobile"},
	{"myvzw", "Verizon Wireless"}, {"verizon", "Verizon"},
	{"mycingular", "AT&T Mobility"}, {"att.net", "AT&T"},
	{"comcast", "Comcast"}, {"spectrum", "Spectrum"}, {"charter", "Spectrum"}, {"cox.net", "Cox"},
}

// traceURL reports the caller's public address (overridable in tests).
var traceURL = "https://1.1.1.1/cdn-cgi/trace"

func providerFor(ip netip.Addr, ptr string) string {
	for _, h := range ptrHints {
		if strings.Contains(strings.ToLower(ptr), h.sub) {
			return h.name
		}
	}
	for _, pp := range providerPrefixes {
		if pp.p.Contains(ip) {
			return pp.name
		}
	}
	return ""
}

// PathCheck asks Cloudflare's trace endpoint, over each link separately,
// what public address the request came from.
func (c *Client) PathCheck() PathReport {
	c.mu.Lock()
	var its []platform.Iface
	for _, l := range c.links {
		if l.relayTo.IsValid() {
			continue // a phone relay is not an interface of this machine
		}
		its = append(its, l.iface)
	}
	c.mu.Unlock()
	rep := PathReport{At: time.Now(), Results: make([]PathResult, len(its))}
	var wg sync.WaitGroup
	for i, it := range its {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rep.Results[i] = tracePath(it)
		}()
	}
	wg.Wait()
	sort.Slice(rep.Results, func(i, j int) bool { return rep.Results[i].Link < rep.Results[j].Link })
	rep.Warnings = pathWarnings(rep.Results)
	return rep
}

func pathWarnings(rs []PathResult) []string {
	var w []string
	for i := range rs {
		for j := i + 1; j < len(rs); j++ {
			a, b := rs[i], rs[j]
			switch {
			case a.IP == "" || b.IP == "":
			case a.IP == b.IP:
				w = append(w, fmt.Sprintf("%s and %s exit from the same public address (%s): one is routed through the other, so bonding them adds nothing.", a.Link, b.Link, a.IP))
			case a.Provider != "" && a.Provider == b.Provider:
				w = append(w, fmt.Sprintf("%s and %s both reach the internet through %s: they share an upstream and will likely fail together.", a.Link, b.Link, a.Provider))
			}
		}
	}
	return w
}

func tracePath(it platform.Iface) PathResult {
	r := PathResult{Link: it.Name, Kind: it.Kind}
	d := &net.Dialer{Timeout: 6 * time.Second, Control: platform.BindControl(it.Name, it.Index)}
	if src := it.PrimaryIPv4(); src.IsValid() {
		d.LocalAddr = &net.TCPAddr{IP: src.AsSlice()}
	}
	cl := &http.Client{Timeout: 8 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		DisableKeepAlives: true,
	}}
	start := time.Now()
	resp, err := cl.Get(traceURL)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.Ms = float64(time.Since(start)) / float64(time.Millisecond)
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), "=")
		switch k {
		case "ip":
			r.IP = v
		case "colo":
			r.Colo = v
		case "loc":
			r.Country = v
		}
	}
	ip, err := netip.ParseAddr(r.IP)
	if err != nil {
		r.Error = "no address in trace"
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if names, err := net.DefaultResolver.LookupAddr(ctx, r.IP); err == nil && len(names) > 0 {
		r.PTR = strings.TrimSuffix(names[0], ".")
	}
	r.Provider = providerFor(ip, r.PTR)
	return r
}
