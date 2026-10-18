// Package speedtest measures download and upload bandwidth against a
// Cloudflare-style speed test endpoint (GET /__down?bytes=N, POST /__up).
// The client uses it per connection (sockets pinned to one interface), the
// server for its own internet link.
package speedtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Options for one measurement.
type Options struct {
	URL      string        // base URL, e.g. https://speed.cloudflare.com
	Duration time.Duration // per direction
	Streams  int           // parallel transfers
	MaxBytes int64         // stop a direction early once this much moved
	// RequestBytes is the size of each download request and upload body
	// (default 25 MB). Fast hosts use bigger ones: fewer requests, so the
	// endpoint's rate limit is not hit.
	RequestBytes int64
	// FileURL, if set, makes Download fetch this plain file (read until the
	// time or data cap) instead of the /__down endpoint.
	FileURL string
	// NewClient returns an HTTP client for one stream (and for pings).
	NewClient func() *http.Client
}

func (o Options) base() string { return strings.TrimRight(o.URL, "/") }

func (o Options) reqBytes() int64 {
	if o.RequestBytes > 0 {
		return o.RequestBytes
	}
	return 25_000_000
}

// errStop ends a stream for good: the endpoint is refusing (rate limit), and
// retrying would only hammer it.
var errStop = errors.New("stop")

type statusError struct{ status string }

func (e statusError) Error() string { return e.status }

// Refused reports whether err is the endpoint refusing the test (rate limit
// or request too large) rather than the network failing.
func Refused(err error) bool {
	var se statusError
	return errors.As(err, &se) && (strings.HasPrefix(se.status, "429") || strings.HasPrefix(se.status, "403"))
}

// FilePing returns the time to fetch the first byte of a plain file URL in
// milliseconds (median of 3), or 0 if it failed.
func FilePing(cl *http.Client, url string) float64 {
	var pings []float64
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Range", "bytes=0-0")
		start := time.Now()
		resp, err := cl.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 400 && i > 0 {
			pings = append(pings, float64(time.Since(start))/float64(time.Millisecond))
		}
	}
	if len(pings) == 0 {
		return 0
	}
	sort.Float64s(pings)
	return pings[len(pings)/2]
}

// Ping returns the median time of a few empty requests in milliseconds
// (the first, which includes the TCP and TLS handshakes, is not counted),
// or 0 if none succeeded.
func Ping(o Options) float64 {
	cl := o.NewClient()
	defer cl.CloseIdleConnections()
	var pings []float64
	for i := 0; i < 6; i++ {
		start := time.Now()
		resp, err := cl.Get(o.base() + "/__down?bytes=0")
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if i > 0 {
			pings = append(pings, float64(time.Since(start))/float64(time.Millisecond))
		}
	}
	if len(pings) == 0 {
		return 0
	}
	sort.Float64s(pings)
	return pings[len(pings)/2]
}

// Download measures download bandwidth in Mbit/s and returns the bytes moved.
func Download(o Options) (mbps, total float64, err error) {
	return measure(o, func(ctx context.Context, cl *http.Client, n *atomic.Int64) error {
		u := fmt.Sprintf("%s/__down?bytes=%d", o.base(), o.reqBytes())
		if o.FileURL != "" {
			u = o.FileURL
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return statusError{resp.Status}
		}
		buf := make([]byte, 32<<10)
		for {
			k, err := resp.Body.Read(buf)
			n.Add(int64(k))
			if err != nil {
				return nil
			}
		}
	})
}

// Upload measures upload bandwidth in Mbit/s and returns the bytes moved.
func Upload(o Options) (mbps, total float64, err error) {
	return measure(o, func(ctx context.Context, cl *http.Client, n *atomic.Int64) error {
		body := &countingZeros{left: o.reqBytes(), n: n}
		req, _ := http.NewRequestWithContext(ctx, "POST", o.base()+"/__up", body)
		req.ContentLength = body.left
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return statusError{resp.Status}
		}
		return nil
	})
}

// measure runs o.Streams parallel transfers for o.Duration and returns the
// rate in Mbit/s, ignoring the first second (connection setup and slow
// start), plus the total bytes moved. If nothing moved after that first
// second (a very fast link hit the data cap or a rate limit early), the rate
// is taken over the whole time data was flowing instead.
func measure(o Options, transfer func(context.Context, *http.Client, *atomic.Int64) error) (mbps, total float64, err error) {
	d := o.Duration
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var n atomic.Int64
	var lastByte atomic.Int64 // unix nanos of the latest progress
	var wg sync.WaitGroup
	var errMu sync.Mutex
	for i := 0; i < max(o.Streams, 1); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl := o.NewClient()
			defer cl.CloseIdleConnections()
			for ctx.Err() == nil {
				if e := transfer(ctx, cl, &n); e != nil && ctx.Err() == nil {
					errMu.Lock()
					err = e
					errMu.Unlock()
					var se statusError
					if errors.As(e, &se) && strings.HasPrefix(se.status, "429") {
						return // rate limited: don't hammer the endpoint
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
		}()
	}
	t0 := time.Now()
	go func() { // stop early once the data cap is reached; note progress
		last := int64(0)
		for ctx.Err() == nil && (o.MaxBytes <= 0 || n.Load() < o.MaxBytes) {
			if v := n.Load(); v != last {
				last = v
				lastByte.Store(time.Now().UnixNano())
			}
			time.Sleep(20 * time.Millisecond)
		}
		if n.Load() != last {
			lastByte.Store(time.Now().UnixNano())
		}
		cancel()
	}()
	start, b0 := t0, int64(0)
	select {
	case <-time.After(min(time.Second, d/4)):
		start, b0 = time.Now(), n.Load()
	case <-ctx.Done(): // hit the data cap during warm-up: use the whole run
	}
	<-ctx.Done()
	el := time.Since(start).Seconds()
	b1 := n.Load()
	wg.Wait()
	if el > 0 {
		mbps = float64(b1-b0) * 8 / el / 1e6
	}
	if b1 == b0 && b1 > 0 {
		if flow := time.Duration(lastByte.Load() - t0.UnixNano()); flow > 0 {
			mbps = float64(b1) * 8 / flow.Seconds() / 1e6
		}
	}
	return mbps, float64(n.Load()), err
}

// countingZeros is an upload body of zeros that counts what is sent.
type countingZeros struct {
	left int64
	n    *atomic.Int64
}

func (z *countingZeros) Read(p []byte) (int, error) {
	if z.left <= 0 {
		return 0, io.EOF
	}
	k := min(int64(len(p)), z.left, 64<<10)
	clear(p[:k])
	z.left -= k
	z.n.Add(k)
	return int(k), nil
}
