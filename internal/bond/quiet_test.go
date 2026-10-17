package bond

import (
	"sync"
	"testing"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
	"github.com/rayzorben/net-bond-007/internal/sched"
)

type frameLog struct {
	mu     sync.Mutex
	frames [][]byte
}

func (f *frameLog) send(b []byte) error {
	f.mu.Lock()
	f.frames = append(f.frames, append([]byte(nil), b...))
	f.mu.Unlock()
	return nil
}

func (f *frameLog) ofType(t proto.Type) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]byte
	for _, b := range f.frames {
		if h, err := proto.ParseHeader(b); err == nil && h.Type == t {
			out = append(out, b)
		}
	}
	return out
}

func TestQuietRoundTrip(t *testing.T) {
	f := proto.AppendQuiet(nil, 9, 2, 1, 140)
	id, ms, err := proto.ParseQuiet(f)
	if err != nil || id != 1 || ms != 140 {
		t.Fatalf("%d %d %v", id, ms, err)
	}
	if _, _, err := proto.ParseQuiet(f[:proto.HeaderLen+1]); err == nil {
		t.Fatal("short QUIET parsed")
	}
}

// The receiver reports a busy link that went silent, over a link still
// heard, and only once it was busy and silent long enough.
func TestWatchSilenceReportsBusyLinkOutage(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	var aLog, bLog frameLog
	a := e.AddLink(1, "starlink", "sim", aLog.send)
	b := e.AddLink(2, "phone", "sim", bLog.send)
	a.SetState(StateUp)
	b.SetState(StateUp)
	now := time.Now()
	tick := func(aPkts int, aHeard, bHeard bool) {
		for i := 0; i < aPkts; i++ {
			a.Rx.Add(1400)
		}
		if aHeard {
			a.LastRx.Store(now.UnixNano())
		}
		if bHeard {
			b.Rx.Add(100)
			b.LastRx.Store(now.UnixNano())
		}
		e.watchSilence(now)
		now = now.Add(10 * time.Millisecond)
	}
	// An idle link going silent is not an outage.
	for i := 0; i < 20; i++ {
		tick(0, i < 2, true)
	}
	if n := len(bLog.ofType(proto.TypeQuiet)); n != 0 {
		t.Fatalf("idle link reported silent (%d)", n)
	}
	// Busy (40 packets per 10 ms), then silent.
	for i := 0; i < 15; i++ {
		tick(40, true, true)
	}
	for i := 0; i < 4; i++ {
		tick(0, false, true)
	}
	if n := len(bLog.ofType(proto.TypeQuiet)); n != 0 {
		t.Fatalf("reported after only 40 ms of silence (%d)", n)
	}
	for i := 0; i < 6; i++ { // 50-100 ms of silence
		tick(0, false, true)
	}
	q := bLog.ofType(proto.TypeQuiet)
	if len(q) < 2 || len(q) > 3 {
		t.Fatalf("want a QUIET about every 25 ms, got %d", len(q))
	}
	if id, ms, _ := proto.ParseQuiet(q[0]); id != a.ID || ms < 50 {
		t.Fatalf("QUIET names link %d silent %d ms", id, ms)
	}
	if n := len(aLog.ofType(proto.TypeQuiet)); n != 0 {
		t.Fatal("QUIET sent on the silent link itself")
	}
	if e.QuietsSent.Load() != uint64(len(q)) {
		t.Fatal("QuietsSent")
	}
}

// The sender resends what went only on the silent link over another one at
// once, and keeps new packets off it while the peer reports it silent.
func TestHandleQuietReinjectsAndAvoidsLink(t *testing.T) {
	e := New(1, func([]byte) {})
	defer e.Close()
	e.SetARQ(true)
	var aLog, bLog frameLog
	a := e.AddLink(1, "starlink", "sim", aLog.send)
	b := e.AddLink(2, "phone", "sim", bLog.send)
	a.SetOneWayDelay(20 * time.Millisecond)
	b.SetOneWayDelay(60 * time.Millisecond)
	for _, l := range []*Link{a, b} {
		l.SetState(StateUp)
	}
	e.SetPolicy(func() (p sched.Policy) { p = e.Policy(); p.Mode = sched.ModeSpeed; return }())
	for i := uint32(1); i <= 50; i++ {
		e.Send(fakePkt(i))
	}
	time.Sleep(20 * time.Millisecond)
	if len(aLog.ofType(proto.TypeData)) != 50 {
		t.Fatalf("setup: want all 50 on the fast link, got %d/%d", len(aLog.ofType(proto.TypeData)), len(bLog.ofType(proto.TypeData)))
	}
	// The receiver says: nothing on starlink for the last 120 ms.
	e.HandleQuiet(b, proto.AppendQuiet(nil, 1, b.ID, a.ID, 120))
	time.Sleep(20 * time.Millisecond)
	if got := len(bLog.ofType(proto.TypeData)); got != 50 || e.Reinjected.Load() != 50 {
		t.Fatalf("reinjected %d on the phone (counter %d), want all 50", got, e.Reinjected.Load())
	}
	if !a.Storming(time.Now().UnixNano()) || !a.PeerQuiet(time.Now().UnixNano()) {
		t.Fatal("silent link not flagged")
	}
	// A repeat QUIET doesn't resend the same packets again.
	e.HandleQuiet(b, proto.AppendQuiet(nil, 1, b.ID, a.ID, 145))
	time.Sleep(10 * time.Millisecond)
	if got := len(bLog.ofType(proto.TypeData)); got != 50 {
		t.Fatalf("repeat QUIET resent again: %d", got)
	}
	// New packets avoid the silent link for now ...
	e.Send(fakePkt(99))
	time.Sleep(10 * time.Millisecond)
	if got := len(bLog.ofType(proto.TypeData)); got != 51 {
		t.Fatalf("new packet went on the silent link")
	}
	// ... and use it again once the reports stop.
	time.Sleep(quietHold)
	e.Send(fakePkt(100))
	time.Sleep(10 * time.Millisecond)
	if got := len(aLog.ofType(proto.TypeData)); got != 51 {
		t.Fatalf("link not used again after the silence: %d on starlink", got)
	}
}
