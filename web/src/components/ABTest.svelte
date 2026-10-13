<script lang="ts">
  import { onMount } from 'svelte'
  import { FlaskConical, Play, LoaderCircle } from '@lucide/svelte'
  import { api, store } from '../lib/store.svelte'

  interface Round { path: string; via: string; mbps: number; connectMs: number; ttfbMs: number; error?: string }
  interface Result {
    running: boolean; started?: number; finished?: number; url: string; rounds: Round[]
    bonded: { mbps: number; connectMs: number; ok: number }; direct: { mbps: number; connectMs: number; ok: number }
    verdict?: string; directVia?: string
  }
  let res = $state<Result | null>(null)
  let err = $state('')
  let rounds = $state(3)
  let url = $state('')
  let timer: ReturnType<typeof setInterval> | undefined

  async function poll() {
    try { res = await api('GET', '/api/x/abtest') } catch {}
    if (res && !res.running && timer) { clearInterval(timer); timer = undefined }
  }
  async function start() {
    err = ''
    try {
      res = await api('POST', '/api/x/abtest', { rounds, url: url.trim() })
      timer ??= setInterval(poll, 1000)
    } catch (e: any) { err = e.message }
  }
  onMount(() => { poll().then(() => { if (res?.running) timer = setInterval(poll, 1000) }); return () => timer && clearInterval(timer) })

  const maxMbps = $derived(Math.max(1, ...(res?.rounds ?? []).map((r) => r.mbps)))
  const bars = $derived.by(() => {
    const rs = res?.rounds ?? []
    const out: { n: number; direct?: Round; bonded?: Round }[] = []
    for (const r of rs) {
      let last = out[out.length - 1]
      if (!last || last[r.path as 'direct' | 'bonded']) { last = { n: out.length + 1 }; out.push(last) }
      last[r.path as 'direct' | 'bonded'] = r
    }
    return out
  })
</script>

<section class="card ab">
  <div class="hd">
    <div class="ic"><FlaskConical size={18} /></div>
    <div class="ttl">
      <h3>Bond vs direct</h3>
      <span class="muted">Downloads the same file through the bond and straight over {res?.directVia ?? 'your best link'}, alternating, so you can see which is faster right now.</span>
    </div>
    <label class="r">Rounds <input class="input num" type="number" min="1" max="5" bind:value={rounds} style="width:64px" /></label>
    <button class="btn primary" onclick={start} disabled={res?.running}>
      {#if res?.running}<LoaderCircle size={15} class="spin" />Testing…{:else}<Play size={15} />Run test{/if}
    </button>
  </div>
  <details class="adv"><summary>Test URL</summary>
    <input class="input mono" placeholder="https://speed.cloudflare.com/__down?bytes=10000000" bind:value={url} />
  </details>
  {#if err}<div class="err">{err}</div>{/if}

  {#if res && res.rounds.length}
    <div class="rows">
      {#each bars as b (b.n)}
        <div class="round">
          <span class="rn muted">#{b.n}</span>
          {#each [['direct', b.direct, 'var(--cyan)'], ['bonded', b.bonded, 'var(--accent-2)']] as [label, r, color]}
            {@const rr = r as Round | undefined}
            <div class="line">
              <span class="lbl">{label}</span>
              <span class="track"><i style="width:{rr && !rr.error ? (rr.mbps / maxMbps) * 100 : 0}%;background:{color}"></i></span>
              <span class="val num">{rr ? (rr.error ? 'failed' : `${rr.mbps.toFixed(1)} Mbit/s · ${rr.connectMs.toFixed(0)} ms`) : '…'}</span>
            </div>
          {/each}
        </div>
      {/each}
    </div>
    {#if !res.running && res.verdict}
      <div class="verdict">
        <div><span class="muted">Direct avg</span><b class="num">{res.direct.mbps.toFixed(1)} Mbit/s</b><span class="muted">{res.direct.connectMs.toFixed(0)} ms</span></div>
        <div><span class="muted">Bonded avg</span><b class="num">{res.bonded.mbps.toFixed(1)} Mbit/s</b><span class="muted">{res.bonded.connectMs.toFixed(0)} ms</span></div>
        <p>{res.verdict}</p>
      </div>
    {/if}
  {/if}
</section>

<style>
  .ab { padding: 16px 18px; display: grid; gap: 12px; }
  .hd { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; }
  .ic { width: 36px; height: 36px; border-radius: 10px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); flex: none; }
  .ttl { flex: 1; min-width: 240px; display: grid; }
  h3 { margin: 0; font-size: 15px; font-weight: 680; }
  .ttl span { font-size: 12px; }
  .r { display: inline-flex; align-items: center; gap: 8px; font-size: 12.5px; color: var(--muted); }
  .adv summary { cursor: pointer; font-size: 12px; color: var(--muted); }
  .adv .input { margin-top: 6px; }
  .err { color: var(--bad); background: var(--bad-bg); border-radius: 10px; padding: 8px 12px; font-size: 12.5px; }
  .rows { display: grid; gap: 8px; }
  .round { display: grid; grid-template-columns: 28px 1fr; gap: 4px 10px; align-items: center; }
  .rn { grid-row: span 2; font-size: 12px; }
  .line { display: grid; grid-template-columns: 56px 1fr 170px; gap: 10px; align-items: center; font-size: 12px; }
  .lbl { color: var(--muted); }
  .track { height: 8px; border-radius: 6px; background: var(--line); overflow: hidden; }
  .track i { display: block; height: 100%; border-radius: 6px; transition: width 0.4s var(--ease-out); }
  .val { text-align: right; color: var(--text-2); }
  .verdict { display: flex; gap: 22px; align-items: center; flex-wrap: wrap; padding: 12px 14px; border-radius: 12px; background: var(--bg-2); border: 1px solid var(--line); }
  .verdict div { display: grid; }
  .verdict b { font-size: 18px; font-weight: 720; }
  .verdict p { margin: 0; flex: 1; min-width: 220px; font-weight: 600; }
  :global(.spin) { animation: spin 0.9s linear infinite; }
  @media (max-width: 600px) { .line { grid-template-columns: 50px 1fr; } .val { grid-column: 1 / -1; text-align: left; } }
</style>
