<script lang="ts">
  import { onMount } from 'svelte'
  import { Search, Laptop, Router, MonitorSmartphone, Server, Pause, Play } from '@lucide/svelte'
  import Segmented from '../components/Segmented.svelte'
  import { store, api } from '../lib/store.svelte'
  import { bps, bytes, kindColor } from '../lib/format'
  import ConnTable, { type Conn } from '../components/ConnTable.svelte'
  import LinkShareBar from '../components/LinkShareBar.svelte'

  interface Group {
    key: string; title: string; sub: string; self?: boolean
    rxBps: number; txBps: number; rxBytes: number; txBytes: number
    links: Conn['links']; conns: Conn[]
  }
  let groups = $state<Group[]>([])
  let q = $state('')
  type SortMode = 'stable' | 'busiest' | 'name' | 'total'
  let sortMode = $state<SortMode>(loadSort())
  let paused = $state(false)
  function loadSort(): SortMode {
    try { const v = localStorage.getItem('b7-conn-sort'); if (v === 'busiest' || v === 'name' || v === 'total' || v === 'stable') return v } catch {}
    return 'stable'
  }
  function setSort(v: string) {
    sortMode = v as SortMode
    try { localStorage.setItem('b7-conn-sort', v) } catch {}
  }
  let err = $state('')
  const role = $derived(store.info?.role)

  async function load() {
    if (paused) return
    try {
      const r = await api('GET', '/api/x/connections')
      if (role === 'server') {
        groups = (r.sessions ?? []).map((s: any): Group => {
          const conns: Conn[] = s.connections ?? []
          return {
            key: s.id, title: s.name, sub: `client ${s.ip} · session ${s.id}`,
            rxBps: sum(conns, 'txBps'), txBps: sum(conns, 'rxBps'), // server view: what it sends is the client's download
            rxBytes: sum(conns, 'txBytes'), txBytes: sum(conns, 'rxBytes'),
            links: mergeLinks(conns), conns: conns.map(flipForServer),
          }
        })
      } else {
        groups = (r.devices ?? []).map((d: any): Group => ({
          key: d.self ? 'self' : d.ip, self: d.self,
          title: d.self ? `${d.name} (this device)` : d.name,
          sub: [d.ip, d.mac].filter(Boolean).join(' · '),
          rxBps: d.rxBps, txBps: d.txBps, rxBytes: d.rxBytes, txBytes: d.txBytes,
          links: d.links, conns: d.connections ?? [],
        }))
      }
      err = ''
    } catch (e: any) {
      err = e.message
    }
  }
  // On the server, a flow's "tx" is toward the client (download).
  function flipForServer(c: Conn): Conn {
    return { ...c, rxBps: c.txBps, txBps: c.rxBps, rxBytes: c.txBytes, txBytes: c.rxBytes, links: (c.links ?? []).map((l) => ({ ...l, wins: l.copies })) }
  }
  function sum(cs: any[], k: string) { return cs.reduce((a, c) => a + (c[k] ?? 0), 0) }
  function mergeLinks(cs: Conn[]) {
    const m = new Map<number, any>()
    for (const c of cs) for (const l of c.links ?? []) {
      const x = m.get(l.id) ?? { ...l, copies: 0, wins: 0 }
      x.copies += l.copies; x.wins += role === 'server' ? l.copies : l.wins
      m.set(l.id, x)
    }
    return [...m.values()]
  }
  onMount(() => { load(); const t = setInterval(load, 2000); return () => clearInterval(t) })

  // Stable mode never reorders existing rows: devices alphabetically (this
  // device first), connections in the order they opened (new ones at the end).
  const connKey = (c: Conn) => c.proto + c.inside + c.outside
  const label = (c: Conn) => (c.host || c.outside).toLowerCase()
  function sortConns(cs: Conn[]): Conn[] {
    const a = [...cs]
    switch (sortMode) {
      case 'busiest': return a.sort((x, y) => y.rxBps + y.txBps - (x.rxBps + x.txBps) || connKey(x).localeCompare(connKey(y)))
      case 'name': return a.sort((x, y) => label(x).localeCompare(label(y)) || connKey(x).localeCompare(connKey(y)))
      case 'total': return a.sort((x, y) => y.rxBytes + y.txBytes - (x.rxBytes + x.txBytes) || connKey(x).localeCompare(connKey(y)))
      default: return a.sort((x, y) => x.first - y.first || connKey(x).localeCompare(connKey(y)))
    }
  }
  function sortGroups(gs: Group[]): Group[] {
    const a = [...gs]
    if (sortMode === 'busiest') return a.sort((x, y) => y.rxBps + y.txBps - (x.rxBps + x.txBps) || x.title.localeCompare(y.title))
    if (sortMode === 'total') return a.sort((x, y) => y.rxBytes + y.txBytes - (x.rxBytes + x.txBytes) || x.title.localeCompare(y.title))
    return a.sort((x, y) => Number(!!y.self) - Number(!!x.self) || x.title.localeCompare(y.title))
  }

  const match = (c: Conn) => !q || `${c.host ?? ''} ${c.outside} ${c.local ?? ''} ${c.proto}`.toLowerCase().includes(q.toLowerCase())
  const view = $derived(sortGroups(groups).map((g) => ({ ...g, conns: sortConns(g.conns.filter(match)) })).filter((g) => !q || g.conns.length || g.title.toLowerCase().includes(q.toLowerCase())))
  const totals = $derived({ devices: groups.length, conns: groups.reduce((a, g) => a + g.conns.length, 0), rx: groups.reduce((a, g) => a + g.rxBps, 0), tx: groups.reduce((a, g) => a + g.txBps, 0) })
  const linkLegend = $derived.by(() => {
    const m = new Map<number, { name: string; kind: string }>()
    for (const g of groups) for (const l of g.links ?? []) m.set(l.id, { name: l.name, kind: l.kind })
    return [...m.entries()].map(([id, v]) => ({ id, ...v }))
  })
</script>

<div class="bar">
  <div class="search tile"><Search size={15} /><input placeholder="Filter by host, address, port…" bind:value={q} /></div>
  <span class="chip">{totals.devices} {role === 'server' ? 'client' : 'device'}{totals.devices === 1 ? '' : 's'}</span>
  <span class="chip">{totals.conns} connections</span>
  <span class="chip info">↓ {bps(totals.rx)}</span>
  <span class="chip accent">↑ {bps(totals.tx)}</span>
  <div class="spacer"></div>
  <Segmented size="sm" value={sortMode} onchange={setSort}
    options={[
      { value: 'stable', label: 'Fixed', hint: 'Fixed order (default): devices A-Z, connections in the order they opened; nothing moves' },
      { value: 'busiest', label: 'Busiest', hint: 'Fastest right now first (re-sorts live)' },
      { value: 'name', label: 'Name', hint: 'Destination A-Z' },
      { value: 'total', label: 'Total', hint: 'Most data transferred first' },
    ]} />
  <button class="btn sm" class:paused onclick={() => (paused = !paused)} title={paused ? 'Resume live updates' : 'Freeze the view'}>
    {#if paused}<Play size={14} />Resume{:else}<Pause size={14} />Pause{/if}
  </button>
  <div class="legend">
    <span class="muted">Delivered by:</span>
    {#each linkLegend as l (l.id)}<span><i style="background:{kindColor(l.kind)}"></i>{l.name}</span>{/each}
  </div>
</div>
{#if err}<div class="err">{err}</div>{/if}

{#each view as g, i (g.key)}
  <section class="card dev" style="animation-delay:{i * 40}ms">
    <header>
      <div class="ic">
        {#if role === 'server'}<Server size={20} />{:else if g.self}<Router size={20} />{:else if /phone|ipad|android|iphone|pixel|galaxy/i.test(g.title)}<MonitorSmartphone size={20} />{:else}<Laptop size={20} />{/if}
      </div>
      <div class="ttl"><b>{g.title}</b><span>{g.sub}</span></div>
      <div class="kpi"><span>Down</span><b class="num">{bps(g.rxBps)}</b></div>
      <div class="kpi"><span>Up</span><b class="num">{bps(g.txBps)}</b></div>
      <div class="kpi"><span>Total</span><b class="num">{bytes(g.rxBytes + g.txBytes)}</b></div>
      <div class="kpi"><span>Links</span><LinkShareBar links={g.links} by="wins" width={110} /></div>
    </header>
    <ConnTable conns={g.conns} />
  </section>
{:else}
  <section class="card empty">
    {#if store.state?.role === 'client' && !store.state.server.connected}
      Not connected to the server, so nothing is going through the bond.
    {:else}
      No traffic is going through the bond right now. If "Send all traffic" is off, only the bonded networks you listed are tracked here.
    {/if}
  </section>
{/each}

<style>
  .bar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .search { display: flex; align-items: center; gap: 8px; padding: 0 10px; height: 34px; width: min(320px, 100%); color: var(--muted); background: var(--bg); }
  .search input { border: 0; outline: 0; background: transparent; flex: 1; }
  .spacer { flex: 1; }
  .btn.paused { border-color: var(--warn); color: var(--warn); }
  .legend { display: flex; gap: 12px; font-size: 12px; align-items: center; flex-wrap: wrap; }
  .legend span { display: inline-flex; align-items: center; gap: 6px; }
  .legend i { width: 9px; height: 9px; border-radius: 3px; display: inline-block; }
  .err { color: var(--bad); background: var(--bad-bg); padding: 10px 14px; border-radius: 10px; font-size: 13px; }
  .dev { padding: 14px 16px; display: grid; gap: 10px; }
  header { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; }
  .ic { width: 40px; height: 40px; border-radius: 12px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); flex: none; }
  .ttl { display: grid; flex: 1; min-width: 160px; }
  .ttl b { font-size: 15px; font-weight: 650; }
  .ttl span { font-size: 11.5px; color: var(--muted); font-family: var(--mono); }
  .kpi { display: grid; gap: 2px; }
  .kpi span { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); font-weight: 650; }
  .kpi b { font-size: 16px; font-weight: 700; }
  .empty { padding: 28px; text-align: center; color: var(--muted); }
</style>
