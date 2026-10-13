package bond

import (
	"fmt"
	"sort"
	"time"

	"github.com/rayzorben/net-bond-007/internal/metrics"
)

// LinkView is the JSON shape of a link for the dashboard.
type LinkView struct {
	ID        uint8            `json:"id"`
	Name      string           `json:"name"`
	Label     string           `json:"label,omitempty"`
	Kind      string           `json:"kind"`
	State     string           `json:"state"`
	Backup    bool             `json:"backup"`
	Disabled  bool             `json:"disabled,omitempty"`
	Weight    float64          `json:"weight"`
	RTTms     float64          `json:"rttMs"`
	MinRTTms  float64          `json:"minRttMs"`
	JitterMs  float64          `json:"jitterMs"`
	LossPct   float64          `json:"lossPct"`
	Quality   float64          `json:"quality"`
	TxBps     float64          `json:"txBps"`
	RxBps     float64          `json:"rxBps"`
	CapBps    float64          `json:"capBps"`
	PaceBps   float64          `json:"paceBps"`
	ShapeBps  float64          `json:"shapeBps"`
	Benched   bool             `json:"benched"`
	Metered   bool             `json:"metered,omitempty"`
	Share     float64          `json:"meterShare,omitempty"` // fraction it may carry while others work
	Usage     *MeterUsage      `json:"usage,omitempty"`      // client only
	Tested    *TestedSpeed     `json:"tested,omitempty"`     // client only: last speed test
	Relay     *RelayInfo       `json:"relay,omitempty"`      // client only: phone relay link
	Reliab    float64          `json:"reliability"`
	TxBytes   uint64           `json:"txBytes"`
	RxBytes   uint64           `json:"rxBytes"`
	TxPackets uint64           `json:"txPackets"`
	RxPackets uint64           `json:"rxPackets"`
	Wins      uint64           `json:"wins"`
	Dups      uint64           `json:"dups"`
	Drops     uint64           `json:"drops"`
	Errors    uint64           `json:"errors"`
	WinShare  float64          `json:"winShare"`
	Queue     int              `json:"queue"`
	Local     string           `json:"local,omitempty"`
	Remote    string           `json:"remote,omitempty"`
	LastError string           `json:"lastError,omitempty"`
	History   []metrics.Sample `json:"history,omitempty"`
}

// RelayInfo describes the phone behind a relay link, from its last hello.
type RelayInfo struct {
	Phone    string  `json:"phone"` // LAN address
	Model    string  `json:"model,omitempty"`
	Carrier  string  `json:"carrier,omitempty"`
	Version  string  `json:"version,omitempty"`
	Cellular bool    `json:"cellular"`
	Battery  int     `json:"battery"` // percent; -1 unknown
	Charging bool    `json:"charging"`
	TempC    float64 `json:"tempC,omitempty"`
	Thermal  int     `json:"thermal"`
	Signal   int     `json:"signal"` // 0..4 bars; -1 unknown
	SeenMs   int64   `json:"seenMs"` // since the last hello
}

// TestedSpeed is the link's last stand-alone speed test.
type TestedSpeed struct {
	DownBps float64   `json:"downBps"`
	UpBps   float64   `json:"upBps"`
	PingMs  float64   `json:"pingMs"`
	At      time.Time `json:"at"`
}

// MeterUsage is a metered link's billing-cycle usage.
type MeterUsage struct {
	UsedBytes  uint64    `json:"usedBytes"`
	CapBytes   uint64    `json:"capBytes,omitempty"`
	CycleStart time.Time `json:"cycleStart"`
	OverCap    bool      `json:"overCap,omitempty"`
}

// View snapshots a link. historyN limits history points (0 = none, -1 = all).
func (l *Link) View(historyN int) LinkView {
	v := LinkView{
		ID: l.ID, Name: l.Name, Kind: l.Kind,
		State:     l.State().String(),
		Backup:    l.Backup(),
		Weight:    l.Weight(),
		TxBytes:   l.Tx.Bytes.Load(),
		RxBytes:   l.Rx.Bytes.Load(),
		TxPackets: l.Tx.Packets.Load(),
		RxPackets: l.Rx.Packets.Load(),
		Wins:      l.Wins.Load(),
		Dups:      l.Dups.Load(),
		Drops:     l.Drops.Load(),
		Errors:    l.Errors.Load(),
		Queue:     l.QueueLen(),
		LastError: l.LastError(),
		PaceBps:   l.Pacer.Rate(),
		ShapeBps:  l.Shaper.ActiveRate(time.Now()),
		Benched:   l.Benched(),
		Metered:   l.Metered(),
		Share:     l.MeterShare(),
	}
	l.Health(func(h *Health) {
		v.Label = h.Label
		v.RTTms = ms(h.RTT.SRTT)
		v.MinRTTms = ms(h.RTT.Min)
		v.JitterMs = ms(h.RTT.RTTVar)
		v.LossPct = h.Loss * 100
		v.Quality = h.Quality
		v.TxBps = h.TxRate.BPS
		v.RxBps = h.RxRate.BPS
		v.CapBps = h.CapBps
		v.WinShare = h.WinShare
		v.Reliab = h.Reliability
		v.Local = h.LocalAddr
		v.Remote = h.RemoteAddr
		if historyN != 0 {
			n := historyN
			if n < 0 {
				n = 0
			}
			v.History = h.History.Snapshot(n)
		}
	})
	return v
}

// EngineView summarises the engine for the dashboard.
type EngineView struct {
	Session       uint32  `json:"session"`
	TxBps         float64 `json:"txBps"`
	RxBps         float64 `json:"rxBps"`
	UniqueTxBytes uint64  `json:"uniqueTxBytes"`
	UniqueRxBytes uint64  `json:"uniqueRxBytes"`
	WireTxBytes   uint64  `json:"wireTxBytes"`
	WireRxBytes   uint64  `json:"wireRxBytes"`
	NoLinkDrops   uint64  `json:"noLinkDrops"`
	ReorderHoldMs float64 `json:"reorderHoldMs"`
	ReorderHeld   int     `json:"reorderHeld"`
	ReorderInOrd  uint64  `json:"reorderInOrder"`
	ReorderWaited uint64  `json:"reorderWaited"`
	ReorderSkip   uint64  `json:"reorderSkipped"`
	ReorderLate   uint64  `json:"reorderLate"`
	Mode          string  `json:"mode"`
	Redundancy    int     `json:"redundancy"`
	ARQ           bool    `json:"arq"`
	Retransmits   uint64  `json:"retransmits"`
	RtxPremature  uint64  `json:"rtxPremature"`
	Reinjected    uint64  `json:"reinjected"`
	NacksSent     uint64  `json:"nacksSent"`
	NacksRecv     uint64  `json:"nacksRecv"`
	GapsDetected  uint64  `json:"gapsDetected"`
	GapsRecovered uint64  `json:"gapsRecovered"`
	GapsGaveUp    uint64  `json:"gapsGaveUp"`
}

func (e *Engine) View() EngineView {
	st, hold, held := e.ReorderStats()
	p := e.Policy()
	v := EngineView{
		Session:       e.Session,
		TxBps:         float64(e.TxBps.Load()),
		RxBps:         float64(e.RxBps.Load()),
		UniqueTxBytes: e.UniqueTx.Bytes.Load(),
		UniqueRxBytes: e.UniqueRx.Bytes.Load(),
		NoLinkDrops:   e.NoLink.Load(),
		ReorderHoldMs: float64(hold) / float64(time.Millisecond),
		ReorderHeld:   held,
		ReorderInOrd:  st.InOrder,
		ReorderWaited: st.Held,
		ReorderSkip:   st.Skipped,
		ReorderLate:   st.Late,
		Mode:          string(p.Mode),
		Redundancy:    p.Redundancy,
		ARQ:           e.arq.Load(),
		Retransmits:   e.Retransmits.Load(),
		RtxPremature:  e.RtxPremature.Load(),
		Reinjected:    e.Reinjected.Load(),
		NacksSent:     e.NacksSent.Load(),
		NacksRecv:     e.NacksRecv.Load(),
	}
	e.gaps.mu.Lock()
	v.GapsDetected, v.GapsRecovered, v.GapsGaveUp = e.gaps.Detected, e.gaps.Recovered, e.gaps.GaveUp
	e.gaps.mu.Unlock()
	for _, l := range e.Links() {
		v.WireTxBytes += l.Tx.Bytes.Load()
		v.WireRxBytes += l.Rx.Bytes.Load()
	}
	return v
}

// FlowLink is one link's share of a connection's traffic.
type FlowLink struct {
	ID     uint8  `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Copies uint64 `json:"copies"` // packet copies sent on this link
	Wins   uint64 `json:"wins"`   // received packets this link delivered first
}

// FlowView is the JSON shape of a tracked connection.
type FlowView struct {
	Proto   string     `json:"proto"`
	Inside  string     `json:"inside"`
	Outside string     `json:"outside"`
	Name    string     `json:"name,omitempty"`
	TxBps   float64    `json:"txBps"`
	RxBps   float64    `json:"rxBps"`
	TxBytes uint64     `json:"txBytes"`
	RxBytes uint64     `json:"rxBytes"`
	PeakTx  float64    `json:"peakTxBps"`
	PeakRx  float64    `json:"peakRxBps"`
	First   int64      `json:"first"`
	Last    int64      `json:"last"`
	Links   []FlowLink `json:"links"`
	Key     FlowKey    `json:"-"`
}

func protoName(p uint8) string {
	switch p {
	case 6:
		return "tcp"
	case 17:
		return "udp"
	case 1, 58:
		return "icmp"
	}
	return fmt.Sprint(p)
}

// Flows snapshots tracked connections, busiest first.
func (e *Engine) Flows() []FlowView {
	names := map[uint8]*Link{}
	for _, l := range e.Links() {
		names[l.ID] = l
	}
	return e.flowViews(e.flows.snapshot(), names)
}

// SetFlowSink registers fn to receive connections as they end.
func (e *Engine) SetFlowSink(fn func([]FlowView)) {
	e.flows.mu.Lock()
	e.flows.sink = func(fs []Flow) {
		names := map[uint8]*Link{}
		for _, l := range e.Links() {
			names[l.ID] = l
		}
		fn(e.flowViews(fs, names))
	}
	e.flows.mu.Unlock()
}

func (e *Engine) flowViews(fs []Flow, names map[uint8]*Link) []FlowView {
	out := make([]FlowView, 0, len(fs))
	for _, f := range fs {
		// Multicast / link-local chatter (router discovery, mDNS) isn't a
		// connection anyone cares about.
		if o := f.Key.Outside.Addr(); o.IsMulticast() || o.IsLinkLocalUnicast() || o.IsLinkLocalMulticast() {
			continue
		}
		v := FlowView{
			Proto: protoName(f.Key.Proto), Inside: f.Key.Inside.String(), Outside: f.Key.Outside.String(),
			Name: f.Name, TxBps: f.TxBps, RxBps: f.RxBps, TxBytes: f.TxBytes, RxBytes: f.RxBytes,
			PeakTx: f.PeakTx, PeakRx: f.PeakRx,
			First: f.First.UnixMilli(), Last: f.Last.UnixMilli(), Key: f.Key,
		}
		ids := map[uint8]bool{}
		for id := range f.CopiesOn {
			ids[id] = true
		}
		for id := range f.WinsOn {
			ids[id] = true
		}
		for id := range ids {
			fl := FlowLink{ID: id, Copies: f.CopiesOn[id], Wins: f.WinsOn[id], Name: "link " + fmt.Sprint(id), Kind: "other"}
			if l := names[id]; l != nil {
				fl.Name, fl.Kind = l.Name, l.Kind
			}
			v.Links = append(v.Links, fl)
		}
		sort.Slice(v.Links, func(i, j int) bool { return v.Links[i].ID < v.Links[j].ID })
		out = append(out, v)
	}
	return out
}
