package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// History is a persistent record of what the bond did: periodic samples per
// client (links, mode, throughput), events (joins, state changes, scaling
// decisions) and a summary of every connection when it ends. One JSON line
// per record, one file per UTC day, pruned after the retention period.

type Record struct {
	T       int64       `json:"t"` // unix ms
	Kind    string      `json:"k"` // sample | event | conn
	Client  string      `json:"c"` // client name
	Session string      `json:"s,omitempty"`
	Sample  *HistSample `json:"sample,omitempty"`
	Event   string      `json:"e,omitempty"`
	Conn    *HistConn   `json:"conn,omitempty"`
}

type HistSample struct {
	Mode        string     `json:"mode"`
	Copies      int        `json:"copies"`
	DownBps     float64    `json:"down"` // to the client
	UpBps       float64    `json:"up"`
	Retransmits uint64     `json:"rtx"`
	Links       []HistLink `json:"links"`
}

type HistLink struct {
	Name    string  `json:"n"`
	Kind    string  `json:"kind"`
	State   string  `json:"st"`
	RTT     float64 `json:"rtt"`
	Loss    float64 `json:"loss"`
	DownBps float64 `json:"down"`
	UpBps   float64 `json:"up"`
	Benched bool    `json:"b,omitempty"`
}

type HistConn struct {
	Host      string             `json:"host,omitempty"`
	Outside   string             `json:"dst"`
	Proto     string             `json:"proto"`
	DownBytes uint64             `json:"down"`
	UpBytes   uint64             `json:"up"`
	Seconds   float64            `json:"secs"`
	AvgDown   float64            `json:"avgDown"`
	PeakDown  float64            `json:"peakDown"`
	Links     map[string]float64 `json:"links"` // share of packets per link
}

type Recorder struct {
	dir  string
	mu   sync.Mutex
	day  string
	f    *os.File
	w    *bufio.Writer
	last time.Time
}

func NewRecorder(dir string) (*Recorder, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Recorder{dir: dir}, nil
}

func dayFile(t time.Time) string { return "history-" + t.UTC().Format("2006-01-02") + ".jsonl" }

// Write appends one record (buffered; flushed at least every few seconds).
func (r *Recorder) Write(rec Record) {
	if r == nil {
		return
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.UnixMilli(rec.T)
	if d := dayFile(now); d != r.day || r.f == nil {
		r.closeLocked()
		f, err := os.OpenFile(filepath.Join(r.dir, d), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		r.f, r.w, r.day = f, bufio.NewWriterSize(f, 64<<10), d
	}
	r.w.Write(b)
	r.w.WriteByte('\n')
	if time.Since(r.last) > 5*time.Second {
		r.w.Flush()
		r.last = time.Now()
	}
}

func (r *Recorder) Flush() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.w != nil {
		r.w.Flush()
	}
	r.mu.Unlock()
}

func (r *Recorder) closeLocked() {
	if r.w != nil {
		r.w.Flush()
	}
	if r.f != nil {
		r.f.Close()
	}
	r.f, r.w = nil, nil
}

func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closeLocked()
	r.mu.Unlock()
}

// Prune deletes day files older than the retention period.
func (r *Recorder) Prune(days int) {
	if r == nil || days <= 0 {
		return
	}
	cutoff := dayFile(time.Now().AddDate(0, 0, -days))
	ents, _ := os.ReadDir(r.dir)
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "history-") && strings.HasSuffix(n, ".jsonl") && n < cutoff {
			os.Remove(filepath.Join(r.dir, n))
		}
	}
}

// Query reads records in [from, to], optionally for one client. Samples are
// averaged into at most maxPoints buckets per client so long ranges stay
// light. Connections and events come back newest first.
func (r *Recorder) Query(from, to time.Time, client string, maxPoints int) (samples, events, conns []Record) {
	if r == nil {
		return
	}
	r.Flush()
	fromMs, toMs := from.UnixMilli(), to.UnixMilli()
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to); d = d.Add(24 * time.Hour) {
		f, err := os.Open(filepath.Join(r.dir, dayFile(d)))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var rec Record
			if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.T < fromMs || rec.T > toMs {
				continue
			}
			if client != "" && rec.Client != client {
				continue
			}
			switch rec.Kind {
			case "sample":
				samples = append(samples, rec)
			case "event":
				events = append(events, rec)
			case "conn":
				conns = append(conns, rec)
			}
		}
		f.Close()
	}
	sort.Slice(events, func(i, j int) bool { return events[i].T > events[j].T })
	sort.Slice(conns, func(i, j int) bool { return conns[i].T > conns[j].T })
	return downsample(samples, maxPoints), events, conns
}

// downsample keeps at most maxPoints samples per client by taking the last
// sample of each bucket (and the maximum throughput within it, so peaks stay
// visible).
func downsample(in []Record, maxPoints int) []Record {
	if maxPoints <= 0 {
		return in
	}
	by := map[string][]Record{}
	for _, r := range in {
		by[r.Client] = append(by[r.Client], r)
	}
	var out []Record
	for _, rs := range by {
		if len(rs) <= maxPoints {
			out = append(out, rs...)
			continue
		}
		step := float64(len(rs)) / float64(maxPoints)
		for i := 0; i < maxPoints; i++ {
			lo, hi := int(float64(i)*step), int(float64(i+1)*step)
			if hi > len(rs) {
				hi = len(rs)
			}
			b := rs[hi-1]
			s := *b.Sample
			for _, x := range rs[lo:hi] {
				s.DownBps = max(s.DownBps, x.Sample.DownBps)
				s.UpBps = max(s.UpBps, x.Sample.UpBps)
			}
			b.Sample = &s
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}
