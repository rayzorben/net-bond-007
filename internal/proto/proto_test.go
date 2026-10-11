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
