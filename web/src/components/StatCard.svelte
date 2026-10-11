<script lang="ts">
  import type { Snippet } from 'svelte'
  let { title, value, unit = '', sub = '', hero = false, children }:
    { title: string; value: string; unit?: string; sub?: string; hero?: boolean; children?: Snippet } = $props()
</script>

<div class="sc card" class:hero>
  <h3 class="section-title">{title}</h3>
  <div class="v"><b class="num" class:grad-text={hero}>{value}</b>{#if unit}<span>{unit}</span>{/if}</div>
  {#if sub}<div class="sub">{sub}</div>{/if}
  {#if children}<div class="extra">{@render children()}</div>{/if}
</div>

<style>
  .sc { padding: 16px 18px; display: grid; gap: 4px; align-content: start; min-width: 0; position: relative; overflow: hidden; }
  .sc.hero { background: radial-gradient(120% 140% at 100% 0%, rgba(139, 92, 246, 0.22), transparent 60%), var(--panel); border-color: color-mix(in srgb, var(--accent) 40%, var(--line)); }
  .v { display: flex; align-items: baseline; gap: 6px; }
  .v b { font-size: 28px; font-weight: 720; letter-spacing: -0.02em; line-height: 1.15; }
  .v span { color: var(--muted); font-weight: 600; font-size: 13px; }
  .sub { font-size: 12px; color: var(--muted); }
  .extra { margin-top: 8px; }
</style>
