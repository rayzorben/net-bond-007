// Package proto defines the Net-Bond-007 wire format spoken between client and
// server over UDP.
//
// Every datagram starts with an 8-byte common header:
//
//	0      1        2..5        6      7
//	+------+--------+-----------+------+-------+
//	| 0xB7 | ver|ty | session   | link | flags |
//	+------+--------+-----------+------+-------+
//
// DATA frames append an 8-byte big-endian sequence number followed by one raw
// IP packet. The sequence number is global per session and direction, so the
// receiver can de-duplicate copies that arrive over several links (the first
// copy wins, every later copy is discarded).
//
// PROBE / PROBE_ACK frames carry a fixed binary body used for health metrics.
// Control frames (HELLO, HELLO_ACK, POLICY, REJECT, BYE) carry JSON bodies:
// they are rare, and JSON keeps them easy to extend.
//
// NOTE: frames are not encrypted or authenticated beyond the HELLO token.
// See docs/SECURITY.md.
package proto

import (
	"encoding/binary"
	"encoding/json"
	"errors"
)

const (
	Magic   byte = 0xB7
	Version byte = 1

	HeaderLen     = 8
	DataHeaderLen = HeaderLen + 8
	ProbeBodyLen  = 4 + 8 + 4 + 4 + 2 + 4 + 2 + 1 + 4 + 4
	ProbeAckLen   = 4 + 8 + 8 + 8
	// probeFECLen is the optional trailing FEC counter on PROBE and
	// PROBE_ACK. Older peers stop reading before it, and frames from them
	// lack it (it reads as 0).
	probeFECLen = 4
	// MaxFrame bounds a datagram: an IP packet up to 64KiB-1 plus headers.
	MaxFrame = 65535 + DataHeaderLen
)

type Type byte

const (
	TypeHello    Type = 1
	TypeHelloAck Type = 2
	TypeData     Type = 3
	TypeProbe    Type = 4
	TypeProbeAck Type = 5
	TypePolicy   Type = 6
	TypeBye      Type = 7
	TypeReject   Type = 8
	// TypeNack asks the peer to retransmit missing DATA sequence numbers.
	TypeNack Type = 9
	// Relay control frames travel only between a phone running the relay
	// app and the client on the same LAN; they never reach the server.
	TypeRelayHello Type = 10 // phone -> client, every 2 s
	TypeRelayAck   Type = 11 // client -> phone
)

func (t Type) String() string {
	switch t {
	case TypeHello:
		return "HELLO"
	case TypeHelloAck:
		return "HELLO_ACK"
	case TypeData:
		return "DATA"
	case TypeProbe:
		return "PROBE"
	case TypeProbeAck:
		return "PROBE_ACK"
	case TypePolicy:
		return "POLICY"
	case TypeBye:
		return "BYE"
	case TypeReject:
		return "REJECT"
	case TypeNack:
		return "NACK"
	case TypeRelayHello:
		return "RELAY_HELLO"
	case TypeRelayAck:
		return "RELAY_ACK"
	case TypeFEC:
		return "FEC"
	case TypeQuiet:
		return "QUIET"
	}
	return "UNKNOWN"
}

var (
	ErrShort   = errors.New("proto: frame too short")
	ErrMagic   = errors.New("proto: bad magic")
	ErrVersion = errors.New("proto: unsupported version")
)

// Header is the common prefix of every frame.
type Header struct {
	Type    Type
	Session uint32
	Link    uint8
	Flags   uint8
}

// PutHeader writes h into b[:HeaderLen].
func PutHeader(b []byte, h Header) {
	b[0] = Magic
	b[1] = Version<<4 | byte(h.Type)&0x0f
	binary.BigEndian.PutUint32(b[2:6], h.Session)
	b[6] = h.Link
	b[7] = h.Flags
}

// ParseHeader validates and decodes the common header.
func ParseHeader(b []byte) (Header, error) {
	if len(b) < HeaderLen {
		return Header{}, ErrShort
	}
	if b[0] != Magic {
		return Header{}, ErrMagic
	}
	if b[1]>>4 != Version {
		return Header{}, ErrVersion
	}
	return Header{
		Type:    Type(b[1] & 0x0f),
		Session: binary.BigEndian.Uint32(b[2:6]),
		Link:    b[6],
		Flags:   b[7],
	}, nil
}

// AppendData builds a DATA frame into dst (which is reset) and returns it.
func AppendData(dst []byte, session uint32, link uint8, seq uint64, payload []byte) []byte {
	n := DataHeaderLen + len(payload)
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	PutHeader(dst, Header{Type: TypeData, Session: session, Link: link})
	binary.BigEndian.PutUint64(dst[HeaderLen:DataHeaderLen], seq)
	copy(dst[DataHeaderLen:], payload)
	return dst
}

// SetLink rewrites the link byte of an already-built frame in place.
func SetLink(frame []byte, link uint8) { frame[6] = link }

// ParseData returns the sequence number and payload of a DATA frame.
func ParseData(b []byte) (seq uint64, payload []byte, err error) {
	if len(b) < DataHeaderLen {
		return 0, nil, ErrShort
	}
	return binary.BigEndian.Uint64(b[HeaderLen:DataHeaderLen]), b[DataHeaderLen:], nil
}

// Probe is sent by the client on every link several times per second. Besides
// measuring RTT it reports the client's view of the link so the server can
// schedule downstream traffic with the same knowledge.
type Probe struct {
	Seq         uint32
	SentAt      int64 // sender clock, nanoseconds
	RTTMicros   uint32
	JitterMicro uint32
	LossPermil  uint16
	CapKbps     uint32 // estimated capacity (downstream) in kbit/s
	Weight      uint16 // downstream scheduling weight x10 (0 = server decides)
	State       uint8  // link state in the low bits, ProbeBackupBit if backup
	LastRTTMicr uint32 // most recent raw RTT sample (fast congestion signal)
	RxKbps      uint32 // rate the client received on this link in the last interval
	// FECRecovered counts packets the client rebuilt from FEC on this link
	// (cumulative), so the server can size its FEC by the real loss rate.
	FECRecovered uint32
}

const (
	ProbeStateMask = 0x7f
	ProbeBackupBit = 0x80
)

func AppendProbe(dst []byte, session uint32, link uint8, p Probe) []byte {
	dst = append(dst[:0], make([]byte, HeaderLen+ProbeBodyLen+probeFECLen)...)
	PutHeader(dst, Header{Type: TypeProbe, Session: session, Link: link})
	b := dst[HeaderLen:]
	binary.BigEndian.PutUint32(b[0:], p.Seq)
	binary.BigEndian.PutUint64(b[4:], uint64(p.SentAt))
	binary.BigEndian.PutUint32(b[12:], p.RTTMicros)
	binary.BigEndian.PutUint32(b[16:], p.JitterMicro)
	binary.BigEndian.PutUint16(b[20:], p.LossPermil)
	binary.BigEndian.PutUint32(b[22:], p.CapKbps)
	binary.BigEndian.PutUint16(b[26:], p.Weight)
	b[28] = p.State
	binary.BigEndian.PutUint32(b[29:], p.LastRTTMicr)
	binary.BigEndian.PutUint32(b[33:], p.RxKbps)
	binary.BigEndian.PutUint32(b[37:], p.FECRecovered)
	return dst
}

func ParseProbe(b []byte) (Probe, error) {
	if len(b) < HeaderLen+ProbeBodyLen {
		return Probe{}, ErrShort
	}
	b = b[HeaderLen:]
	var fec uint32
	if len(b) >= ProbeBodyLen+probeFECLen {
		fec = binary.BigEndian.Uint32(b[ProbeBodyLen:])
	}
	return Probe{
		FECRecovered: fec,
		Seq:          binary.BigEndian.Uint32(b[0:]),
		SentAt:       int64(binary.BigEndian.Uint64(b[4:])),
		RTTMicros:    binary.BigEndian.Uint32(b[12:]),
		JitterMicro:  binary.BigEndian.Uint32(b[16:]),
		LossPermil:   binary.BigEndian.Uint16(b[20:]),
		CapKbps:      binary.BigEndian.Uint32(b[22:]),
		Weight:       binary.BigEndian.Uint16(b[26:]),
		State:        b[28],
		LastRTTMicr:  binary.BigEndian.Uint32(b[29:]),
		RxKbps:       binary.BigEndian.Uint32(b[33:]),
	}, nil
}

// ProbeAck echoes a probe and reports what the server has received on the
// link so the client can compute upstream loss and goodput.
type ProbeAck struct {
	Seq       uint32
	SentAt    int64
	RxPackets uint64
	RxBytes   uint64
	// FECRecovered counts packets the server rebuilt from FEC on this link.
	FECRecovered uint32
}

func AppendProbeAck(dst []byte, session uint32, link uint8, a ProbeAck) []byte {
	dst = append(dst[:0], make([]byte, HeaderLen+ProbeAckLen+probeFECLen)...)
	PutHeader(dst, Header{Type: TypeProbeAck, Session: session, Link: link})
	b := dst[HeaderLen:]
	binary.BigEndian.PutUint32(b[0:], a.Seq)
	binary.BigEndian.PutUint64(b[4:], uint64(a.SentAt))
	binary.BigEndian.PutUint64(b[12:], a.RxPackets)
	binary.BigEndian.PutUint64(b[20:], a.RxBytes)
	binary.BigEndian.PutUint32(b[28:], a.FECRecovered)
	return dst
}

func ParseProbeAck(b []byte) (ProbeAck, error) {
	if len(b) < HeaderLen+ProbeAckLen {
		return ProbeAck{}, ErrShort
	}
	b = b[HeaderLen:]
	var fec uint32
	if len(b) >= ProbeAckLen+probeFECLen {
		fec = binary.BigEndian.Uint32(b[ProbeAckLen:])
	}
	return ProbeAck{
		FECRecovered: fec,
		Seq:          binary.BigEndian.Uint32(b[0:]),
		SentAt:       int64(binary.BigEndian.Uint64(b[4:])),
		RxPackets:    binary.BigEndian.Uint64(b[12:]),
		RxBytes:      binary.BigEndian.Uint64(b[20:]),
	}, nil
}

// Hello registers a link with a session. The first HELLO for an unknown
// session id creates the session.
type Hello struct {
	Token      string `json:"token"`
	ClientName string `json:"client"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	LinkName   string `json:"link"`
	LinkKind   string `json:"kind"`
	// FEC: this client understands FEC frames (sent downstream when its
	// policy asks for them).
	FEC bool `json:"fec,omitempty"`
}

// HelloAck assigns the client its tunnel address.
type HelloAck struct {
	ClientIP   string `json:"ip"`
	PrefixLen  int    `json:"prefix"`
	GatewayIP  string `json:"gateway"`
	DNS        string `json:"dns"`
	MTU        int    `json:"mtu"`
	ServerName string `json:"server"`
	Version    string `json:"version"`
	// Epoch identifies the server-side session instance. If it changes (the
	// server restarted and rebuilt the session) the client resets its
	// receive window because the server's sequence numbers restart at 1.
	Epoch int64 `json:"epoch"`
	// FEC: the server understands FEC frames, so the client may send them.
	FEC bool `json:"fec,omitempty"`
}

// Policy tells the server how to schedule downstream traffic for a session.
type Policy struct {
	Mode                   string `json:"mode"`
	Strategy               string `json:"strategy"`
	Redundancy             int    `json:"redundancy"`
	SmallPacketMirrorBytes int    `json:"smallMirror"`
	RealtimeMirror         bool   `json:"realtimeMirror"`
	RealtimePorts          string `json:"realtimePorts"`
	ReorderMode            string `json:"reorderMode"`
	ReorderMs              int    `json:"reorderMs"`
	QueueDepth             int    `json:"queueDepth"`
	Pacing                 bool   `json:"pacing"`
	PacingStartMbps        int    `json:"pacingStartMbps"`
	PacingDelayMs          int    `json:"pacingDelayMs"`
	PacingLossPct          int    `json:"pacingLossPct"`
	Retransmit             bool   `json:"retransmit"`
	Shaping                bool   `json:"shaping"`
	// FEC asks the server to protect downstream links with FEC frames
	// (sized automatically from measured loss).
	FEC bool `json:"fec,omitempty"`
	// Feature switches for loss handling (absent from older clients: off).
	RepairPriority  bool `json:"repairPriority,omitempty"`
	StormRepair     bool `json:"stormRepair,omitempty"`
	OutageReports   bool `json:"outageReports,omitempty"`
	LossTolerantTCP bool `json:"lossTolerantTCP,omitempty"`
	// Automatic scaling decided by the client: copies per packet and which
	// links to stop using. Zero/empty = not active.
	AutoCopies int     `json:"autoCopies,omitempty"`
	Benched    []uint8 `json:"benched,omitempty"`
	// What automatic scaling is doing, for the server's history log.
	AutoMode         string            `json:"autoMode,omitempty"`
	AutoDirectVia    string            `json:"autoDirectVia,omitempty"`
	LatencyWeighting bool              `json:"latencyWeighting"`
	LinkMaxMbps      map[uint8]float64 `json:"linkMaxMbps,omitempty"`
	// Metered links (phone hotspots): the share of traffic each may carry
	// while other links work (0 = failover only). Absent = not metered.
	MeteredShare map[uint8]float64 `json:"meteredShare,omitempty"`
}

// RelayHello announces a phone that shares its cellular data. The phone
// copies every other frame between the client (Wi-Fi) and the server
// (cellular) without looking inside it.
type RelayHello struct {
	Token     string  `json:"token"`
	Name      string  `json:"name"`
	Version   string  `json:"version"`
	Model     string  `json:"model"`
	Cellular  bool    `json:"cellular"` // the phone has mobile data right now
	Battery   int     `json:"battery"`  // percent; -1 unknown
	Charging  bool    `json:"charging"`
	TempC     float64 `json:"tempC"`
	Thermal   int     `json:"thermal"` // Android thermal status 0 (none) .. 6 (shutdown)
	Signal    int     `json:"signal"`  // 0..4 bars; -1 unknown
	Carrier   string  `json:"carrier"`
	UpBytes   uint64  `json:"upBytes"`
	DownBytes uint64  `json:"downBytes"`
	// Awake: the relay app is on screen with the screen on (keep-awake mode),
	// so Android keeps the phone's Wi-Fi out of power save. Off screen, the
	// phone holds traffic for up to ~2 s at a time. Nil: an older app.
	Awake *bool `json:"awake,omitempty"`
	// BatteryExempt: Android battery optimisation is off for the app.
	BatteryExempt *bool `json:"batteryExempt,omitempty"`
}

// RelayAck accepts or refuses a relay and tells it where the server is.
type RelayAck struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	Server string `json:"server,omitempty"` // host:port, resolved by the phone over cellular
}

type Reject struct {
	Reason string `json:"reason"`
}

// AppendJSON builds a control frame with a JSON body.
func AppendJSON(dst []byte, t Type, session uint32, link uint8, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dst = append(dst[:0], make([]byte, HeaderLen)...)
	PutHeader(dst, Header{Type: t, Session: session, Link: link})
	return append(dst, body...), nil
}

// ParseJSON decodes the JSON body of a control frame.
func ParseJSON(b []byte, v any) error {
	if len(b) < HeaderLen {
		return ErrShort
	}
	return json.Unmarshal(b[HeaderLen:], v)
}

// SeqRange is a run of missing sequence numbers [Start, Start+Count).
type SeqRange struct {
	Start uint64
	Count uint16
}

// MaxNackRanges bounds one NACK frame (10 bytes per range).
const MaxNackRanges = 100

// AppendNack builds a NACK frame listing missing sequence ranges.
func AppendNack(dst []byte, session uint32, link uint8, ranges []SeqRange) []byte {
	if len(ranges) > MaxNackRanges {
		ranges = ranges[:MaxNackRanges]
	}
	dst = append(dst[:0], make([]byte, HeaderLen+2+10*len(ranges))...)
	PutHeader(dst, Header{Type: TypeNack, Session: session, Link: link})
	b := dst[HeaderLen:]
	binary.BigEndian.PutUint16(b, uint16(len(ranges)))
	for i, r := range ranges {
		o := 2 + 10*i
		binary.BigEndian.PutUint64(b[o:], r.Start)
		binary.BigEndian.PutUint16(b[o+8:], r.Count)
	}
	return dst
}

// ParseNack decodes the ranges of a NACK frame.
func ParseNack(f []byte) ([]SeqRange, error) {
	if len(f) < HeaderLen+2 {
		return nil, ErrShort
	}
	b := f[HeaderLen:]
	n := int(binary.BigEndian.Uint16(b))
	if n > MaxNackRanges || len(b) < 2+10*n {
		return nil, ErrShort
	}
	out := make([]SeqRange, n)
	for i := range out {
		o := 2 + 10*i
		out[i] = SeqRange{Start: binary.BigEndian.Uint64(b[o:]), Count: binary.BigEndian.Uint16(b[o+8:])}
	}
	return out, nil
}

// FEC (forward error correction) frames carry the XOR of a group of DATA
// payloads that were sent on one link, so the receiver can rebuild any single
// missing packet of the group without waiting a round trip for a NACK repair.
//
// Body: count n (1 byte) | XOR of the payload lengths (2) | first seq (8) |
// (n-1) x uint16 offsets of the other seqs from the first | parity bytes
// (the XOR of the payloads, each zero-padded to the longest).
const TypeFEC Type = 12

// MaxFECGroup bounds the packets one FEC frame protects.
const MaxFECGroup = 64

// FEC is a decoded FEC frame. Parity aliases the frame buffer.
type FEC struct {
	Seqs   []uint64
	LenXor uint16
	Parity []byte
}

// AppendFEC builds a FEC frame into dst (which is reset). seqs must be
// ascending, at most MaxFECGroup long, and within 65535 of seqs[0].
func AppendFEC(dst []byte, session uint32, link uint8, seqs []uint64, lenXor uint16, parity []byte) []byte {
	n := HeaderLen + 1 + 2 + 8 + 2*(len(seqs)-1) + len(parity)
	if cap(dst) < n {
		dst = make([]byte, n)
	}
	dst = dst[:n]
	PutHeader(dst, Header{Type: TypeFEC, Session: session, Link: link})
	b := dst[HeaderLen:]
	b[0] = byte(len(seqs))
	binary.BigEndian.PutUint16(b[1:], lenXor)
	binary.BigEndian.PutUint64(b[3:], seqs[0])
	o := 11
	for _, s := range seqs[1:] {
		binary.BigEndian.PutUint16(b[o:], uint16(s-seqs[0]))
		o += 2
	}
	copy(b[o:], parity)
	return dst
}

// ParseFEC decodes a FEC frame. seqs is reused for the result if large enough.
func ParseFEC(f []byte, seqs []uint64) (FEC, error) {
	if len(f) < HeaderLen+11 {
		return FEC{}, ErrShort
	}
	b := f[HeaderLen:]
	n := int(b[0])
	if n < 2 || n > MaxFECGroup || len(b) < 11+2*(n-1)+1 {
		return FEC{}, ErrShort
	}
	base := binary.BigEndian.Uint64(b[3:])
	seqs = append(seqs[:0], base)
	o := 11
	for i := 1; i < n; i++ {
		seqs = append(seqs, base+uint64(binary.BigEndian.Uint16(b[o:])))
		o += 2
	}
	return FEC{Seqs: seqs, LenXor: binary.BigEndian.Uint16(b[1:]), Parity: b[o:]}, nil
}

// TypeQuiet tells the peer that one of its links went silent at the
// receiver while that link had been busy: the sender should resend at once,
// on its other links, what it sent only on the silent one. Without it the
// receiver can't NACK the lost packets until traffic resumes (a Starlink
// outage is 20-200 ms), and the sender's own silence detection waits for the
// link's upstream to go quiet, which a download barely uses.
//
// Body: silent link id (1) | silent for, ms (2).
const TypeQuiet Type = 13

func AppendQuiet(dst []byte, session uint32, via, silent uint8, silentFor uint16) []byte {
	dst = append(dst[:0], make([]byte, HeaderLen+3)...)
	PutHeader(dst, Header{Type: TypeQuiet, Session: session, Link: via})
	dst[HeaderLen] = silent
	binary.BigEndian.PutUint16(dst[HeaderLen+1:], silentFor)
	return dst
}

func ParseQuiet(f []byte) (silent uint8, silentForMs uint16, err error) {
	if len(f) < HeaderLen+3 {
		return 0, 0, ErrShort
	}
	return f[HeaderLen], binary.BigEndian.Uint16(f[HeaderLen+1:]), nil
}
