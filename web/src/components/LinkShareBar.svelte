<script lang="ts">
  // Stacked bar: each link's share of a connection's traffic.
  import { kindColor } from '../lib/format'
  interface FL { id: number; name: string; kind: string; copies: number; wins: number }
  let { links, by = 'wins', width = 90 }: { links: FL[] | null; by?: 'wins' | 'copies'; width?: number } = $props()
  const parts = $derived.by(() => {
    const ls = links ?? []
    const total = ls.reduce((a, l) => a + (by === 'wins' ? l.wins : l.copies), 0)
    return total ? ls.map((l) => ({ ...l, share: (by === 'wins' ? l.wins : l.copies) / total })).filter((p) => p.share > 0) : []
  })
  const tip = $derived(parts.map((p) => `${p.name}: ${(p.share * 100).toFixed(0)}%`).join(' · '))
</script>

<span class="bar" style="width:{width}px" title={tip || 'no traffic yet'}>
  {#each parts as p (p.id)}
    <i style="width:{p.share * 100}%;background:{kindColor(p.kind)}"></i>
  {/each}
</span>

<style>
  .bar { display: inline-flex; height: 8px; border-radius: 4px; overflow: hidden; background: var(--line); vertical-align: middle; }
  i { display: block; height: 100%; }
  i + i { box-shadow: inset 1px 0 0 var(--panel); }
</style>
