<script lang="ts">
  import { onMount } from 'svelte'
  import { Search, RefreshCw } from '@lucide/svelte'
  import { api } from '../lib/store.svelte'
  import { bps, bytes, ms, duration, kindColor } from '../lib/format'
  import Segmented from '../components/Segmented.svelte'
  import TimeChart from '../components/TimeChart.svelte'

  interface HLink { n: string; kind: string; st: string; rtt: number; loss: number; down: number; up: number; b?: boolean }
  interface Rec { t: number; k: string; c: string; s?: string; e?: string
    sample?: { mode: string; copies: number; down: number; up: number; rtx: number; links: HLink[] | null }
    conn?: { host?: string; dst: string; proto: string; down: number; up: number; secs: number; avgDown: number; peakDown: number; links: Record<string, number> } }

  let hours = $state('6')
  let client = $state('')
  let data = $state<{ enabled: boolean; clients: string[]; samples: Rec[]; events: Rec[]; connections: Rec[] } | null>(null)
  let loading = $state(false)
  let q = $state('')
  let err = $state('')

  async function load() {
    loading = true
    try {
      data = await api('GET', `/api/x/history?hours=${hours}${client ? `&client=${encodeURIComponent(client)}` : ''}`)
      if (!client && data!.clients.length === 1) client = data!.clients[0]
      err = ''
    } catch (e: any) { err = e.message }
    loading = false
  }
  onMount(() => { load(); const t = setInterval(load, 30000); return () => clearInterval(t) })

  const samples = $derived((data?.samples ?? []).filter((r) => !client || r.c === client))
  const times = $derived(samples.map((r) => r.t))
  const linkNames = $derived([...new Set(samples.flatMap((r) => (r.sample?.links ?? []).map((l) => l.n)))].sort())
  const kindOf = $derived(Object.fromEntries(samples.flatMap((r) => (r.sample?.links ?? []).map((l) => [l.n, l.kind]))))
  const perLink = (key: 'rtt' | 'loss') => linkNames.map((n) => ({
    label: n, color: kindColor(kindOf[n] ?? 'other'), fill: false,
    values: samples.map((r) => { const l = r.sample?.links?.find((x) => x.n === n); return l && l.st !== 'down' ? l[key] : 0 }),
  }))
  const modeColor: Record<string, string> = { balancing: 'var(--ok)', mirroring: 'var(--warn)', redundant: 'var(--info)', direct: 'var(--faint)', single: 'var(--accent-2)' }
  const band = $derived.by(() => {
    if (samples.length < 2) return []
    const t0 = samples[0].t, t1 = samples[samples.length - 1].t, span = Math.max(1, t1 - t0)
    const segs: { mode: string; x: number; w: number }[] = []
    samples.forEach((r, i) => {
      const end = i + 1 < samples.length ? samples[i + 1].t : t1
      const mode = r.sample?.mode ?? '?'
      const last = segs[segs.length - 1]
      if (last && last.mode === mode) last.w += ((end - r.t) / span) * 100
      else segs.push({ mode, x: ((r.t - t0) / span) * 100, w: ((end - r.t) / span) * 100 })
    })
    return segs
  })
  const conns = $derived((data?.connections ?? []).filter((r) => (!client || r.c === client) &&
    (!q || `${r.conn?.host ?? ''} ${r.conn?.dst ?? ''} ${r.c}`.toLowerCase().includes(q.toLowerCase()))))
  const events = $derived((data?.events ?? []).filter((r) => !client || r.c === client))
  const fmtTime = (t: number) => new Date(t).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
</script>

<div class="bar">
  <Segmented value={hours} onchange={(v) => { hours = v; load() }}
    options={[{ value: '1', label: '1h' }, { value: '6', label: '6h' }, { value: '24', label: '24h' }, { value: '72', label: '3d' }, { value: '168', label: '7d' }]} />
  {#if (data?.clients.length ?? 0) > 1}
    <select class="input" style="width:auto" bind:value={client} onchange={load}>
      <option value="">All clients</option>
      {#each data?.clients ?? [] as c}<option value={c}>{c}</option>{/each}
    </select>
  {/if}
  <div class="spacer"></div>
  <button class="btn sm ghost" onclick={load} disabled={loading}><RefreshCw size={14} />Refresh</button>
</div>
{#if err}<div class="err">{err}</div>{/if}
{#if data && !data.enabled}<div class="err">History is turned off (Settings › History).</div>{/if}

{#if samples.length}
  <section class="card pad">
    <h3 class="section-title">What automatic scaling was doing</h3>
    <div class="band">{#each band as s}<i style="left:{s.x}%;width:{s.w}%;background:{modeColor[s.mode] ?? 'var(--line-2)'}" title={s.mode}></i>{/each}</div>
    <div class="legend">{#each Object.entries(modeColor) as [m, c]}<span><i style="background:{c}"></i>{m}</span>{/each}</div>
  </section>
  <section class="card pad">
    <h3 class="section-title">Throughput through the bond</h3>
    <TimeChart {times} format={(v) => bps(v, 0)} floor={1e6} height={170}
      series={[{ label: 'Download', color: 'var(--cyan)', values: samples.map((r) => r.sample?.down ?? 0) }, { label: 'Upload', color: 'var(--accent-2)', values: samples.map((r) => r.sample?.up ?? 0) }]} />
  </section>
  <div class="two">
    <section class="card pad"><h3 class="section-title">Latency per link</h3><TimeChart {times} format={(v) => ms(v)} height={150} series={perLink('rtt')} /></section>
    <section class="card pad"><h3 class="section-title">Loss per link</h3><TimeChart {times} format={(v) => `${v.toFixed(0)}%`} height={150} series={perLink('loss')} /></section>
  </div>
{:else if data}
  <section class="card pad muted">No samples in this period yet.</section>
{/if}

<section class="card pad">
  <h3 class="section-title">Events</h3>
  <div class="events">
    {#each events.slice(0, 200) as e (e.t + e.e!)}
      <div class="ev"><span class="mono faint">{fmtTime(e.t)}</span><span class="chip">{e.c}</span><span>{e.e}</span></div>
    {:else}<p class="muted">No events in this period.</p>{/each}
  </div>
</section>

<section class="card pad">
  <div class="ch"><h3 class="section-title">Connections ({conns.length})</h3>
    <div class="search tile"><Search size={14} /><input placeholder="Filter by site or address…" bind:value={q} /></div></div>
  <div class="wrap">
    <table>
      <thead><tr><th>Ended</th><th>Client</th><th>Destination</th><th class="r">Down</th><th class="r">Up</th><th class="r">Duration</th><th class="r">Avg ↓</th><th class="r">Peak ↓</th><th>Links</th></tr></thead>
      <tbody>
        {#each conns.slice(0, 500) as r (r.t + r.c + r.conn!.dst)}
          {@const c = r.conn!}
          <tr>
            <td class="mono faint">{fmtTime(r.t)}</td>
            <td>{r.c}</td>
            <td><b>{c.host || c.dst}</b> <span class="mono faint">{c.host ? c.dst : ''} {c.proto}</span></td>
            <td class="r num">{bytes(c.down)}</td><td class="r num">{bytes(c.up)}</td>
            <td class="r num">{duration(c.secs)}</td><td class="r num">{bps(c.avgDown)}</td><td class="r num">{bps(c.peakDown)}</td>
            <td class="lk">{#each Object.entries(c.links ?? {}) as [n, sh]}<span title="{n}: {(sh * 100).toFixed(0)}%"><i style="width:{sh * 70}px;background:{kindColor(kindOf[n] ?? 'other')}"></i></span>{/each}</td>
          </tr>
        {:else}<tr><td colspan="9" class="muted">No finished connections in this period.</td></tr>{/each}
      </tbody>
    </table>
  </div>
</section>

<style>
  .bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .spacer { flex: 1; }
  .err { color: var(--bad); background: var(--bad-bg); padding: 10px 14px; border-radius: 10px; font-size: 13px; }
  .pad { padding: 14px 16px; display: grid; gap: 10px; }
  .two { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 14px; }
  @media (max-width: 900px) { .two { grid-template-columns: 1fr; } }
  .band { position: relative; height: 18px; border-radius: 6px; background: var(--line); overflow: hidden; }
  .band i { position: absolute; top: 0; bottom: 0; }
  .legend { display: flex; gap: 14px; font-size: 11.5px; color: var(--muted); flex-wrap: wrap; }
  .legend span { display: inline-flex; align-items: center; gap: 6px; text-transform: capitalize; }
  .legend i { width: 9px; height: 9px; border-radius: 3px; display: inline-block; }
  .events { display: grid; gap: 4px; max-height: 320px; overflow: auto; font-size: 12.5px; }
  .ev { display: flex; gap: 10px; align-items: center; padding: 3px 0; border-bottom: 1px solid color-mix(in srgb, var(--line) 50%, transparent); }
  .ch { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; }
  .search { display: flex; align-items: center; gap: 8px; padding: 0 10px; height: 30px; width: min(280px, 100%); color: var(--muted); background: var(--bg); }
  .search input { border: 0; outline: 0; background: transparent; flex: 1; }
  .wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  th { text-align: left; font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); font-weight: 650; padding: 6px 8px; border-bottom: 1px solid var(--line); white-space: nowrap; }
  td { padding: 6px 8px; border-bottom: 1px solid color-mix(in srgb, var(--line) 60%, transparent); white-space: nowrap; }
  .r { text-align: right; }
  .lk { display: flex; gap: 2px; align-items: center; }
  .lk i { display: inline-block; height: 8px; border-radius: 3px; }
</style>
