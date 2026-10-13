package bond

import (
	"math/rand"
	"testing"
	"time"
)

func TestReliabilityFallsFastRisesSlow(t *testing.T) {
	var r Reliability
	now := time.Now()
	fall, rise := 10*time.Second, 60*time.Second
	for i := 0; i < 240; i++ { // 60s of a clean link
		now = now.Add(250 * time.Millisecond)
		r.Observe(true, now, fall, rise)
	}
	if r.Value() < 0.95 {
		t.Fatalf("clean link should be trusted, got %.3f", r.Value())
	}
	for i := 0; i < 40; i++ { // 10s outage (mountain pass)
		now = now.Add(250 * time.Millisecond)
		r.Observe(false, now, fall, rise)
	}
	afterOutage := r.Value()
	if afterOutage > 0.45 {
		t.Fatalf("10s outage should cut trust quickly, got %.3f", afterOutage)
	}
	for i := 0; i < 40; i++ { // 10s clean again
		now = now.Add(250 * time.Millisecond)
		r.Observe(true, now, fall, rise)
	}
	if gain := r.Value() - afterOutage; gain > 0.3 {
		t.Fatalf("trust should be re-earned slowly; regained %.3f in 10s", gain)
	}
}

func TestReliabilityIntermittentLinkSettlesLow(t *testing.T) {
	var r Reliability
	now := time.Now()
	for i := 0; i < 1200; i++ { // 5 min: works 4s, drops 4s
		now = now.Add(250 * time.Millisecond)
		r.Observe((i/16)%2 == 0, now, 10*time.Second, 60*time.Second)
	}
	if v := r.Value(); v > 0.6 {
		t.Fatalf("an on/off link must not look reliable: %.3f", v)
	}
}

func TestCopiesForScenarios(t *testing.T) {
	cases := []struct {
		name   string
		rels   []float64
		target float64
		want   int
	}{
		{"mountains: both flaky", []float64{0.8, 0.75}, 0.995, 2},
		{"rv park: both clean", []float64{0.999, 0.998}, 0.995, 1},
		{"one clean one flaky", []float64{0.999, 0.6}, 0.995, 1},
		{"three ok links", []float64{0.95, 0.94, 0.93}, 0.999, 3},
		{"three ok links, looser target", []float64{0.95, 0.94, 0.93}, 0.995, 2},
		{"nothing meets target", []float64{0.5, 0.5}, 0.999, 2},
		{"single link", []float64{0.7}, 0.995, 1},
		{"none", nil, 0.995, 0},
	}
	for _, c := range cases {
		if got := CopiesFor(c.rels, c.target); got != c.want {
			t.Errorf("%s: got %d copies, want %d", c.name, got, c.want)
		}
	}
}

// Field regression (campground Wi-Fi): ~13% random loss with no outages must
// read as a weak-but-working link (~0.87), not as flaky (~0.5).
func TestReliabilityRandomLossIsNotFlakiness(t *testing.T) {
	var r Reliability
	now := time.Now()
	rnd := rand.New(rand.NewSource(5))
	for i := 0; i < 2400; i++ { // 10 minutes
		now = now.Add(250 * time.Millisecond)
		r.Observe(rnd.Float64() >= 0.13, now, 10*time.Second, 60*time.Second)
	}
	if v := r.Value(); v < 0.8 || v > 0.93 {
		t.Fatalf("13%% random loss scored %.2f, want ~0.87", v)
	}
}
