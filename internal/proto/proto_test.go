package proto

import (
	"bytes"
	"testing"
)

func TestHeaderRoundTrip(t *testing.T) {
	b := make([]byte, HeaderLen)
	in := Header{Type: TypePolicy, Session: 0xdeadbeef, Link: 7, Flags: 3}
	PutHeader(b, in)
	out, err := ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v want %+v", out, in)
	}
}

func TestParseHeaderErrors(t *testing.T) {
	if _, err := ParseHeader([]byte{Magic}); err != ErrShort {
		t.Fatalf("want ErrShort, got %v", err)
	}
	b := make([]byte, HeaderLen)
	PutHeader(b, Header{Type: TypeData})
	b[0] = 0
	if _, err := ParseHeader(b); err != ErrMagic {
		t.Fatalf("want ErrMagic, got %v", err)
	}
	b[0] = Magic
	b[1] = 9 << 4
	if _, err := ParseHeader(b); err != ErrVersion {
		t.Fatalf("want ErrVersion, got %v", err)
	}
}

func TestDataRoundTrip(t *testing.T) {
	payload := []byte{0x45, 1, 2, 3, 4, 5}
	f := AppendData(nil, 42, 3, 1<<40+5, payload)
	h, err := ParseHeader(f)
	if err != nil || h.Type != TypeData || h.Session != 42 || h.Link != 3 {
		t.Fatalf("bad header %+v %v", h, err)
	}
	seq, p, err := ParseData(f)
	if err != nil || seq != 1<<40+5 || !bytes.Equal(p, payload) {
		t.Fatalf("bad data seq=%d p=%v err=%v", seq, p, err)
	}
	SetLink(f, 9)
	h, _ = ParseHeader(f)
	if h.Link != 9 {
		t.Fatalf("SetLink failed: %d", h.Link)
	}
}

func TestAppendDataReusesBuffer(t *testing.T) {
	buf := make([]byte, 0, 2048)
	f := AppendData(buf, 1, 1, 1, make([]byte, 100))
	if &f[0] != &buf[:1][0] {
		t.Fatal("expected buffer reuse")
	}
}

func TestProbeRoundTrip(t *testing.T) {
	in := Probe{Seq: 9, SentAt: -123456789, RTTMicros: 26000, JitterMicro: 1200, LossPermil: 15, CapKbps: 120000, Weight: 250, State: 2, LastRTTMicr: 31000, RxKbps: 88000}
	f := AppendProbe(nil, 5, 2, in)
	out, err := ParseProbe(f)
	if err != nil || out != in {
		t.Fatalf("got %+v err %v", out, err)
	}
	if _, err := ParseProbe(f[:10]); err != ErrShort {
		t.Fatal("expected ErrShort")
	}
}

func TestProbeAckRoundTrip(t *testing.T) {
	in := ProbeAck{Seq: 1, SentAt: 99, RxPackets: 1 << 50, RxBytes: 77}
	out, err := ParseProbeAck(AppendProbeAck(nil, 5, 2, in))
	if err != nil || out != in {
		t.Fatalf("got %+v err %v", out, err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	in := Hello{Token: "t", ClientName: "laptop", LinkName: "wlan0", LinkKind: "wifi"}
	f, err := AppendJSON(nil, TypeHello, 77, 1, in)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := ParseHeader(f)
	if h.Type != TypeHello || h.Session != 77 {
		t.Fatalf("bad header %+v", h)
	}
	var out Hello
	if err := ParseJSON(f, &out); err != nil || out != in {
		t.Fatalf("got %+v err %v", out, err)
	}
}

func TestNackRoundTrip(t *testing.T) {
	in := []SeqRange{{Start: 10, Count: 1}, {Start: 1 << 40, Count: 300}}
	f := AppendNack(nil, 7, 2, in)
	h, _ := ParseHeader(f)
	if h.Type != TypeNack || h.Session != 7 {
		t.Fatalf("header %+v", h)
	}
	out, err := ParseNack(f)
	if err != nil || len(out) != 2 || out[0] != in[0] || out[1] != in[1] {
		t.Fatalf("got %+v %v", out, err)
	}
	if _, err := ParseNack(f[:HeaderLen+5]); err == nil {
		t.Fatal("truncated frame accepted")
	}
	many := make([]SeqRange, 500)
	if r, _ := ParseNack(AppendNack(nil, 1, 1, many)); len(r) != MaxNackRanges {
		t.Fatalf("ranges not capped: %d", len(r))
	}
}

func TestFECRoundTrip(t *testing.T) {
	seqs := []uint64{1000, 1002, 1005, 1000 + 65535}
	parity := []byte{1, 2, 3, 4, 5}
	f := AppendFEC(nil, 7, 3, seqs, 0xabcd, parity)
	h, err := ParseHeader(f)
	if err != nil || h.Type != TypeFEC || h.Session != 7 || h.Link != 3 {
		t.Fatalf("header %+v %v", h, err)
	}
	got, err := ParseFEC(f, nil)
	if err != nil || got.LenXor != 0xabcd || string(got.Parity) != string(parity) || len(got.Seqs) != len(seqs) {
		t.Fatalf("got %+v %v", got, err)
	}
	for i := range seqs {
		if got.Seqs[i] != seqs[i] {
			t.Fatalf("seq %d: %d want %d", i, got.Seqs[i], seqs[i])
		}
	}
	if _, err := ParseFEC(f[:HeaderLen+5], nil); err == nil {
		t.Fatal("short FEC frame parsed")
	}
}

// PROBE / PROBE_ACK gained a trailing FEC counter: frames from older peers
// (without it) still parse, and older parsers only check the minimum length.
func TestProbeFECCounterIsBackwardCompatible(t *testing.T) {
	p := Probe{Seq: 3, RxKbps: 9, FECRecovered: 42}
	f := AppendProbe(nil, 1, 2, p)
	if got, err := ParseProbe(f); err != nil || got.FECRecovered != 42 || got.RxKbps != 9 {
		t.Fatalf("got %+v %v", got, err)
	}
	old := f[:HeaderLen+ProbeBodyLen]
	if got, err := ParseProbe(old); err != nil || got.FECRecovered != 0 || got.RxKbps != 9 {
		t.Fatalf("old-format probe: %+v %v", got, err)
	}
	a := AppendProbeAck(nil, 1, 2, ProbeAck{Seq: 4, RxBytes: 5, FECRecovered: 6})
	if got, err := ParseProbeAck(a); err != nil || got.FECRecovered != 6 || got.RxBytes != 5 {
		t.Fatalf("ack %+v %v", got, err)
	}
	if got, err := ParseProbeAck(a[:HeaderLen+ProbeAckLen]); err != nil || got.FECRecovered != 0 {
		t.Fatalf("old-format ack %+v %v", got, err)
	}
}
