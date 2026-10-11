<script lang="ts">
  // Share of first-arrivals ("race wins") per link.
  let { slices, size = 120, center = '', sub = '' }:
    { slices: { label: string; value: number; color: string }[]; size?: number; center?: string; sub?: string } = $props()
  const r = 44, c = 2 * Math.PI * r
  const arcs = $derived.by(() => {
    const total = slices.reduce((a, s) => a + s.value, 0) || 1
    let off = 0
    return slices.map((s) => {
      const len = (s.value / total) * c
      const a = { ...s, dash: `${Math.max(len - 2, 0)} ${c}`, off: -off }
      off += len
      return a
    })
  })
</script>

<div class="donut" style="width:{size}px;height:{size}px">
  <svg viewBox="0 0 110 110">
    <circle cx="55" cy="55" r={r} fill="none" stroke="var(--line)" stroke-width="12" />
    {#each arcs as a (a.label)}
      <circle cx="55" cy="55" r={r} fill="none" stroke={a.color} stroke-width="12"
        stroke-dasharray={a.dash} stroke-dashoffset={a.off} transform="rotate(-90 55 55)" class="arc" />
    {/each}
  </svg>
  <div class="c"><b class="num">{center}</b><span>{sub}</span></div>
</div>

<style>
  .donut { position: relative; flex: none; }
  svg { width: 100%; height: 100%; }
  .arc { transition: stroke-dasharray 0.6s var(--ease-out), stroke-dashoffset 0.6s var(--ease-out); }
  .c { position: absolute; inset: 0; display: grid; place-content: center; text-align: center; }
  b { font-size: 18px; font-weight: 720; letter-spacing: -0.02em; }
  span { font-size: 10.5px; color: var(--muted); font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; }
</style>
