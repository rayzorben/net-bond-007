<script lang="ts">
  let { values, color = 'var(--accent)', height = 36, fill = true, max: fixedMax = 0 }:
    { values: number[]; color?: string; height?: number; fill?: boolean; max?: number } = $props()
  const uid = Math.random().toString(36).slice(2)
  const W = 100
  const geo = $derived.by(() => {
    const v = values.length ? values : [0]
    const max = fixedMax || Math.max(...v, 1e-9)
    const n = Math.max(v.length - 1, 1)
    const pts = v.map((y, i) => [(i / n) * W, height - 2 - (Math.min(y, max) / max) * (height - 4)])
    const line = pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(2)},${p[1].toFixed(2)}`).join('')
    return { line, area: `${line}L${W},${height}L0,${height}Z` }
  })
</script>

<svg viewBox="0 0 {W} {height}" preserveAspectRatio="none" style="height:{height}px">
  <defs>
    <linearGradient id="sg{uid}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color={color} stop-opacity="0.35" />
      <stop offset="1" stop-color={color} stop-opacity="0" />
    </linearGradient>
  </defs>
  {#if fill}<path d={geo.area} fill="url(#sg{uid})" />{/if}
  <path d={geo.line} fill="none" stroke={color} stroke-width="1.6" vector-effect="non-scaling-stroke" stroke-linejoin="round" />
</svg>

<style>
  svg { width: 100%; display: block; overflow: visible; }
</style>
