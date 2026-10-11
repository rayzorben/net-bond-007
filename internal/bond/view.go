package bond

import (
	"time"

	"github.com/rayzorben/bond007/internal/metrics"
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
	}
	for _, l := range e.Links() {
		v.WireTxBytes += l.Tx.Bytes.Load()
		v.WireRxBytes += l.Rx.Bytes.Load()
	}
	return v
}
