package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorderWriteQueryAndPrune(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRecorder(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 100; i++ {
		r.Write(Record{T: now.Add(time.Duration(i-100) * 10 * time.Second).UnixMilli(), Kind: "sample", Client: "slate-ax",
			Sample: &HistSample{Mode: "balancing", DownBps: float64(i) * 1e6}})
	}
	r.Write(Record{T: now.UnixMilli(), Kind: "event", Client: "slate-ax", Event: "link sta0 benched"})
	r.Write(Record{T: now.UnixMilli(), Kind: "conn", Client: "slate-ax", Conn: &HistConn{Host: "api.torbox.app", DownBytes: 5e8}})
	r.Write(Record{T: now.UnixMilli(), Kind: "sample", Client: "laptop", Sample: &HistSample{}})

	s, e, c := r.Query(now.Add(-time.Hour), now.Add(time.Minute), "slate-ax", 0)
	if len(s) != 100 || len(e) != 1 || len(c) != 1 || c[0].Conn.Host != "api.torbox.app" {
		t.Fatalf("query: %d samples %d events %d conns", len(s), len(e), len(c))
	}
	// Downsampling keeps peaks.
	s, _, _ = r.Query(now.Add(-time.Hour), now.Add(time.Minute), "slate-ax", 10)
	if len(s) != 10 || s[len(s)-1].Sample.DownBps != 99e6 {
		t.Fatalf("downsample: %d points, last peak %v", len(s), s[len(s)-1].Sample.DownBps)
	}
	// Old day files are pruned; today's stays.
	old := filepath.Join(dir, dayFile(now.AddDate(0, 0, -30)))
	os.WriteFile(old, []byte("{}\n"), 0o644)
	r.Prune(14)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old history not pruned")
	}
	if _, err := os.Stat(filepath.Join(dir, dayFile(now))); err != nil {
		t.Fatal("today's history pruned")
	}
	r.Close()
}
