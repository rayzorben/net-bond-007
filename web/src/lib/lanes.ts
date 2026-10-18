import type { LinkView } from './types'
import type { Lane, LaneRole } from '../components/BondMap.svelte'

// Recent wins/dups per lane, decayed over ~10 s, so the role reflects what a
// link is doing now rather than its lifetime totals.
const recent = new Map<string, { wins: number; dups: number; w: number; d: number; at: number }>()

function recentDupShare(key: string, l: LinkView): { dupShare: number; active: boolean } {
  const now = performance.now()
  const r = recent.get(key)
  if (!r || l.wins < r.wins || l.dups < r.dups) {
    recent.set(key, { wins: l.wins, dups: l.dups, w: 0, d: 0, at: now })
    return { dupShare: 0, active: false }
  }
  const k = Math.exp(-(now - r.at) / 10000)
  r.w = r.w * k + (l.wins - r.wins)
  r.d = r.d * k + (l.dups - r.dups)
  r.wins = l.wins; r.dups = l.dups; r.at = now
  const n = r.w + r.d
  return { dupShare: n > 0 ? r.d / n : 0, active: n > 0.5 }
}

/** Map engine link views onto bond-map lanes. `flip` swaps up/down (server side). */
export function toLanes(links: LinkView[], keyOf: (l: LinkView) => string, opts: { flip?: boolean; directVia?: string } = {}): Lane[] {
  const live = (l: LinkView) => l.state === 'up' || l.state === 'degraded'
  // One winner: the live link that delivered the largest share of packets first.
  let winner: LinkView | undefined
  for (const l of links) if (live(l) && l.winShare > 0.05 && (!winner || l.winShare > winner.winShare)) winner = l
  return links.map((l) => {
    const key = keyOf(l)
    const { dupShare, active } = recentDupShare(key, l)
    let role: LaneRole = ''
    if (live(l)) {
      if (!active && l.winShare < 0.01) role = 'idle'
      else role = dupShare >= 0.5 ? 'redundancy' : 'speed'
    }
    return {
      key, name: l.name, label: l.label, kind: l.kind, state: l.state, backup: l.backup, benched: l.benched,
      rtt: l.rttMs, loss: l.lossPct,
      up: opts.flip ? l.rxBps : l.txBps, down: opts.flip ? l.txBps : l.rxBps,
      winRatio: l.wins + l.dups ? l.wins / (l.wins + l.dups) : 0,
      winShare: l.winShare ?? 0, dupShare, quality: l.quality ?? 0, winner: l === winner, role,
      direct: !!opts.directVia && l.name === opts.directVia,
    }
  })
}
