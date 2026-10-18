package speedtest

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// A fast link that is rate limited after a burst still reports the rate it
// reached, and stops asking once refused.
func TestRateLimitedFastLinkReportsBurstRate(t *testing.T) {
	var served, refused atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__down", func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) > 4 { // 4 requests, then 429
			refused.Add(1)
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		n, _ := strconv.Atoi(r.URL.Query().Get("bytes"))
		buf := make([]byte, 64<<10)
		for n > 0 {
			k := min(n, len(buf))
			if _, err := w.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	o := Options{URL: ts.URL, Duration: 3 * time.Second, Streams: 2, RequestBytes: 5_000_000,
		NewClient: func() *http.Client { return &http.Client{} }}
	mbps, total, err := Download(o)
	if total < 20_000_000 || mbps <= 0 {
		t.Fatalf("burst then 429: %.0f Mbit/s over %.0f bytes (err %v)", mbps, total, err)
	}
	if r := refused.Load(); r > 4 {
		t.Fatalf("kept retrying after a 429: %d refused requests", r)
	}
}

// Requests carry our User-Agent: some hosts drop Go's default one.
func TestRequestsCarryUserAgent(t *testing.T) {
	var bad atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != UserAgent {
			bad.Add(1)
		}
	}))
	defer ts.Close()
	Ping(Options{URL: ts.URL, NewClient: func() *http.Client { return &http.Client{} }})
	FilePing(&http.Client{}, ts.URL+"/file")
	Download(Options{URL: ts.URL, Duration: 300 * time.Millisecond, Streams: 1, NewClient: func() *http.Client { return &http.Client{} }})
	if bad.Load() != 0 {
		t.Fatalf("%d requests without the net-bond-007 User-Agent", bad.Load())
	}
}
