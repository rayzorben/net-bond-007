package client

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/platform"
)

func TestIdleLimitSteps(t *testing.T) {
	sc := config.DefaultClient().SpeedTest // 1 Mbit, +1 every 15 min, max 5
	cases := map[time.Duration]float64{0: 1, 14 * time.Minute: 1, 15 * time.Minute: 2, 45 * time.Minute: 4, 10 * time.Hour: 5}
	for wait, want := range cases {
		if got := idleLimit(sc, wait); got != want {
			t.Errorf("after %v: limit %.0f, want %.0f", wait, got, want)
		}
	}
}

// A full test against a local Cloudflare-style endpoint.
func TestRunSpeedTest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cdn-cgi/trace":
			fmt.Fprint(w, "ip=172.56.78.119\ncolo=MIA\nloc=US\n")
		case "/__down":
			n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
			buf := make([]byte, 64<<10)
			for n > 0 {
				k := min(n, len(buf))
				if _, err := w.Write(buf[:k]); err != nil {
					return
				}
				n -= k
			}
		case "/__up":
			io.Copy(io.Discard, r.Body)
		}
	}))
	defer srv.Close()
	old := traceURL
	traceURL = srv.URL + "/cdn-cgi/trace"
	defer func() { traceURL = old }()

	it := platform.Iface{Name: "lo", Index: 1, IPv4: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8")}}
	sc := config.DefaultClient().SpeedTest
	sc.URL, sc.Seconds = srv.URL, 3
	oldMax := speedMaxBytes
	speedMaxBytes = 2_000_000_000
	defer func() { speedMaxBytes = oldMax }()
	var phases []string
	r := runSpeedTest(it, sc, func(p string) { phases = append(phases, p) })
	if r.Error != "" {
		t.Fatalf("speed test error: %s", r.Error)
	}
	if r.DownMbps < 50 || r.UpMbps < 50 || r.PublicIP != "172.56.78.119" || r.Provider != "T-Mobile" {
		t.Fatalf("result: %+v", r)
	}
	if len(phases) != 4 {
		t.Fatalf("phases: %v", phases)
	}
	if r.UsedMB > 4500 {
		t.Fatalf("data cap ignored: %.0f MB", r.UsedMB)
	}
	t.Logf("loopback: down %.0f Mbit/s, up %.0f Mbit/s, ping %.2f ms, %v MB", r.DownMbps, r.UpMbps, r.PingMs, int(r.UsedMB))
}
