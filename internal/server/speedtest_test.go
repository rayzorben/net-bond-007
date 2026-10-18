package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/config"
)

// fakeSpeedServer is a Cloudflare-style endpoint served from memory.
func fakeSpeedServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__down", func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc("POST /__up", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
	})
	return httptest.NewServer(mux)
}

func TestServerSpeedTestMeasuresAndFlagsLimits(t *testing.T) {
	ts := fakeSpeedServer()
	defer ts.Close()
	r := runServerSpeedTest(ts.URL, 2*time.Second, 1)
	if r.Error != "" || r.DownMbps < 100 || r.UpMbps < 100 {
		t.Fatalf("loopback test: %+v", r)
	}
	if r.Limited {
		t.Fatal("loopback flagged limited at a 1 Mbit/s threshold")
	}
	if r := runServerSpeedTest(ts.URL, 2*time.Second, 1e9); !r.Limited {
		t.Fatalf("not flagged limited below a huge threshold: %+v", r)
	}
	if r := runServerSpeedTest("http://127.0.0.1:1", time.Second, 1); r.Error == "" {
		t.Fatal("no error for an unreachable test server")
	}
}

func TestServerSpeedTestRunsInBackgroundAndWarns(t *testing.T) {
	ts := fakeSpeedServer()
	defer ts.Close()
	cfg := config.DefaultServer()
	cfg.Speed.URL, cfg.Speed.Seconds, cfg.Speed.LimitedMbps = ts.URL, 3, 1_000_000
	s := &Server{cfg: config.NewMemory(cfg), log: slog.New(slog.NewTextHandler(io.Discard, nil)), started: time.Now()}
	if err := s.RunSpeedTest("manual"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunSpeedTest("manual"); err != errSpeedBusy {
		t.Fatalf("second concurrent test: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for s.speedView().Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	v := s.speedView()
	if v.Last == nil || v.Last.Reason != "manual" || !v.Last.Limited || v.NextAt == nil {
		t.Fatalf("view after a test: %+v", v)
	}
	if w := s.speedWarning(); !strings.Contains(w, "tested slow") {
		t.Fatalf("no dashboard warning for a limited server: %q", w)
	}
}

// When the test server refuses (rate limit), download is measured against
// the fallback file instead, and upload is reported as not measured.
func TestServerSpeedTestFallsBackWhenRefused(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("bytes") == "0" {
			return // pings still answer
		}
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer refusing.Close()
	files := fakeSpeedServer()
	defer files.Close()
	file := files.URL + "/__down?bytes=500000000" // stands in for a plain big file
	r := runServerSpeedTest(refusing.URL, 2*time.Second, 1, "http://127.0.0.1:1/none.bin", file)
	if r.Error != "" || r.DownMbps <= 0 || r.UpMbps != 0 || r.Source != file {
		t.Fatalf("fallback: %+v", r)
	}
}
