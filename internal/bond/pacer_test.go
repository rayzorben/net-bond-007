package bond

import (
	"math/rand"
	"testing"
	"time"
)

// saturate offers more traffic than the pacer allows for d, in 1ms steps,
// the way a busy sender would.
func saturate(p *Pacer, now time.Time, d time.Duration) time.Time {
	for end := now.Add(d); now.Before(end); now = now.Add(time.Millisecond) {
		for p.Allow(1400, now) {
		}
	}
	return now
}

// offer sends at most capBps worth of traffic for d (a link of finite
// capacity), in 1ms steps.
func offer(p *Pacer, now time.Time, d time.Duration, capBps float64) time.Time {
	per := capBps / 8 / 1000 // bytes per ms
	for end := now.Add(d); now.Before(end); now = now.Add(time.Millisecond) {
		for sent := 0.0; sent+1400 <= per && p.Allow(1400, now); sent += 1400 {
		}
	}
	return now
}

func TestPacerTokenBucket(t *testing.T) {
	var p Pacer
	p.Configure(true, 8e6, 0) // 1 MB/s
	now := time.Now()
	sent := 0
	for i := 0; i < 1000; i++ {
		if p.Allow(1000, now) {
			sent++
		}
	}
	// Initial burst is max(256KB, 50ms of rate) = 256KB.
	if sent < 255 || sent > 265 {
		t.Fatalf("burst allowed %d packets", sent)
	}
	// One second later about 1MB more is available (capped at the burst).
	later := now.Add(10 * time.Millisecond)
	extra := 0
	for p.Allow(1000, later) {
		extra++
	}
	if extra < 0 || extra > 12 {
		t.Fatalf("refill after 10ms allowed %d", extra)
	}
}

func TestPacerDisabledAllowsAll(t *testing.T) {
	var p Pacer
	p.Configure(false, 1e6, 0)
	for i := 0; i < 10000; i++ {
		if !p.Allow(1500, time.Now()) {
			t.Fatal("disabled pacer limited traffic")
		}
	}
	if p.Rate() != 0 {
		t.Fatal("rate should read 0 when disabled")
	}
}

func TestPacerProbesUpWhenHealthyAndBacksOffOnBloat(t *testing.T) {
	var p Pacer
	p.Configure(true, 10e6, 0)
	now := time.Now()
	base := 26 * time.Millisecond
	// Demand exceeds the rate and RTT is clean: rate grows.
	for i := 0; i < 20; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, base+2*time.Millisecond, base, 0, 0)
	}
	grown := p.Rate()
	if grown < 50e6 {
		t.Fatalf("rate should have grown well past 10Mbit, got %.1f", grown/1e6)
	}
	// Queue builds: RTT inflates by 60ms. Rate must drop.
	for i := 0; i < 5; i++ {
		now = now.Add(250 * time.Millisecond)
		p.Govern(now, base+60*time.Millisecond, base, 0, 0)
	}
	if p.Rate() > grown*0.5 {
		t.Fatalf("rate should back off on bloat: %.1f -> %.1f", grown/1e6, p.Rate()/1e6)
	}
	// Not in demand: rate holds steady.
	r := p.Rate()
	for i := 0; i < 5; i++ {
		now = now.Add(250 * time.Millisecond)
		p.Govern(now, base, base, 0, 0)
	}
	if p.Rate() != r {
		t.Fatal("rate changed without demand")
	}
}

func TestPacerDoesNotInflateBeyondUse(t *testing.T) {
	var p Pacer
	p.Configure(true, 10e6, 0)
	now := time.Now()
	// The link only ever carries ~40 Mbit/s but keeps hitting microbursts.
	for i := 0; i < 80; i++ {
		now = now.Add(250 * time.Millisecond)
		p.Force(int(40e6/8*0.25), now)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	if r := p.Rate(); r > 61e6 || r < 40e6 {
		t.Fatalf("rate should settle near 1.5x actual use (60Mbit), got %.1f", r/1e6)
	}
	// Idle for a while: the learned rate is remembered.
	r := p.Rate()
	for i := 0; i < 20; i++ {
		now = now.Add(250 * time.Millisecond)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	if p.Rate() != r {
		t.Fatalf("idle changed the rate: %.1f -> %.1f", r/1e6, p.Rate()/1e6)
	}
}

func TestPacerIgnoresModerateRandomLoss(t *testing.T) {
	var p Pacer
	p.Configure(true, 10e6, 0)
	now := time.Now()
	for i := 0; i < 20; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, 46*time.Millisecond, 46*time.Millisecond, 0.05, 0) // lossy Wi-Fi, no bloat
	}
	if p.Rate() < 30e6 {
		t.Fatalf("5%% random loss should not throttle the link: %.1f", p.Rate()/1e6)
	}
	p.Tuning(0, 0.02)
	r := p.Rate()
	for i := 0; i < 5; i++ {
		now = now.Add(250 * time.Millisecond)
		p.Govern(now, 46*time.Millisecond, 46*time.Millisecond, 0.05, 0)
	}
	if p.Rate() >= r {
		t.Fatal("custom loss threshold not applied")
	}
}

func TestPacerRespectsMaxAndMin(t *testing.T) {
	var p Pacer
	p.Configure(true, 10e6, 12e6)
	now := time.Now()
	for i := 0; i < 50; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	if p.Rate() != 12e6 {
		t.Fatalf("max not honoured: %v", p.Rate())
	}
	for i := 0; i < 100; i++ {
		now = now.Add(time.Second)
		p.Govern(now, 500*time.Millisecond, 20*time.Millisecond, 0.5, 0)
	}
	if p.Rate() != minPaceRate {
		t.Fatalf("min not honoured: %v", p.Rate())
	}
}

func TestPacerBacksOffWhenDeliveryFallsShort(t *testing.T) {
	var p Pacer
	p.Configure(true, 40e6, 0)
	now := time.Now()
	// We send ~40 Mbit/s but the far end only receives 25: the link is
	// overflowing a deep buffer that hasn't shown up in RTT yet.
	for i := 0; i < 12; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 25e6)
	}
	if p.Rate() > 32e6 {
		t.Fatalf("should back off toward delivered rate, got %.1f", p.Rate()/1e6)
	}
}

func TestPacerGrowthCappedByDelivery(t *testing.T) {
	var p Pacer
	p.Configure(true, 10e6, 0)
	now := time.Now()
	for i := 0; i < 40; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 30e6) // link delivers 30
	}
	if p.Rate() > 43e6 {
		t.Fatalf("growth should stop near 1.4x delivered (42), got %.1f", p.Rate()/1e6)
	}
}

// The server configures a link before the client's policy arrives; the
// later, real start rate must take effect.
func TestPacerStartRateUpdatesUntilLearned(t *testing.T) {
	var p Pacer
	p.Configure(false, 1e6, 0) // placeholder policy at link creation
	p.Configure(true, 20e6, 0) // real policy arrives
	if p.Rate() != 20e6 {
		t.Fatalf("start rate not applied: %.1f", p.Rate()/1e6)
	}
	now := time.Now()
	for i := 0; i < 5; i++ {
		now = saturate(&p, now, 250*time.Millisecond)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	learned := p.Rate()
	if learned <= 20e6 {
		t.Fatalf("expected growth, got %.1f", learned/1e6)
	}
	p.Configure(true, 20e6, 0) // periodic policy resend must not reset learning
	if p.Rate() != learned {
		t.Fatalf("learned rate was reset: %.1f -> %.1f", learned/1e6, p.Rate()/1e6)
	}
}

// Starlink: base RTT ~25ms but samples jump around 25-70ms with satellite
// handovers. That is jitter, not congestion: the rate must not collapse.
func TestPacerToleratesStarlinkJitter(t *testing.T) {
	var p Pacer
	p.Configure(true, 20e6, 0)
	now := time.Now()
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 240; i++ { // 60 seconds
		now = offer(&p, now, 250*time.Millisecond, 50e6)
		rtt := 25*time.Millisecond + time.Duration(r.Intn(45))*time.Millisecond
		p.Govern(now, rtt, 20*time.Millisecond, 0.01, 0)
	}
	if p.Rate() < 20e6 {
		t.Fatalf("pacer collapsed on a jittery but uncongested link: %.1f Mbit/s", p.Rate()/1e6)
	}
}

// Base latency shifts permanently (new satellite / route): after the window
// rolls over, the new base is accepted instead of read as permanent bloat.
func TestPacerAdaptsToNewBaseRTT(t *testing.T) {
	var p Pacer
	p.Configure(true, 20e6, 0)
	now := time.Now()
	for i := 0; i < 20; i++ {
		now = offer(&p, now, 250*time.Millisecond, 50e6)
		p.Govern(now, 20*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	before := p.Rate()
	for i := 0; i < 120; i++ { // base jumps to 60ms for 30s
		now = offer(&p, now, 250*time.Millisecond, 50e6)
		p.Govern(now, 60*time.Millisecond, 20*time.Millisecond, 0, 0)
	}
	if p.Rate() < before*0.5 {
		t.Fatalf("permanent base-RTT shift treated as congestion: %.1f -> %.1f", before/1e6, p.Rate()/1e6)
	}
}

// Field regression (campground Wi-Fi, ~1.2 Mbit): mirrored copies drove its
// RTT from ~50ms to seconds, and the jitter allowance hid it. The pacer must
// back off toward what the link delivers.
func TestPacerBacksOffSlowLinkDespiteHugeJitter(t *testing.T) {
	var p Pacer
	p.Configure(true, 20e6, 0)
	now := time.Now()
	rtt := 50 * time.Millisecond
	for i := 0; i < 80; i++ { // 20s of flooding a 1.2 Mbit link
		now = offer(&p, now, 250*time.Millisecond, 40e6)
		// Queue grows: RTT climbs and swings wildly, loss is moderate.
		rtt = min(rtt+60*time.Millisecond, 4*time.Second)
		p.Govern(now, rtt+time.Duration(i%3)*300*time.Millisecond, 50*time.Millisecond, 0.08, 1.2e6)
	}
	if r := p.Rate(); r > 3e6 {
		t.Fatalf("pacer still feeding %.1f Mbit into a 1.2 Mbit link", r/1e6)
	}
}

// Growth is capped by delivery even below the configured start rate.
func TestPacerDeliveryCapBelowStartRate(t *testing.T) {
	var p Pacer
	p.Configure(true, 20e6, 0)
	now := time.Now()
	for i := 0; i < 40; i++ {
		now = offer(&p, now, 250*time.Millisecond, 40e6)
		p.Govern(now, 50*time.Millisecond, 50*time.Millisecond, 0, 1.2e6) // link delivers 1.2 Mbit
	}
	if r := p.Rate(); r > 2e6 {
		t.Fatalf("pacer at %.1f Mbit for a link delivering 1.2", r/1e6)
	}
}
