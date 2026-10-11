<script lang="ts">
  // Quality ring: 0..100 swept over 270 degrees.
  let { value, size = 64, label = '' }: { value: number; size?: number; label?: string } = $props()
  const r = 26, c = 2 * Math.PI * r, sweep = 0.75
  const color = $derived(value >= 80 ? 'var(--ok)' : value >= 50 ? 'var(--warn)' : 'var(--bad)')
  const dash = $derived(`${(Math.max(0, Math.min(100, value)) / 100) * c * sweep} ${c}`)
</script>

<div class="gauge" style="width:{size}px;height:{size}px">
  <svg viewBox="0 0 64 64">
    <circle cx="32" cy="32" r={r} fill="none" stroke="var(--line-2)" stroke-width="5" stroke-linecap="round"
      stroke-dasharray="{c * sweep} {c}" transform="rotate(135 32 32)" />
    <circle cx="32" cy="32" r={r} fill="none" stroke={color} stroke-width="5" stroke-linecap="round"
      stroke-dasharray={dash} transform="rotate(135 32 32)" class="arc" style="filter:drop-shadow(0 0 4px {color})" />
  </svg>
  <div class="v num">{Math.round(value)}</div>
  {#if label}<div class="l">{label}</div>{/if}
</div>

<style>
  .gauge { position: relative; flex: none; }
  svg { width: 100%; height: 100%; }
  .arc { transition: stroke-dasharray 0.6s var(--ease-out), stroke 0.3s; }
  .v { position: absolute; inset: 0; display: grid; place-items: center; font-weight: 720; font-size: 16px; letter-spacing: -0.02em; }
  .l { position: absolute; left: 0; right: 0; bottom: 2px; text-align: center; font-size: 9.5px; font-weight: 650; color: var(--muted); letter-spacing: 0.06em; text-transform: uppercase; }
</style>
