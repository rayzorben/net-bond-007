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
	// NewClient returns an HTTP client for one stream (and for pings).
	NewClient func() *http.Client
}

func (o Options) base() string { return strings.TrimRight(o.URL, "/") }

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
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/__down?bytes=%d", o.base(), 25_000_000), nil)
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errors.New(resp.Status)
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
		body := &countingZeros{left: 25_000_000, n: n}
		req, _ := http.NewRequestWithContext(ctx, "POST", o.base()+"/__up", body)
		req.ContentLength = body.left
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := cl.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil
	})
}

// measure runs o.Streams parallel transfers for o.Duration and returns the
// rate in Mbit/s, ignoring the first second (connection setup and slow
// start), plus the total bytes moved.
func measure(o Options, transfer func(context.Context, *http.Client, *atomic.Int64) error) (mbps, total float64, err error) {
	d := o.Duration
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var n atomic.Int64
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
					time.Sleep(200 * time.Millisecond)
				}
			}
		}()
	}
	go func() { // stop early once the data cap is reached
		for ctx.Err() == nil && (o.MaxBytes <= 0 || n.Load() < o.MaxBytes) {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	start, b0 := time.Now(), int64(0)
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
