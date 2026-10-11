<script lang="ts">
  // Multi-series time chart with a hover crosshair and tooltip.
  interface Series { label: string; color: string; values: number[]; dashed?: boolean; fill?: boolean }
  let { times, series, format, height = 160, max: fixedMax = 0, floor = 0 }:
    { times: number[]; series: Series[]; format: (v: number) => string; height?: number; max?: number; floor?: number } = $props()

  const uid = Math.random().toString(36).slice(2)
  let w = $state(300)
  let hover = $state<number | null>(null)
  const pad = { l: 52, r: 10, t: 10, b: 22 }

  const geo = $derived.by(() => {
    const n = times.length
    const iw = Math.max(w - pad.l - pad.r, 10), ih = height - pad.t - pad.b
    let max = fixedMax
    if (!max) for (const s of series) for (const v of s.values) if (v > max) max = v
    max = niceMax(Math.max(max, floor) || 1)
    const x = (i: number) => pad.l + (n <= 1 ? 0 : (i / (n - 1)) * iw)
    const y = (v: number) => pad.t + ih - (Math.min(v, max) / max) * ih
    const paths = series.map((s) => {
      const line = s.values.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('')
      return { ...s, line, area: n ? `${line}L${x(n - 1)},${pad.t + ih}L${x(0)},${pad.t + ih}Z` : '' }
    })
    const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({ y: y(max * f), label: format(max * f) }))
    const tl: { x: number; label: string }[] = []
    if (n > 1) for (const f of [0, 0.5, 1]) {
      const i = Math.round(f * (n - 1))
      tl.push({ x: x(i), label: ago(times[n - 1] - times[i]) })
    }
    return { x, y, paths, ticks, tl, iw, ih }
  })

  function niceMax(v: number) {
    const p = Math.pow(10, Math.floor(Math.log10(v)))
    for (const m of [1, 2, 2.5, 5, 10]) if (v <= m * p) return m * p
    return 10 * p
  }
  function ago(ms: number) {
    const s = Math.round(ms / 1000)
    return s === 0 ? 'now' : s < 60 ? `-${s}s` : `-${Math.round(s / 60)}m`
  }
  function move(e: PointerEvent) {
    const r = (e.currentTarget as SVGElement).getBoundingClientRect()
    const fx = (e.clientX - r.left - pad.l) / geo.iw
    hover = times.length ? Math.max(0, Math.min(times.length - 1, Math.round(fx * (times.length - 1)))) : null
  }
</script>

<div class="chart" bind:clientWidth={w}>
  <svg width={w} {height} onpointermove={move} onpointerleave={() => (hover = null)} role="img">
    <defs>
      {#each series as s, i}
        <linearGradient id="tc{uid}{i}" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stop-color={s.color} stop-opacity="0.28" />
          <stop offset="1" stop-color={s.color} stop-opacity="0" />
        </linearGradient>
      {/each}
    </defs>
    {#each geo.ticks as t}
      <line x1={pad.l} x2={w - pad.r} y1={t.y} y2={t.y} class="grid" />
      <text x={pad.l - 8} y={t.y + 3.5} class="ax" text-anchor="end">{t.label}</text>
    {/each}
    {#each geo.tl as t, i}
      <text x={t.x} y={height - 6} class="ax" text-anchor={i === 0 ? 'start' : i === geo.tl.length - 1 ? 'end' : 'middle'}>{t.label}</text>
    {/each}
    {#each geo.paths as p, i}
      {#if p.fill !== false}<path d={p.area} fill="url(#tc{uid}{i})" />{/if}
      <path d={p.line} fill="none" stroke={p.color} stroke-width="1.7" stroke-linejoin="round" stroke-dasharray={p.dashed ? '4 4' : ''} />
    {/each}
    {#if hover !== null}
      <line x1={geo.x(hover)} x2={geo.x(hover)} y1={pad.t} y2={pad.t + geo.ih} class="cross" />
      {#each series as s}
        <circle cx={geo.x(hover)} cy={geo.y(s.values[hover] ?? 0)} r="3.5" fill={s.color} stroke="var(--panel)" stroke-width="2" />
      {/each}
    {/if}
  </svg>
  {#if hover !== null}
    <div class="tip glass" style="left:{Math.min(geo.x(hover) + 12, w - 170)}px">
      <div class="t">{new Date(times[hover]).toLocaleTimeString()}</div>
      {#each series as s}
        <div class="row"><i style="background:{s.color}"></i>{s.label}<b class="num">{format(s.values[hover] ?? 0)}</b></div>
      {/each}
    </div>
  {/if}
  <div class="legend">
    {#each series as s}<span><i style="background:{s.color}"></i>{s.label}</span>{/each}
  </div>
</div>

<style>
  .chart { position: relative; width: 100%; min-width: 0; overflow: hidden; }
  svg { display: block; touch-action: none; max-width: 100%; }
  .grid { stroke: var(--line); stroke-width: 1; }
  .cross { stroke: var(--faint); stroke-dasharray: 3 3; }
  .ax { fill: var(--faint); font-size: 10.5px; font-family: var(--mono); }
  .tip { position: absolute; top: 6px; padding: 8px 10px; min-width: 150px; font-size: 12px; pointer-events: none; animation: fade-up 0.12s var(--ease-out); }
  .tip .t { color: var(--muted); font-size: 11px; margin-bottom: 4px; }
  .row { display: flex; align-items: center; gap: 6px; }
  .row b { margin-left: auto; font-weight: 650; }
  i { width: 8px; height: 8px; border-radius: 2px; display: inline-block; }
  .legend { display: flex; gap: 14px; flex-wrap: wrap; font-size: 11.5px; color: var(--muted); padding-left: 52px; }
  .legend span { display: inline-flex; align-items: center; gap: 6px; }
</style>
