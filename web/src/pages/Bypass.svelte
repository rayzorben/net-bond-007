<script lang="ts">
  import { onMount } from 'svelte'
  import { Plus, Pencil, Trash2, Split, TriangleAlert, ChevronDown, ChevronUp } from '@lucide/svelte'
  import Toggle from '../components/Toggle.svelte'
  import { store, api, ApiError } from '../lib/store.svelte'

  interface Rule { host: string; name?: string; disabled?: boolean }
  interface RuleView extends Rule { addrs: number; seen?: number }
  interface Active { ip: string; host: string; how: string; seen: number }
  interface View { rules: RuleView[]; version: string; via?: string; supported: boolean; problem?: string; active: Active[] }

  let v = $state<View | null>(null)
  let err = $state('')
  let busy = $state(false)
  let showActive = $state(false)
  // Editing: index into the list, or -1 for a new entry.
  let editing = $state<number | null>(null)
  let draft = $state<Rule>({ host: '', name: '' })
  // The list the open form edits, as loaded when the form opened; the server
  // refuses the save (409) if the list changed since.
  let base: { list: Rule[]; version: string; orig?: Rule } | null = null

  async function load() {
    try {
      v = await api('GET', '/api/x/bypass')
      err = ''
    } catch (e: any) {
      err = e.message
    }
  }
  onMount(() => { load(); const t = setInterval(load, 3000); return () => clearInterval(t) })

  function plain(r: Rule): Rule {
    const o: Rule = { host: r.host.trim().toLowerCase().replace(/\.$/, '') }
    if (r.name?.trim()) o.name = r.name.trim()
    if (r.disabled) o.disabled = true
    return o
  }
  async function save(list: Rule[], version: string, ok: string) {
    busy = true
    try {
      v = await api('PUT', '/api/x/bypass', list.map(plain), { 'If-Match': `"${version}"` })
      store.toast(ok)
      editing = null
      base = null
      return true
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 409 && e.data?.current) {
        v = e.data.current
        rebase()
      }
      store.toast(e.message, 'err')
      return false
    } finally {
      busy = false
    }
  }
  const current = () => (v?.rules ?? []).map(plain)
  const same = (a: Rule, b: Rule) => JSON.stringify(plain(a)) === JSON.stringify(plain(b))
  function rebase() {
    if (editing === null || !v) return
    if (editing === -1) {
      base = { list: current(), version: v.version }
      return
    }
    const i = v.rules.findIndex((r) => base?.orig && same(r, base.orig))
    if (i < 0) {
      editing = null
      base = null
      return
    }
    editing = i
    base = { list: current(), version: v.version, orig: base!.orig }
  }
  function startAdd() {
    draft = { host: '', name: '' }
    base = { list: current(), version: v?.version ?? '' }
    editing = -1
  }
  function startEdit(i: number) {
    const r = v!.rules[i]
    draft = { host: r.host, name: r.name ?? '', disabled: r.disabled }
    base = { list: current(), version: v!.version, orig: plain(r) }
    editing = i
  }
  /** Split "a.com, *.b.net" pasted into the host box into one entry each. */
  function hosts(s: string): string[] {
    return [...new Set(s.split(/[\s,;]+/).map((h) => h.trim().toLowerCase().replace(/\.$/, '')).filter(Boolean))]
  }
  async function submit(e: Event) {
    e.preventDefault()
    if (!base) return
    const list = [...base.list]
    const hs = hosts(draft.host)
    if (editing === -1) {
      const have = new Set(list.map((r) => r.host))
      for (const h of hs) if (!have.has(h)) list.push({ host: h, name: hs.length === 1 ? draft.name : '' })
      await save(list, base.version, hs.length === 1 ? `${hs[0]} bypasses the bond` : `${hs.length} hosts bypass the bond`)
    } else if (editing !== null) {
      list[editing] = { ...draft, host: hs[0] ?? '' }
      await save(list, base.version, 'Entry updated')
    }
  }
  async function remove(i: number) {
    const r = v!.rules[i]
    if (!confirm(`Send ${r.host} through the bond again?`)) return
    const version = v!.version
    const list = current()
    list.splice(i, 1)
    await save(list, version, `${r.host} removed`)
  }
  async function setEnabled(i: number, on: boolean) {
    const version = v!.version
    const list = current()
    list[i].disabled = !on || undefined
    await save(list, version, on ? `${list[i].host} bypasses the bond` : `${list[i].host} goes through the bond`)
  }
  const isLiteral = (h: string) => /^[0-9.]+(\/\d+)?$/.test(h)
  const kind = (h: string) => (isLiteral(h) ? (h.includes('/') ? 'Address range' : 'Address') : h.startsWith('*.') ? 'Names under it' : 'Name and everything under it')
  const ago = (ms?: number) => {
    if (!ms) return ''
    const s = Math.max(0, (Date.now() - ms) / 1000)
    return s < 60 ? 'just now' : s < 3600 ? `${Math.floor(s / 60)} min ago` : s < 86400 ? `${Math.floor(s / 3600)} h ago` : `${Math.floor(s / 86400)} d ago`
  }
  const total = $derived(v?.active.length ?? 0)
</script>

<section class="card head">
  <div class="ic"><Split size={20} /></div>
  <div class="ttl">
    <b>Bypass the bond</b>
    <span>Sites listed here are always reached straight out of your best connection, never through the server. Use it for sites that block or challenge data-center addresses (ticket sellers and their bot checks). Add the site and the hosts its pages load.</span>
  </div>
  <div class="facts">
    <div><span>Going out via</span><b class="num">{v?.via || '—'}</b></div>
    <div><span>Addresses routed</span><b class="num">{v ? total : '—'}</b></div>
  </div>
  <button class="btn primary" onclick={startAdd} disabled={!v || editing !== null}><Plus size={15} />Add host</button>
</section>

{#if err}<div class="banner bad">{err}</div>{/if}
{#if v?.problem}<div class="banner warn"><TriangleAlert size={15} />{v.problem}</div>{/if}

{#if editing !== null}
  <form class="card form" onsubmit={submit}>
    <h3>{editing === -1 ? 'New bypass' : 'Edit bypass'}</h3>
    <div class="grid">
      <label class="wide">
        <span>Host</span>
        <input class="input num" placeholder="seatgeek.com, *.px-cloud.net, 203.0.113.0/24" bind:value={draft.host} required autocomplete="off" spellcheck="false" />
        <small>A name covers itself and everything under it (<b class="num">seatgeek.com</b> includes <b class="num">api.seatgeek.com</b>). <b class="num">*.px-cloud.net</b> covers only names under it. Addresses and CIDRs work too.{#if editing === -1} Paste several, separated by commas.{/if}</small>
      </label>
      <label>
        <span>Note</span>
        <input class="input" placeholder="e.g. StubHub bot check" bind:value={draft.name} />
      </label>
    </div>
    <p class="hint">
      Name lookups are read as they come back through the tunnel, so the first connection after a lookup already goes direct. If a site still complains, bypass the host its challenge script loads from too.
    </p>
    <div class="actions">
      <button type="button" class="btn" onclick={() => { editing = null; base = null }}>Cancel</button>
      <button class="btn primary" disabled={busy}>{editing === -1 ? 'Add' : 'Save'}</button>
    </div>
  </form>
{/if}

{#if v && v.rules.length}
  <section class="card list">
    {#each v.rules as r, i (i)}
      <div class="row" class:off={r.disabled}>
        <Toggle checked={!r.disabled} label="Enabled" onchange={(on) => setEnabled(i, on)} disabled={busy} />
        <div class="name">
          <b class="num">{r.host}</b>
          <span>{kind(r.host)}{#if r.name} · {r.name}{/if}</span>
        </div>
        <div class="stat">
          {#if r.disabled}
            <span class="chip">Off</span>
          {:else if r.addrs}
            <span class="chip ok">{r.addrs} address{r.addrs === 1 ? '' : 'es'}</span>
            {#if r.seen}<span class="when">seen {ago(r.seen)}</span>{/if}
          {:else}
            <span class="chip warn" title="No lookup for this name has been seen yet. Open the site once; its addresses are routed as the answer arrives.">Waiting for a lookup</span>
          {/if}
        </div>
        <div class="tools">
          <button class="btn icon sm ghost" title="Edit" onclick={() => startEdit(i)} disabled={editing !== null}><Pencil size={14} /></button>
          <button class="btn icon sm ghost" title="Delete" onclick={() => remove(i)} disabled={busy}><Trash2 size={14} /></button>
        </div>
      </div>
    {/each}
  </section>
  {#if total}
    <section class="card active">
      <button class="hdr" onclick={() => (showActive = !showActive)}>
        <b>Addresses going direct right now</b><span>{total}</span>
        {#if showActive}<ChevronUp size={15} />{:else}<ChevronDown size={15} />{/if}
      </button>
      {#if showActive}
        <div class="addrs">
          {#each v.active as a (a.ip)}
            <div class="addr">
              <span class="num ip">{a.ip}</span>
              <span class="num host">{a.host}</span>
              <span class="how">{a.how === 'dns' ? 'from a lookup' : 'looked up'} · {ago(a.seen)}</span>
            </div>
          {/each}
        </div>
      {/if}
    </section>
  {/if}
{:else if v && editing === null}
  <section class="card empty">Nothing bypasses the bond. Add a site that blocks the server's address and it will always go out directly.</section>
{/if}

<style>
  .head { display: flex; align-items: center; gap: 18px; flex-wrap: wrap; padding: 14px 16px; }
  .ic { width: 40px; height: 40px; border-radius: 12px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); flex: none; }
  .ttl { display: grid; gap: 3px; flex: 1; min-width: 240px; }
  .ttl b { font-size: 15px; font-weight: 650; }
  .ttl span { font-size: 12.5px; color: var(--muted); max-width: 70ch; }
  .facts { display: flex; gap: 22px; }
  .facts div { display: grid; gap: 2px; }
  .facts span, label > span { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); font-weight: 650; }
  .facts b { font-size: 15px; font-weight: 700; }
  .banner { display: flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: 10px; font-size: 13px; }
  .banner.bad { color: var(--bad); background: var(--bad-bg); }
  .banner.warn { color: var(--warn); background: var(--warn-bg); }
  .form { padding: 16px; display: grid; gap: 14px; }
  .form h3 { margin: 0; font-size: 14px; }
  .grid { display: grid; grid-template-columns: 2fr 1fr; gap: 14px; }
  label { display: grid; gap: 6px; align-content: start; }
  label small { font-size: 11.5px; color: var(--muted); }
  label small b { color: var(--text); font-weight: 600; }
  .hint { margin: 0; font-size: 12.5px; color: var(--muted); }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
  .list { padding: 6px 0; }
  .row { display: flex; align-items: center; gap: 14px; padding: 10px 16px; flex-wrap: wrap; }
  .row + .row { border-top: 1px solid var(--line); }
  .row.off .name { opacity: 0.5; }
  .name { display: grid; flex: 1; min-width: 160px; }
  .name b { font-size: 13.5px; }
  .name span { font-size: 11.5px; color: var(--muted); }
  .stat { display: flex; align-items: center; gap: 8px; }
  .when { font-size: 11.5px; color: var(--muted); }
  .tools { display: flex; gap: 2px; }
  .empty { padding: 28px; text-align: center; color: var(--muted); }
  .active { padding: 0; }
  .hdr { display: flex; align-items: center; gap: 10px; width: 100%; padding: 12px 16px; background: none; border: 0; color: var(--text); font-size: 13px; cursor: pointer; text-align: left; }
  .hdr span { color: var(--muted); font-family: var(--mono); flex: 1; }
  .addrs { border-top: 1px solid var(--line); padding: 6px 0; max-height: 420px; overflow: auto; }
  .addr { display: grid; grid-template-columns: 150px 1fr auto; gap: 12px; padding: 5px 16px; font-size: 12.5px; }
  .addr .host { color: var(--text-2); }
  .addr .how { color: var(--muted); font-size: 11.5px; }
  @media (max-width: 600px) { .grid { grid-template-columns: 1fr; } .facts { width: 100%; } .addr { grid-template-columns: 1fr; gap: 0; } }
</style>
