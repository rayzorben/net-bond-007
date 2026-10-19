<script lang="ts">
  import { onMount } from 'svelte'
  import { Plus, Pencil, Trash2, Copy, Globe, ArrowRight, TriangleAlert } from '@lucide/svelte'
  import Toggle from '../components/Toggle.svelte'
  import Segmented from '../components/Segmented.svelte'
  import { store, api, ApiError } from '../lib/store.svelte'

  interface Forward { name?: string; proto: 'tcp' | 'udp' | 'both'; port: number; device: string; devicePort?: number; disabled?: boolean }
  interface ForwardView extends Forward { public: string; state: string; error?: string }
  interface Device { ip: string; name: string; mac?: string }
  interface View {
    forwards: ForwardView[]; version: string; serverPorts: string; serverEnabled: boolean; serverAnswer: boolean
    publicHost: string; supported: boolean; problem?: string; devices: Device[]
  }

  let v = $state<View | null>(null)
  let err = $state('')
  let busy = $state(false)
  // Editing: index into the list, or -1 for a new forward.
  let editing = $state<number | null>(null)
  let draft = $state<Forward>({ proto: 'both', port: 0, device: '' })
  let manualIP = $state(false)
  // The list the open form edits, as loaded when the form opened. The 3 s
  // refresh can reorder rows under the form; saving applies the edit to this
  // copy and the server refuses it (409) if the list changed since.
  let base: { list: Forward[]; version: string; orig?: Forward } | null = null

  async function load() {
    try {
      v = await api('GET', '/api/x/forwards')
      err = ''
    } catch (e: any) {
      err = e.message
    }
  }
  onMount(() => { load(); const t = setInterval(load, 3000); return () => clearInterval(t) })

  function plain(f: Forward): Forward {
    const o: Forward = { proto: f.proto, port: Number(f.port), device: f.device.trim() }
    if (f.name?.trim()) o.name = f.name.trim()
    if (f.devicePort && Number(f.devicePort) !== Number(f.port)) o.devicePort = Number(f.devicePort)
    if (f.disabled) o.disabled = true
    return o
  }
  async function save(list: Forward[], version: string, ok: string) {
    busy = true
    try {
      v = await api('PUT', '/api/x/forwards', list.map(plain), { 'If-Match': `"${version}"` })
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
  const current = () => (v?.forwards ?? []).map(plain)
  const same = (a: Forward, b: Forward) => JSON.stringify(plain(a)) === JSON.stringify(plain(b))
  /** After a conflict: point the open form at the fresh list, keeping the draft. */
  function rebase() {
    if (editing === null || !v) return
    if (editing === -1) {
      base = { list: current(), version: v.version }
      return
    }
    const i = v.forwards.findIndex((f) => base?.orig && same(f, base.orig))
    if (i < 0) {
      editing = null
      base = null
      return
    }
    editing = i
    base = { list: current(), version: v.version, orig: base!.orig }
  }

  /** Ports the server allows, expanded (capped: ranges can be large). */
  function allowed(spec: string): number[] {
    const out: number[] = []
    for (const part of spec.split(',')) {
      const [a, b] = part.trim().split('-').map((x) => parseInt(x, 10))
      if (!a) continue
      for (let p = a; p <= (b || a) && out.length < 5000; p++) out.push(p)
    }
    return out
  }
  function freePort(): number {
    const used = new Set((v?.forwards ?? []).map((f) => f.port))
    // Before the server answers, suggest from its default range.
    return allowed(v?.serverPorts || '40000-40019').find((p) => !used.has(p)) ?? 0
  }
  function startAdd() {
    const port = freePort()
    draft = { name: '', proto: 'both', port, device: v?.devices[0]?.ip ?? '', devicePort: undefined }
    manualIP = !v?.devices.length
    base = { list: current(), version: v?.version ?? '' }
    editing = -1
  }
  function startEdit(i: number) {
    const f = v!.forwards[i]
    draft = { name: f.name ?? '', proto: f.proto, port: f.port, device: f.device, devicePort: f.devicePort, disabled: f.disabled }
    manualIP = !v!.devices.some((d) => d.ip === f.device)
    base = { list: current(), version: v!.version, orig: plain(f) }
    editing = i
  }
  async function submit(e: Event) {
    e.preventDefault()
    if (!base) return
    const list = [...base.list]
    if (editing === -1) list.push(draft)
    else if (editing !== null) list[editing] = { ...draft }
    await save(list, base.version, editing === -1 ? `Port ${draft.port} forwarded` : 'Forward updated')
  }
  async function remove(i: number) {
    const f = v!.forwards[i]
    if (!confirm(`Stop forwarding port ${f.port}?`)) return
    const version = v!.version
    const list = current()
    list.splice(i, 1)
    await save(list, version, `Port ${f.port} closed`)
  }
  async function setEnabled(i: number, on: boolean) {
    const version = v!.version
    const list = current()
    list[i].disabled = !on || undefined
    await save(list, version, on ? `Port ${list[i].port} on` : `Port ${list[i].port} off`)
  }
  function copy(s: string) {
    navigator.clipboard?.writeText(s).then(() => store.toast(`Copied ${s}`, 'info'), () => {})
  }
  const devName = (ip: string) => v?.devices.find((d) => d.ip === ip)?.name || ip
  const protoLabel = (p: string) => (p === 'both' ? 'TCP + UDP' : p.toUpperCase())
  const stateChip: Record<string, [string, string]> = {
    open: ['ok', 'Open'],
    waiting: ['warn', 'Waiting'],
    off: ['', 'Off'],
    taken: ['bad', 'Taken'],
    outside: ['bad', 'Port not allowed'],
    error: ['bad', 'Error'],
  }
  const stateHint = (f: ForwardView) =>
    f.error || (f.state === 'outside' ? `The server only opens ports ${v?.serverPorts}.` : f.state === 'waiting' ? 'Waiting for the server to confirm.' : '')
  const inRange = $derived(!v?.serverPorts || !draft.port || allowed(v.serverPorts).includes(Number(draft.port)))
</script>

<section class="card head">
  <div class="ic"><Globe size={20} /></div>
  <div class="ttl">
    <b>Port forwarding</b>
    <span>Open a port on the server's public address and send what arrives to a device on your network, like your router's port forwarding. Works on Starlink and other CGNAT links, because connections come in through the bond.</span>
  </div>
  <div class="facts">
    <div><span>Public address</span><b class="num">{v?.publicHost || '—'}</b></div>
    <div><span>Ports you can use</span><b class="num">{v?.serverAnswer ? v.serverPorts || 'none' : '—'}</b></div>
  </div>
  <button class="btn primary" onclick={startAdd} disabled={!v || editing !== null}><Plus size={15} />Add forward</button>
</section>

{#if err}<div class="banner bad">{err}</div>{/if}
{#if v?.problem}<div class="banner warn"><TriangleAlert size={15} />{v.problem}</div>{/if}

{#if editing !== null}
  <form class="card form" onsubmit={submit}>
    <h3>{editing === -1 ? 'New port forward' : 'Edit port forward'}</h3>
    <div class="grid">
      <label>
        <span>Name</span>
        <input class="input" placeholder="e.g. qBittorrent" bind:value={draft.name} />
      </label>
      <label>
        <span>Device</span>
        {#if manualIP}
          <input class="input num" placeholder="192.168.8.x" bind:value={draft.device} required />
        {:else}
          <select class="input" bind:value={draft.device} required>
            {#each v?.devices ?? [] as d (d.ip)}<option value={d.ip}>{d.name && d.name !== '*' && d.name !== d.ip ? `${d.name} — ${d.ip}` : d.ip}</option>{/each}
          </select>
        {/if}
        <button type="button" class="link" onclick={() => (manualIP = !manualIP)}>{manualIP ? 'Pick from the network' : 'Enter an address'}</button>
      </label>
      <label>
        <span>Protocol</span>
        <Segmented size="sm" value={draft.proto} onchange={(x: string) => (draft.proto = x as Forward['proto'])}
          options={[{ value: 'both', label: 'TCP + UDP' }, { value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]} />
      </label>
      <label>
        <span>Public port</span>
        <input class="input num" class:err={!inRange} type="number" min="1" max="65535" bind:value={draft.port} required />
        {#if !inRange}<small class="bad">The server only opens {v?.serverPorts}.</small>{/if}
      </label>
      <label>
        <span>Port on the device</span>
        <input class="input num" type="number" min="1" max="65535" placeholder={`${draft.port || ''} (same)`} bind:value={draft.devicePort} />
      </label>
    </div>
    <p class="hint">
      Give out <b class="num">{v?.publicHost}:{draft.port || '…'}</b>. For BitTorrent and games, set the app to listen on the
      same port as the public one: they tell peers their own port number.
    </p>
    <div class="actions">
      <button type="button" class="btn" onclick={() => { editing = null; base = null }}>Cancel</button>
      <button class="btn primary" disabled={busy}>{editing === -1 ? 'Add' : 'Save'}</button>
    </div>
  </form>
{/if}

{#if v && v.forwards.length}
  <section class="card list">
    {#each v.forwards as f, i (i)}
      {@const chip = stateChip[f.state] ?? ['', f.state]}
      <div class="row" class:off={f.disabled}>
        <Toggle checked={!f.disabled} label="Enabled" onchange={(on) => setEnabled(i, on)} disabled={busy} />
        <div class="name">
          <b>{f.name || `Port ${f.port}`}</b>
          <span>{protoLabel(f.proto)}</span>
        </div>
        <button class="pub num" title="Copy" onclick={() => copy(f.public)}>{f.public}<Copy size={13} /></button>
        <ArrowRight size={15} class="muted" />
        <div class="dest">
          <b>{devName(f.device)}</b>
          <span class="num">{f.device}:{f.devicePort || f.port}</span>
        </div>
        <span class="chip {chip[0]}" title={stateHint(f)}>{chip[1]}</span>
        <div class="tools">
          <button class="btn icon sm ghost" title="Edit" onclick={() => startEdit(i)} disabled={editing !== null}><Pencil size={14} /></button>
          <button class="btn icon sm ghost" title="Delete" onclick={() => remove(i)} disabled={busy}><Trash2 size={14} /></button>
        </div>
      </div>
      {#if stateHint(f) && f.state !== 'waiting' && f.state !== 'open'}<div class="why">{stateHint(f)}</div>{/if}
    {/each}
  </section>
{:else if v && editing === null}
  <section class="card empty">No ports are forwarded. Add one to reach a device on your network from the internet.</section>
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
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 14px; }
  label { display: grid; gap: 6px; align-content: start; }
  small.bad { color: var(--bad); font-size: 11.5px; }
  .link { background: none; border: 0; padding: 0; color: var(--accent-2); font-size: 11.5px; cursor: pointer; justify-self: start; }
  .hint { margin: 0; font-size: 12.5px; color: var(--muted); }
  .hint b { color: var(--text); }
  .actions { display: flex; gap: 8px; justify-content: flex-end; }
  .list { padding: 6px 0; }
  .row { display: flex; align-items: center; gap: 14px; padding: 10px 16px; flex-wrap: wrap; }
  .row + .row { border-top: 1px solid var(--line); }
  .row.off .name, .row.off .pub, .row.off .dest { opacity: 0.5; }
  .name, .dest { display: grid; min-width: 120px; }
  .name { flex: 1; }
  .name span, .dest span { font-size: 11.5px; color: var(--muted); }
  .dest span { font-family: var(--mono); }
  .pub { display: inline-flex; align-items: center; gap: 7px; font-family: var(--mono); font-size: 13px; background: var(--panel-2); border: 1px solid var(--line); border-radius: 8px; padding: 5px 9px; color: var(--text); cursor: pointer; }
  .pub:hover { border-color: var(--line-2); }
  .tools { display: flex; gap: 2px; }
  .why { padding: 0 16px 10px 68px; font-size: 12px; color: var(--bad); }
  .empty { padding: 28px; text-align: center; color: var(--muted); }
  @media (max-width: 600px) { .why { padding-left: 16px; } .facts { width: 100%; } }
</style>
