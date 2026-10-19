package client

import (
	"testing"
	"time"
)

func TestDirectMeterSubtractsBond(t *testing.T) {
	var ifRx, ifTx uint64
	m := directMeter{readCounters: func(string) (uint64, uint64, bool) { return ifRx, ifTx, true }}
	now := time.Unix(0, 0)
	var bondRx, bondTx uint64
	m.sample("eth0", bondRx, bondTx, now)
	for i := 0; i < 20; i++ {
		now = now.Add(time.Second)
		// 5 MB/s on the interface, 1 MB/s of it bond: 4 MB/s = 32 Mbit/s direct.
		ifRx += 5_000_000
		bondRx += 1_000_000
		ifTx += 100_000
		bondTx += 200_000 // bond accounting overshoots: never negative
		m.sample("eth0", bondRx, bondTx, now)
	}
	v := m.view()
	if v == nil || v.Via != "eth0" {
		t.Fatalf("view = %+v", v)
	}
	if v.RxBps < 31.9e6 || v.RxBps > 32.1e6 {
		t.Errorf("rx = %.0f, want 32e6", v.RxBps)
	}
	if v.TxBps != 0 {
		t.Errorf("tx = %.0f, want 0", v.TxBps)
	}

	// A link reconnect restarts the bond counters: count only the new bytes.
	now = now.Add(time.Second)
	ifRx += 5_000_000
	m.sample("eth0", 1_000_000, 0, now)
	if m.outRx != 20*4_000_000+4_000_000 {
		t.Errorf("outRx = %d after bond counter reset", m.outRx)
	}

	m.sample("", 0, 0, now)
	if m.view() != nil {
		t.Error("view should be nil when not direct")
	}
}
