<script lang="ts">
  import { Search, Eye, EyeOff, RotateCcw, Check, LoaderCircle } from '@lucide/svelte'
  import type { Field } from '../lib/types'
  import { store } from '../lib/store.svelte'
  import Toggle from '../components/Toggle.svelte'
  import Segmented from '../components/Segmented.svelte'

  let draft = $state<any>($state.snapshot(store.config))
  // What this page started from: only fields changed relative to it are saved.
  let baseline: any = $state.snapshot(store.config)
  let advanced = $state(false)
  let q = $state('')
  let error = $state('')
  let saving = $state<'idle' | 'saving' | 'saved'>('idle')
  let reveal = $state<Record<string, boolean>>({})
  let timer: ReturnType<typeof setTimeout> | undefined

  const get = (o: any, k: string) => k.split('.').reduce((a, p) => a?.[p], o)
  function set(k: string, v: unknown) {
    const parts = k.split('.')
    let o = draft
    for (const p of parts.slice(0, -1)) o = o[p] ??= {}
    o[parts[parts.length - 1]] = v
    schedule()
  }
  function schedule() {
    clearTimeout(timer)
    saving = 'saving'
    timer = setTimeout(save, 650)
  }
  async function save() {
    const err = await store.saveConfig($state.snapshot(draft), baseline)
    if (err) {
      error = err
      saving = 'idle'
      store.toast('Not saved: ' + err, 'err')
    } else {
      error = ''
      saving = 'saved'
      draft = $state.snapshot(store.config)
      baseline = $state.snapshot(store.config)
      setTimeout(() => saving === 'saved' && (saving = 'idle'), 1800)
    }
  }
  function reset() {
    clearTimeout(timer)
    draft = $state.snapshot(store.config)
    baseline = $state.snapshot(store.config)
    error = ''
    saving = 'idle'
  }

  const match = (f: Field) => !q || (f.label + ' ' + (f.help ?? '') + ' ' + f.key).toLowerCase().includes(q.toLowerCase())
  const sections = $derived(store.schema.map((s) => ({
    ...s, fields: s.fields.filter((f) => (advanced || !f.advanced || q) && match(f)),
  })).filter((s) => s.fields.length))
  const hiddenCount = $derived(store.schema.reduce((a, s) => a + s.fields.filter((f) => f.advanced).length, 0))
  const errFor = (k: string) => error.split('\n').find((l) => l.startsWith(k + ':'))?.slice(k.length + 1).trim()
</script>

<div class="bar">
  <div class="search tile"><Search size={15} /><input placeholder="Find a setting…" bind:value={q} /></div>
  <label class="adv"><Toggle bind:checked={advanced} label="Show advanced" /><span>Show advanced <span class="faint">({hiddenCount})</span></span></label>
  <div class="spacer"></div>
  <span class="state {saving}">
    {#if saving === 'saving'}<LoaderCircle size={14} class="spin" />Applying…{:else if saving === 'saved'}<Check size={14} />Saved · applied live{/if}
  </span>
  <button class="btn sm ghost" onclick={reset} title="Discard unsaved edits"><RotateCcw size={14} />Revert</button>
</div>
{#if error}<div class="error">{error}</div>{/if}

<div class="grid">
  {#each sections as s (s.key)}
    <section class="card sec">
      <header>
        <h3>{s.label}</h3>
        {#if s.help}<p>{s.help}</p>{/if}
      </header>
      {#each s.fields as f (f.key)}
        {@const v = get(draft, f.key)}
        {@const fe = errFor(f.key)}
        <div class="field" class:wide={f.type === 'enum' && (f.options?.length ?? 0) > 3}>
          <div class="lbl">
            <b>{f.label}{#if f.advanced}<span class="chip">advanced</span>{/if}</b>
            {#if f.help}<span>{f.help}</span>{/if}
            {#if fe}<span class="fe">{fe}</span>{/if}
          </div>
          <div class="ctl">
            {#if f.type === 'bool'}
              <Toggle checked={!!v} label={f.label} onchange={(x) => set(f.key, x)} />
            {:else if f.type === 'enum'}
              {#if (f.options?.length ?? 0) <= 4}
                <Segmented size="sm" value={v} options={(f.options ?? []).map((o) => ({ value: o.value, label: o.label.replace(/ \(.*\)$/, '') }))} onchange={(x) => set(f.key, x)} />
              {:else}
                <select class="input" value={v} onchange={(e) => set(f.key, (e.target as HTMLSelectElement).value)}>
                  {#each f.options ?? [] as o}<option value={o.value}>{o.label}</option>{/each}
                </select>
              {/if}
            {:else if f.type === 'int' || f.type === 'float'}
              <div class="numrow">
                {#if f.min !== undefined && f.max !== undefined && f.max - f.min <= 20000}
                  <input type="range" min={f.min} max={f.max} step={f.step ?? 1} value={v}
                    oninput={(e) => set(f.key, Number((e.target as HTMLInputElement).value))} />
                {/if}
                <div class="numbox">
                  <input class="input num" class:err={!!fe} type="number" min={f.min} max={f.max} step={f.step ?? 1} value={v}
                    oninput={(e) => { const n = (e.target as HTMLInputElement).valueAsNumber; if (!isNaN(n)) set(f.key, n) }} />
                  {#if f.unit}<span class="unit">{f.unit}</span>{/if}
                </div>
              </div>
            {:else}
              <div class="txt">
                <input class="input" class:err={!!fe} type={f.secret && !reveal[f.key] ? 'password' : 'text'} value={v ?? ''}
                  autocomplete="off" spellcheck="false" oninput={(e) => set(f.key, (e.target as HTMLInputElement).value)} />
                {#if f.secret}
                  <button class="btn icon sm ghost" onclick={() => (reveal[f.key] = !reveal[f.key])} aria-label="Show">
                    {#if reveal[f.key]}<EyeOff size={14} />{:else}<Eye size={14} />{/if}
                  </button>
                {/if}
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </section>
  {/each}
  {#if !sections.length}<p class="muted">No settings match “{q}”.</p>{/if}
</div>

<style>
  .bar { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; position: sticky; top: 58px; z-index: 5; padding: 10px 0; background: color-mix(in srgb, var(--bg) 85%, transparent); backdrop-filter: blur(8px); }
  .search { display: flex; align-items: center; gap: 8px; padding: 0 10px; height: 34px; width: min(320px, 100%); color: var(--muted); background: var(--bg); }
  .search input { border: 0; outline: 0; background: transparent; flex: 1; }
  .adv { display: inline-flex; align-items: center; gap: 9px; font-size: 13px; cursor: pointer; }
  .spacer { flex: 1; }
  .state { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--muted); }
  .state.saved { color: var(--ok); }
  .state :global(.spin) { animation: spin 0.9s linear infinite; }
  .error { white-space: pre-line; color: var(--bad); background: var(--bad-bg); border-radius: 10px; padding: 10px 14px; font-size: 12.5px; }
  .grid { columns: 2 520px; column-gap: 14px; }
  .sec { break-inside: avoid; margin-bottom: 14px; padding: 6px 18px 10px; display: block; }
  header { padding: 12px 0 8px; }
  h3 { margin: 0; font-size: 15px; font-weight: 680; }
  header p { margin: 2px 0 0; font-size: 12.5px; color: var(--muted); }
  .field { display: grid; grid-template-columns: 1fr minmax(180px, 46%); gap: 16px; align-items: center; padding: 11px 0; border-top: 1px solid var(--line); }
  .field.wide { grid-template-columns: 1fr; gap: 8px; }
  .lbl { display: grid; gap: 2px; min-width: 0; }
  .lbl b { font-size: 13.5px; font-weight: 600; display: flex; align-items: center; gap: 8px; }
  .lbl b .chip { height: 18px; font-size: 10px; }
  .lbl span { font-size: 12px; color: var(--muted); }
  .lbl .fe { color: var(--bad); }
  .ctl { display: flex; justify-content: flex-end; min-width: 0; }
  .field.wide .ctl { justify-content: flex-start; }
  .numrow { display: flex; align-items: center; gap: 10px; width: 100%; }
  .numbox { position: relative; flex: none; width: 120px; }
  .numbox .input { padding-right: 46px; }
  .unit { position: absolute; right: 10px; top: 50%; transform: translateY(-50%); font-size: 11px; color: var(--faint); pointer-events: none; }
  .txt { display: flex; gap: 4px; width: 100%; }
  @media (max-width: 640px) { .field { grid-template-columns: 1fr; gap: 8px; } .ctl { justify-content: flex-start; } }
</style>
