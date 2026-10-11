<script lang="ts">
  import { TriangleAlert, Plus, Ban, Power } from '@lucide/svelte'
  import type { ClientState } from '../lib/types'
  import { store, key } from '../lib/store.svelte'
  import { bps, bpsParts, ms, kindColor, kindLabel, displayName, cssColor } from '../lib/format'
  import BondMap, { type Lane } from '../components/BondMap.svelte'
  import StatCard from '../components/StatCard.svelte'
  import Sparkline from '../components/Sparkline.svelte'
  import TimeChart from '../components/TimeChart.svelte'
  import Donut from '../components/Donut.svelte'
  import LinkCard from '../components/LinkCard.svelte'
  import LinkDrawer from '../components/LinkDrawer.svelte'
  import Segmented from '../components/Segmented.svelte'
  import Toggle from '../components/Toggle.svelte'
  import KindIcon from '../components/KindIcon.svelte'
  import Onboarding from '../components/Onboarding.svelte'

  let { st }: { st: ClientState } = $props()

  const lanes: Lane[] = $derived(st.links.map((l) => ({
    key: key(l), name: l.name, label: l.label, kind: l.kind, state: l.state, backup: l.backup,
    rtt: l.rttMs, loss: l.lossPct, up: l.txBps, down: l.rxBps,
    winRatio: l.wins + l.dups ? l.wins / (l.wins + l.dups) : 0,
  })))
  const live = $derived(st.links.filter((l) => l.state === 'up' || l.state === 'degraded'))
  const best = $derived(live.length ? Math.min(...live.map((l) => l.rttMs).filter((x) => x > 0)) : 0)
  const worst = $derived(live.length ? Math.max(...live.map((l) => l.rttMs)) : 0)
  const redundancy = $derived(st.engine.uniqueRxBytes ? st.engine.wireRxBytes / st.engine.uniqueRxBytes : 0)
  const totals = $derived(store.totals)
  const selected = $derived(st.links.find((l) => key(l) === store.selected))
  const others = $derived(st.interfaces.filter((i) => i.status !== 'bonded' && i.status !== 'tunnel'))
  const [dlv, dlu] = $derived(bpsParts(st.engine.rxBps))
  const [ulv, ulu] = $derived(bpsParts(st.engine.txBps))
  const slices = $derived(st.links.filter((l) => l.wins > 0).map((l) => ({ label: displayName(l), value: l.wins, color: cssColor(kindColor(l.kind)) })))
  const topWinner = $derived([...st.links].sort((a, b) => b.wins - a.wins)[0])
  const modeHelp: Record<string, string> = $derived({
    smart: st.links.length <= 2 ? 'Mirroring every packet on both links' : `Each packet on ${st.engine.redundancy} of ${live.length} links`,
    mirror: 'Every packet on every link: maximum reliability',
    speed: 'No duplicates: maximum combined bandwidth',
    custom: `Each packet on ${st.engine.redundancy} links`,
  })

  async function setBonding(patch: (c: any) => void, msg: string) {
    const cfg = $state.snapshot(store.config)
    patch(cfg)
    const err = await store.saveConfig(cfg)
    store.toast(err ?? msg, err ? 'err' : 'ok')
  }
  async function bondIface(name: string) {
    const cur = store.config?.links?.[name] ?? {}
    const err = await store.saveLink(name, { ...cur, disabled: false })
    store.toast(err ?? `${name} joins the bond`, err ? 'err' : 'ok')
  }
  const statusWord: Record<string, string> = {
    available: 'Available', disabled: 'Turned off', excluded: 'Ignored', 'no-ipv4': 'No IPv4 address', down: 'Disconnected',
  }
</script>

{#if !st.server.address}
  <Onboarding />
{:else}
  {#each [...st.warnings, ...(st.tunnel.error ? [`Virtual adapter: ${st.tunnel.error}`] : []), ...(st.server.error ? [st.server.error] : [])] as w}
    <div class="banner"><TriangleAlert size={16} />{w}</div>
  {/each}

  <section class="hero glass">
    <div class="hh">
      <div>
        <h2>Bond map</h2>
        <p class="muted">{modeHelp[store.config?.bonding?.mode ?? 'smart']}{st.engine.reorderHoldMs ? ` · reorder hold ${ms(st.engine.reorderHoldMs)}` : ''}</p>
      </div>
      <div class="ctl">
        <Segmented value={store.config?.bonding?.mode ?? 'smart'}
          options={[
            { value: 'smart', label: 'Smart', hint: 'Mirror on 1-2 links, stripe with redundancy on 3+' },
            { value: 'mirror', label: 'Mirror', hint: 'Every packet on every link' },
            { value: 'speed', label: 'Speed', hint: 'No duplication' },
            { value: 'custom', label: 'Custom', hint: 'Fixed number of copies' },
          ]}
          onchange={(v) => setBonding((c) => (c.bonding.mode = v), `Bonding mode: ${v} · applied live`)} />
        <label class="pw" title="Turn bonding on or off">
          <Power size={15} />
          <Toggle checked={st.server.enabled} label="Bonding on"
            onchange={(v) => setBonding((c) => (c.server.enabled = v), v ? 'Bonding on' : 'Bonding off: traffic uses your normal connection')} />
        </label>
      </div>
    </div>
    <BondMap {lanes} leftTitle={st.name} leftSub={st.tunnel.ip ?? 'no tunnel yet'}
      rightTitle={st.server.name || st.server.address} rightSub={st.server.connected ? st.server.resolved ?? '' : 'connecting…'}
      egress={st.engine.rxBps + st.engine.txBps} winSide="left" height={Math.max(300, Math.min(460, 140 + lanes.length * 78))}
      onselect={(k) => (store.selected = k)} />
  </section>

  <section class="stats">
    <StatCard title="Download" value={dlv} unit={dlu} hero sub="combined, after de-duplication">
      <Sparkline values={totals.slice(-90).map((p) => p.rx)} color="var(--cyan)" height={34} />
    </StatCard>
    <StatCard title="Upload" value={ulv} unit={ulu} sub="combined">
      <Sparkline values={totals.slice(-90).map((p) => p.tx)} color="var(--accent-2)" height={34} />
    </StatCard>
    <StatCard title="Best path latency" value={best ? best.toFixed(best >= 100 ? 0 : 1) : '—'} unit="ms"
      sub={live.length > 1 ? `spread to ${ms(worst)}; you always get the fastest copy` : 'single path'} />
    <StatCard title="Redundancy" value={redundancy ? redundancy.toFixed(2) : '—'} unit="×"
      sub={`${st.engine.reorderSkipped} packets lost on every path · ${st.engine.reorderWaited} reordered`} />
  </section>

  <section class="row2">
    <div class="card chart">
      <div class="ch"><h3 class="section-title">Bonded throughput</h3><span class="muted">last {Math.round(totals.length / 60) || 1} min</span></div>
      <TimeChart times={totals.map((p) => p.t)} format={(v) => bps(v, 0)} floor={1e6} height={190}
        series={[{ label: 'Download', color: 'var(--cyan)', values: totals.map((p) => p.rx) }, { label: 'Upload', color: 'var(--accent-2)', values: totals.map((p) => p.tx) }]} />
    </div>
    <div class="card race">
      <h3 class="section-title">Who wins the race</h3>
      <div class="rb">
        <Donut {slices} size={132} center={topWinner && topWinner.wins ? displayName(topWinner) : '—'} sub="fastest" />
        <ul>
          {#each st.links as l (key(l))}
            {@const tot = st.links.reduce((a, x) => a + x.wins, 0) || 1}
            <li><i style="background:{kindColor(l.kind)}"></i><span>{displayName(l)}</span><b class="num">{((l.wins / tot) * 100).toFixed(0)}%</b></li>
          {/each}
        </ul>
      </div>
      <p class="muted small">Share of packets whose first copy arrived on each link. Later copies are discarded.</p>
    </div>
  </section>

  <div class="sh"><h3 class="section-title">Links</h3><span class="muted">{live.length} of {st.links.length} carrying traffic</span></div>
  <section class="links">
    {#each st.links as l, i (key(l))}
      <LinkCard link={l} delay={i * 40} onopen={() => (store.selected = key(l))} />
    {/each}
  </section>

  {#if others.length}
    <div class="sh"><h3 class="section-title">Other interfaces</h3><span class="muted">detected on this device</span></div>
    <section class="others">
      {#each others as it (it.name)}
        <div class="oi tile" style="--kc:{kindColor(it.kind)}">
          <span class="ic"><KindIcon kind={it.kind} size={16} /></span>
          <div class="ot"><b>{it.label || it.name}</b><span>{kindLabel[it.kind] ?? it.kind}{it.ipv4?.length ? ` · ${it.ipv4[0]}` : ''} · {statusWord[it.status] ?? it.status}</span></div>
          {#if it.status === 'available' || it.status === 'disabled'}
            <button class="btn sm" onclick={() => bondIface(it.name)}><Plus size={14} />Bond it</button>
          {:else if it.status === 'excluded'}
            <span class="chip" title="Matches an ignore pattern in Settings › Interface discovery"><Ban size={12} />ignored</span>
          {/if}
        </div>
      {/each}
    </section>
  {/if}
{/if}

{#if selected}
  <LinkDrawer link={selected} editable downKey="rx" onclose={() => (store.selected = null)} />
{/if}

<style>
  .banner { display: flex; align-items: center; gap: 9px; padding: 10px 14px; border-radius: 12px; background: var(--warn-bg); color: var(--warn); border: 1px solid rgba(245, 165, 36, 0.35); font-size: 13px; }
  .hero { padding: 16px 16px 6px; overflow: hidden; }
  .hh { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; flex-wrap: wrap; padding: 2px 4px 0; }
  h2 { margin: 0; font-size: 17px; font-weight: 700; letter-spacing: -0.01em; }
  .hh p { margin: 2px 0 0; font-size: 12.5px; }
  .ctl { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .pw { display: inline-flex; align-items: center; gap: 8px; color: var(--muted); }
  .stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; }
  .row2 { display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); gap: 14px; }
  .chart, .race { min-width: 0; padding: 16px 18px; display: grid; gap: 10px; align-content: start; }
  .ch { display: flex; justify-content: space-between; align-items: baseline; font-size: 12px; }
  .rb { display: flex; gap: 18px; align-items: center; }
  .rb ul { list-style: none; margin: 0; padding: 0; display: grid; gap: 6px; flex: 1; min-width: 0; }
  .rb li { display: flex; align-items: center; gap: 8px; font-size: 12.5px; }
  .rb li i { width: 9px; height: 9px; border-radius: 3px; flex: none; }
  .rb li span { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .small { font-size: 11.5px; margin: 0; }
  .sh { display: flex; align-items: baseline; gap: 12px; margin-top: 6px; font-size: 12px; }
  .links { display: grid; grid-template-columns: repeat(auto-fill, minmax(360px, 1fr)); gap: 14px; }
  .others { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 10px; }
  .oi { display: flex; align-items: center; gap: 10px; padding: 10px 12px; }
  .oi .ic { width: 30px; height: 30px; border-radius: 9px; display: grid; place-items: center; color: var(--kc); background: color-mix(in srgb, var(--kc) 14%, transparent); flex: none; }
  .ot { display: grid; flex: 1; min-width: 0; }
  .ot b { font-size: 13px; }
  .ot span { font-size: 11.5px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  @media (max-width: 1100px) { .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); } .row2 { grid-template-columns: 1fr; } }
  @media (max-width: 520px) { .stats { grid-template-columns: 1fr; } .links { grid-template-columns: 1fr; } }
</style>
