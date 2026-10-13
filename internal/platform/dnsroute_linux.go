package platform

import (
	"bufio"
	"net/netip"
	"os"
	"strings"
)

// adGuardConfig is where GL.iNet keeps AdGuard Home's settings.
var adGuardConfig = "/etc/AdGuardHome/config.yaml"

// RouterDNSUpstreams returns the public IPv4 resolvers the router itself
// queries (AdGuard Home's upstream and bootstrap servers). Those queries are
// router-originated, which GL firmware routes outside the tunnel; sending
// them through it lets name lookups fail over with everything else.
func RouterDNSUpstreams() []netip.Addr {
	f, err := os.Open(adGuardConfig)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseAdGuardUpstreams(bufio.NewScanner(f))
}

func parseAdGuardUpstreams(sc *bufio.Scanner) []netip.Addr {
	var out []netip.Addr
	seen := map[netip.Addr]bool{}
	in := false
	for sc.Scan() {
		line := sc.Text()
		t := strings.TrimSpace(line)
		switch {
		case strings.HasSuffix(t, ":") && !strings.HasPrefix(t, "-"):
			key := strings.TrimSuffix(t, ":")
			in = key == "upstream_dns" || key == "bootstrap_dns" || key == "fallback_dns"
			continue
		case strings.Contains(t, ":") && !strings.HasPrefix(t, "-"):
			in = false // another key (possibly with an inline value)
			continue
		case !in || !strings.HasPrefix(t, "- "):
			continue
		}
		v := strings.Trim(strings.TrimSpace(strings.TrimPrefix(t, "- ")), `"'`)
		if i := strings.LastIndex(v, "]"); strings.HasPrefix(v, "[/") && i > 0 {
			v = v[i+1:] // [/domain/]server
		}
		if i := strings.Index(v, "://"); i >= 0 {
			v = v[i+3:]
		}
		v = strings.SplitN(v, "/", 2)[0]
		var a netip.Addr
		if ap, err := netip.ParseAddrPort(v); err == nil {
			a = ap.Addr()
		} else if a, err = netip.ParseAddr(v); err != nil {
			continue
		}
		if a.Is4() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}
