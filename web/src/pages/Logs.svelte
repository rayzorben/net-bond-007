<script lang="ts">
  import { onMount } from 'svelte'
  import { Search, Pause, Play } from '@lucide/svelte'
  import type { LogEntry } from '../lib/types'
  import { store } from '../lib/store.svelte'
  import Segmented from '../components/Segmented.svelte'

  let entries = $state<LogEntry[]>([])
  let level = $state('all')
  let q = $state('')
  let paused = $state(false)
  const rank: Record<string, number> = { debug: 0, info: 1, warn: 2, error: 3 }

  onMount(() => {
    const load = async () => { if (!paused) entries = await store.logs().catch(() => entries) }
    load()
    const t = setInterval(load, 2000)
    return () => clearInterval(t)
  })
  const shown = $derived(entries.filter((e) => (level === 'all' || rank[e.level] >= rank[level]) &&
    (!q || (e.msg + ' ' + (e.attrs ?? '')).toLowerCase().includes(q.toLowerCase()))).slice().reverse())
</script>

<div class="bar">
  <div class="search tile"><Search size={15} /><input placeholder="Filter…" bind:value={q} /></div>
  <Segmented size="sm" value={level} onchange={(v) => (level = v)}
    options={[{ value: 'all', label: 'All' }, { value: 'info', label: 'Info+' }, { value: 'warn', label: 'Warnings' }, { value: 'error', label: 'Errors' }]} />
  <div class="spacer"></div>
  <span class="muted">{shown.length} lines</span>
  <button class="btn sm" onclick={() => (paused = !paused)}>{#if paused}<Play size={14} />Resume{:else}<Pause size={14} />Pause{/if}</button>
</div>

<section class="card log">
  {#each shown as e, i (e.t + ':' + i)}
    <div class="ln {e.level}">
      <span class="t mono">{new Date(e.t).toLocaleTimeString()}</span>
      <span class="lv">{e.level}</span>
      <span class="m">{e.msg}</span>
      <span class="a mono">{e.attrs}</span>
    </div>
  {:else}
    <p class="muted">Nothing logged yet.</p>
  {/each}
</section>

<style>
  .bar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .search { display: flex; align-items: center; gap: 8px; padding: 0 10px; height: 34px; width: min(300px, 100%); color: var(--muted); background: var(--bg); }
  .search input { border: 0; outline: 0; background: transparent; flex: 1; }
  .spacer { flex: 1; }
  .log { padding: 6px 0; font-size: 12.5px; max-height: calc(100vh - 170px); overflow: auto; }
  .ln { display: grid; grid-template-columns: 82px 56px minmax(180px, auto) 1fr; gap: 10px; padding: 5px 16px; border-bottom: 1px solid color-mix(in srgb, var(--line) 50%, transparent); align-items: baseline; }
  .ln:hover { background: var(--panel-2); }
  .t { color: var(--faint); font-size: 11.5px; }
  .lv { font-size: 10.5px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; color: var(--muted); }
  .warn .lv { color: var(--warn); }
  .error .lv { color: var(--bad); }
  .debug .lv { color: var(--faint); }
  .a { color: var(--muted); font-size: 11.5px; word-break: break-all; }
  p { padding: 16px; margin: 0; }
  @media (max-width: 700px) { .ln { grid-template-columns: 70px 1fr; } .lv, .a { display: none; } }
</style>
