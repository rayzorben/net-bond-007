<script lang="ts">
  import { Route, Play, LoaderCircle, TriangleAlert } from '@lucide/svelte'
  import { api } from '../lib/store.svelte'

  interface PathResult { link: string; kind: string; ip?: string; colo?: string; country?: string; ptr?: string; provider?: string; ms: number; error?: string }
  let rep = $state<{ results: PathResult[]; warnings?: string[] } | null>(null)
  let busy = $state(false)
  let err = $state('')

  async function run() {
    busy = true; err = ''
    try { rep = await api('POST', '/api/x/pathcheck') } catch (e: any) { err = e.message }
    busy = false
  }
</script>

<section class="card pc">
  <div class="hd">
    <div class="ic"><Route size={18} /></div>
    <div class="ttl">
      <h3>Where does each link really go?</h3>
      <span class="muted">Asks the internet, over each link separately, which public address and network the request came from. Two links on the same provider fail together.</span>
    </div>
    <button class="btn" onclick={run} disabled={busy}>
      {#if busy}<LoaderCircle size={15} class="spin" />Checking…{:else}<Play size={15} />Check paths{/if}
    </button>
  </div>
  {#if err}<div class="err">{err}</div>{/if}
  {#if rep}
    <div class="tbl">
      {#each rep.results as r (r.link)}
        <div class="r">
          <b>{r.link}</b>
          {#if r.error}
            <span class="bad">{r.error}</span>
          {:else}
            <span class="mono">{r.ip}</span>
            <span><b>{r.provider || 'unknown network'}</b>{#if r.ptr}<em class="muted"> · {r.ptr}</em>{/if}</span>
            <span class="muted num">{r.colo} {r.country} · {r.ms.toFixed(0)} ms</span>
          {/if}
        </div>
      {/each}
    </div>
    {#each rep.warnings ?? [] as w}
      <div class="warn"><TriangleAlert size={14} />{w}</div>
    {/each}
  {/if}
</section>

<style>
  .pc { padding: 16px 18px; display: grid; gap: 12px; }
  .hd { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .ic { width: 36px; height: 36px; border-radius: 10px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); flex: none; }
  .ttl { flex: 1; min-width: 240px; display: grid; }
  h3 { margin: 0; font-size: 15px; font-weight: 680; }
  .ttl span { font-size: 12px; }
  .tbl { display: grid; gap: 6px; }
  .r { display: grid; grid-template-columns: 70px 130px 1fr auto; gap: 10px; font-size: 12.5px; align-items: baseline; }
  .r > * { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bad { color: var(--bad); grid-column: span 3; }
  .err { color: var(--bad); background: var(--bad-bg); border-radius: 10px; padding: 8px 12px; font-size: 12.5px; }
  .warn { display: flex; gap: 8px; align-items: flex-start; color: var(--warn); font-size: 12.5px; }
  @media (max-width: 640px) { .r { grid-template-columns: 60px 1fr; } .r > :nth-child(n+3) { grid-column: 2; } }
</style>
