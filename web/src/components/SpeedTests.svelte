<script lang="ts">
  import { onMount } from 'svelte'
  import { Gauge as GaugeIcon, Play, LoaderCircle, ArrowDown, ArrowUp, Clock } from '@lucide/svelte'
  import { api, store } from '../lib/store.svelte'
  import { kindColor, duration } from '../lib/format'
  import KindIcon from './KindIcon.svelte'

  interface Result { at: string; reason: string; downMbps: number; upMbps: number; pingMs: number; usedMB: number; publicIp?: string; provider?: string; colo?: string; error?: string }
  interface Row {
    link: string; label?: string; kind: string; metered?: boolean; status: string; detail: string
    trafficMbps: number; limitMbps?: number; nextAt?: string; phase?: string
    last?: Result; best?: Result; history: Result[]
  }
  let rows = $state<Row[]>([])
  let enabled = $state(true)
  let timer: ReturnType<typeof setTimeout> | undefined

  async function poll() {
    try {
      const r = await api('GET', '/api/x/speedtest')
      rows = r.links; enabled = r.enabled
    } catch {}
    timer = setTimeout(poll, rows.some((r) => r.status === 'testing') ? 1000 : 4000)
  }
  onMount(() => { poll(); return () => clearTimeout(timer) })

  async function run(r: Row) {
    if (r.metered) {
      const mb = r.last?.usedMB ? `about ${Math.round(r.last.usedMB)} MB` : 'up to 300 MB'
      if (!confirm(`${r.label || r.link} is metered. A speed test uses ${mb} of data. Run it?`)) return
    }
    try {
      const rep = await api('POST', `/api/x/speedtest/${encodeURIComponent(r.link)}`)
      rows = rep.links
      clearTimeout(timer); timer = setTimeout(poll, 1000)
    } catch (e: any) { store.toast(e.message, 'err') }
  }

  const ago = (t: string) => duration((Date.now() - new Date(t).getTime()) / 1000) + ' ago'
  const fmt = (v: number) => (v >= 100 ? v.toFixed(0) : v.toFixed(1))
  const tone: Record<string, string> = { testing: 'info', waiting: 'warn', ready: 'info', idle: 'ok', manual: '', off: '' }
  const label: Record<string, string> = { testing: 'testing', waiting: 'waiting', ready: 'starting', idle: 'up to date', manual: 'manual only', off: 'auto off' }
</script>

<section class="card st">
  <div class="hd">
    <div class="ic"><GaugeIcon size={18} /></div>
    <div class="ttl">
      <h3>Connection speeds</h3>
      <span class="muted">Each connection tested on its own, outside the bond, while it's quiet: its real bandwidth. {enabled ? 'Re-tested on a schedule and whenever its address changes.' : 'Automatic tests are off (Settings → Speed tests).'}</span>
    </div>
  </div>

  {#if rows.length === 0}
    <p class="muted small">No connections found yet.</p>
  {/if}
  <div class="rows">
    {#each rows as r (r.link)}
      {@const hist = r.history.filter((h) => !h.error).slice(-16)}
      {@const top = Math.max(1, ...hist.map((h) => h.downMbps))}
      <div class="row" style="--kc:{kindColor(r.kind)}">
        <div class="who">
          <div class="badge"><KindIcon kind={r.kind} size={16} /></div>
          <div>
            <b>{r.label || r.link}{#if r.metered}<span class="chip tiny">metered</span>{/if}</b>
            <span class="muted small">{r.link}{r.last?.provider ? ` · ${r.last.provider}` : ''}{r.last?.publicIp ? ` · ${r.last.publicIp}` : ''}</span>
          </div>
        </div>

        <div class="nums">
          {#if r.status === 'testing'}
            <span class="testing"><LoaderCircle size={15} class="spin" /> {r.phase}…</span>
          {:else if r.last}
            <span class="n"><ArrowDown size={14} /><b class="num">{fmt(r.last.downMbps)}</b><em>Mbit/s</em></span>
            <span class="n"><ArrowUp size={14} /><b class="num">{fmt(r.last.upMbps)}</b><em>Mbit/s</em></span>
            <span class="n ping"><b class="num">{r.last.pingMs.toFixed(0)}</b><em>ms</em></span>
          {:else}
            <span class="muted small">not tested yet</span>
          {/if}
        </div>

        <svg class="spark" viewBox="0 0 96 28" preserveAspectRatio="none" aria-label="download speed history">
          {#each hist as h, i}
            {@const bh = Math.max(2, (h.downMbps / top) * 26)}
            <rect x={i * 6} y={28 - bh} width="4" height={bh} rx="1" fill="var(--kc)" opacity={i === hist.length - 1 ? 1 : 0.45}>
              <title>{new Date(h.at).toLocaleString()}: ↓{fmt(h.downMbps)} ↑{fmt(h.upMbps)} Mbit/s ({h.reason})</title>
            </rect>
          {/each}
        </svg>

        <div class="when">
          <span class="chip {tone[r.status]}">{label[r.status] ?? r.status}</span>
          {#if r.last}<span class="muted small"><Clock size={12} /> {ago(r.last.at)} · {r.last.reason}{r.best && r.best.at !== r.last.at ? ` · best ${fmt(r.best.downMbps)}↓` : ''}</span>{/if}
        </div>

        <button class="btn sm" onclick={() => run(r)} disabled={rows.some((x) => x.status === 'testing')}>
          <Play size={14} />Test now
        </button>

        {#if r.status !== 'idle' || r.history.at(-1)?.error}
          <p class="detail small" class:err={!!r.history.at(-1)?.error && r.status === 'idle'}>
            {r.history.at(-1)?.error && r.status === 'idle' ? `Last test failed: ${r.history.at(-1)?.error}` : r.detail}
          </p>
        {:else}
          <p class="detail small muted">{r.detail}</p>
        {/if}
      </div>
    {/each}
  </div>
</section>

<style>
  .st { padding: 16px 18px; display: grid; gap: 12px; }
  .hd { display: flex; align-items: center; gap: 14px; }
  .ic { width: 36px; height: 36px; border-radius: 10px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); flex: none; }
  .ttl { display: grid; }
  h3 { margin: 0; font-size: 15px; font-weight: 680; }
  .ttl span { font-size: 12px; }
  .rows { display: grid; gap: 4px; }
  .row { display: grid; grid-template-columns: minmax(150px, 1.1fr) minmax(220px, 1.4fr) 96px minmax(170px, 1fr) auto; gap: 6px 16px; align-items: center; padding: 10px 0; border-top: 1px solid var(--border); }
  .row:first-child { border-top: 0; }
  .who { display: flex; gap: 10px; align-items: center; min-width: 0; }
  .who > div:last-child { display: grid; min-width: 0; }
  .who span { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .badge { width: 30px; height: 30px; border-radius: 9px; display: grid; place-items: center; color: var(--kc); background: color-mix(in srgb, var(--kc) 14%, transparent); flex: none; }
  .chip.tiny { font-size: 10px; padding: 1px 6px; margin-left: 6px; vertical-align: middle; }
  .nums { display: flex; gap: 16px; align-items: baseline; flex-wrap: wrap; }
  .n { display: inline-flex; align-items: baseline; gap: 4px; color: var(--muted); }
  .n b { font-size: 20px; color: var(--text); font-weight: 700; }
  .n em { font-style: normal; font-size: 11px; }
  .n.ping b { font-size: 15px; }
  .testing { display: inline-flex; gap: 8px; align-items: center; color: var(--accent-2); font-size: 13px; }
  .spark { width: 96px; height: 28px; }
  .when { display: grid; gap: 4px; justify-items: start; }
  .when span { display: inline-flex; gap: 4px; align-items: center; }
  .detail { grid-column: 1 / -1; margin: 0; color: var(--warn); }
  .detail.muted { color: var(--muted); }
  .detail.err { color: var(--bad); }
  .small { font-size: 12px; }
  @media (max-width: 860px) {
    .row { grid-template-columns: 1fr auto; }
    .nums, .when, .spark { grid-column: 1 / -1; }
  }
</style>
