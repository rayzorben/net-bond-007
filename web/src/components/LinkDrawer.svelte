<script lang="ts">
  import { X, Copy } from '@lucide/svelte'
  import type { LinkView } from '../lib/types'
  import { store } from '../lib/store.svelte'
  import { bps, ms, pct, bytes, count, kindColor, kindLabel, stateTone, displayName } from '../lib/format'
  import KindIcon from './KindIcon.svelte'
  import Gauge from './Gauge.svelte'
  import TimeChart from './TimeChart.svelte'
  import Toggle from './Toggle.svelte'
  import Segmented from './Segmented.svelte'

  let { link, editable, downKey = 'rx', onclose }:
    { link: LinkView; editable: boolean; downKey?: 'rx' | 'tx'; onclose: () => void } = $props()

  const hist = $derived(link.history ?? [])
  const times = $derived(hist.map((s) => s.t))
  const winRatio = $derived(link.wins + link.dups ? link.wins / (link.wins + link.dups) : 0)

  // ---- per-link settings (client only) ----
  let lc = $state<{ disabled?: boolean; backup?: boolean; weight?: number; label?: string; maxMbps?: number }>({})
  let loadedFor = ''
  $effect(() => {
    if (loadedFor !== link.name) {
      loadedFor = link.name
      lc = { ...(store.config?.links?.[link.name] ?? {}) }
    }
  })
  let timer: ReturnType<typeof setTimeout> | undefined
  function save(immediate = false) {
    clearTimeout(timer)
    timer = setTimeout(async () => {
      const err = await store.saveLink(link.name, $state.snapshot(lc))
      if (err) store.toast(err, 'err')
      else store.toast(`${displayName(link)} updated · applied live`)
    }, immediate ? 0 : 600)
  }

  const counters = $derived([
    ['Link id', String(link.id)], ['Interface', link.name], ['Kind', kindLabel[link.kind] ?? link.kind],
    ['Local address', link.local || '—'], ['Remote address', link.remote || '—'],
    ['State', link.state], ['Scheduling weight', link.weight.toFixed(2)], ['Capacity estimate', bps(link.capBps)], ['Paced rate (learned)', link.paceBps ? bps(link.paceBps) : 'off'],
    ['RTT (smoothed)', ms(link.rttMs)], ['RTT (min)', ms(link.minRttMs)], ['Jitter', ms(link.jitterMs)], ['Probe loss', pct(link.lossPct, 2)],
    ['Bytes sent', bytes(link.txBytes)], ['Bytes received', bytes(link.rxBytes)],
    ['Packets sent', count(link.txPackets)], ['Packets received', count(link.rxPackets)],
    ['Race wins (first copy)', count(link.wins)], ['Duplicates discarded', count(link.dups)],
    ['Queue drops', count(link.drops)], ['Send errors', count(link.errors)], ['Queue depth now', String(link.queue)],
    ['Last error', link.lastError || '—'],
  ])

  async function copyJSON() {
    await navigator.clipboard.writeText(JSON.stringify({ ...link, history: undefined }, null, 2))
    store.toast('Copied link data')
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div class="scrim" onclick={onclose} role="presentation"></div>
<aside class="drawer glass" style="--kc:{kindColor(link.kind)}">
  <header>
    <div class="badge"><KindIcon kind={link.kind} size={22} /></div>
    <div class="ttl">
      <h2>{displayName(link)}</h2>
      <span>{kindLabel[link.kind] ?? link.kind} · {link.name}{link.local ? ` · ${link.local}` : ''}</span>
    </div>
    <span class="chip {stateTone(link.state)}"><span class="dot"></span>{link.state}</span>
    <button class="btn icon ghost" onclick={onclose} aria-label="Close"><X size={18} /></button>
  </header>

  <div class="scroll">
    <section class="kpis">
      <Gauge value={link.state === 'down' ? 0 : link.quality} size={84} label="quality" />
      <div class="kpi"><span>Latency</span><b class="num">{ms(link.rttMs)}</b><em>min {ms(link.minRttMs)}</em></div>
      <div class="kpi"><span>Jitter</span><b class="num">{ms(link.jitterMs)}</b></div>
      <div class="kpi"><span>Loss</span><b class="num">{pct(link.lossPct)}</b></div>
      <div class="kpi"><span>Capacity</span><b class="num">{bps(link.capBps)}</b><em>weight {link.weight.toFixed(1)}</em></div>
      <div class="kpi gold"><span>Races won</span><b class="num">{pct(winRatio * 100, 0)}</b><em>{count(link.wins)} first · {count(link.dups)} dup</em></div>
    </section>

    {#if editable}
      <section class="card controls">
        <h3 class="section-title">This link</h3>
        <label class="row">
          <div><b>Use in bond</b><span>Turn off to stop sending anything over {link.name}.</span></div>
          <Toggle checked={!lc.disabled} onchange={(v) => { lc.disabled = !v; save(true) }} label="Use in bond" />
        </label>
        <label class="row">
          <div><b>Backup only</b><span>Carry traffic only when every primary link is down (e.g. metered cellular).</span></div>
          <Toggle checked={!!lc.backup} onchange={(v) => { lc.backup = v; save(true) }} label="Backup only" />
        </label>
        <div class="row">
          <div><b>Name</b><span>A friendly label for the dashboard.</span></div>
          <input class="input" style="max-width:200px" placeholder={link.name} bind:value={lc.label} oninput={() => save()} />
        </div>
        <div class="row col">
          <div class="rowhead">
            <div><b>Scheduling weight</b><span>Share of striped traffic. Auto measures capacity and latency.</span></div>
            <Segmented size="sm" value={lc.weight ? 'manual' : 'auto'}
              options={[{ value: 'auto', label: 'Auto' }, { value: 'manual', label: 'Manual' }]}
              onchange={(v) => { lc.weight = v === 'auto' ? 0 : Math.max(1, Math.round(link.weight)); save(true) }} />
          </div>
          {#if lc.weight}
            <div class="slider">
              <input type="range" min="1" max="1000" step="1" bind:value={lc.weight} oninput={() => save()} />
              <b class="num">{lc.weight}</b>
            </div>
          {/if}
        </div>
        <div class="row">
          <div><b>Capacity hint</b><span>Known bandwidth in Mbit/s. 0 = measure automatically.</span></div>
          <input class="input" type="number" min="0" step="1" style="max-width:120px" bind:value={lc.maxMbps} oninput={() => save()} />
        </div>
      </section>
    {/if}

    <section class="card chartc">
      <h3 class="section-title">Latency</h3>
      <TimeChart {times} format={(v) => ms(v)} height={150}
        series={[{ label: 'RTT', color: 'var(--kc)', values: hist.map((s) => s.r) }, { label: 'Jitter', color: 'var(--warn)', values: hist.map((s) => s.j), fill: false, dashed: true }]} />
    </section>
    <section class="card chartc">
      <h3 class="section-title">Throughput on this link</h3>
      <TimeChart {times} format={(v) => bps(v, 0)} floor={1e6} height={150}
        series={[{ label: 'Down', color: 'var(--cyan)', values: hist.map((s) => (downKey === 'rx' ? s.rx : s.tx)) }, { label: 'Up', color: 'var(--accent-2)', values: hist.map((s) => (downKey === 'rx' ? s.tx : s.rx)) }]} />
    </section>
    <div class="two">
      <section class="card chartc">
        <h3 class="section-title">Probe loss</h3>
        <TimeChart {times} format={(v) => pct(v * 100, 0)} height={120} series={[{ label: 'Loss', color: 'var(--bad)', values: hist.map((s) => s.l) }]} />
      </section>
      <section class="card chartc">
        <h3 class="section-title">Share of race wins</h3>
        <TimeChart {times} max={1} format={(v) => pct(v * 100, 0)} height={120} series={[{ label: 'Wins', color: 'var(--gold)', values: hist.map((s) => s.w) }]} />
      </section>
    </div>

    <section class="card data">
      <div class="dh">
        <h3 class="section-title">Raw counters</h3>
        <button class="btn sm ghost" onclick={copyJSON}><Copy size={14} />Copy JSON</button>
      </div>
      <table>
        <tbody>
          {#each counters as [k, v]}
            <tr><td>{k}</td><td class="mono">{v}</td></tr>
          {/each}
        </tbody>
      </table>
    </section>
  </div>
</aside>

<style>
  .scrim { position: fixed; inset: 0; background: rgba(5, 7, 12, 0.5); backdrop-filter: blur(3px); z-index: 40; animation: fade-up 0.2s; }
  .drawer {
    position: fixed; top: 10px; right: 10px; bottom: 10px; width: min(720px, calc(100vw - 20px)); z-index: 41;
    display: grid; grid-template-rows: auto 1fr; overflow: hidden; background: var(--glass-strong);
    animation: slide 0.32s var(--ease-out);
  }
  @keyframes slide { from { transform: translateX(40px); opacity: 0; } }
  header { display: flex; align-items: center; gap: 12px; padding: 16px 18px; border-bottom: 1px solid var(--line); }
  .badge { width: 44px; height: 44px; border-radius: 12px; display: grid; place-items: center; color: var(--kc); background: color-mix(in srgb, var(--kc) 16%, transparent); box-shadow: 0 0 24px -6px var(--kc); }
  .ttl { flex: 1; min-width: 0; }
  h2 { margin: 0; font-size: 18px; font-weight: 700; letter-spacing: -0.01em; }
  .ttl span { color: var(--muted); font-size: 12px; }
  .scroll { overflow-y: auto; overflow-x: hidden; padding: 16px 18px 24px; display: grid; gap: 14px; align-content: start; }
  .kpis { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; padding: 4px 2px; }
  .kpi { display: grid; }
  .kpi span { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.08em; color: var(--muted); font-weight: 650; }
  .kpi b { font-size: 20px; font-weight: 720; letter-spacing: -0.02em; }
  .kpi em { font-style: normal; font-size: 11px; color: var(--faint); }
  .kpi.gold b { color: var(--gold); }
  .controls { padding: 14px 16px; display: grid; gap: 4px; }
  .row { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 9px 0; border-top: 1px solid var(--line); cursor: default; }
  .row:first-of-type { border-top: 0; }
  .row.col { display: grid; gap: 8px; }
  .rowhead { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
  .row b { display: block; font-size: 13.5px; font-weight: 600; }
  .row span { font-size: 12px; color: var(--muted); }
  .slider { display: flex; align-items: center; gap: 12px; }
  .slider b { min-width: 40px; text-align: right; }
  .chartc { padding: 14px 16px 10px; display: grid; gap: 8px; }
  .two { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 14px; }
  .scroll > :global(*) { min-width: 0; }
  @media (max-width: 640px) { .two { grid-template-columns: 1fr; } }
  .data { padding: 14px 16px; }
  .dh { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  td { padding: 6px 0; border-top: 1px solid var(--line); }
  td:first-child { color: var(--muted); }
  td:last-child { text-align: right; }
</style>
