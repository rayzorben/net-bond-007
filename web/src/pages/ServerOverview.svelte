<script lang="ts">
  import { TriangleAlert, UserX, Terminal } from '@lucide/svelte'
  import type { ServerState } from '../lib/types'
  import { store, key, api } from '../lib/store.svelte'
  import { bps, bpsParts, duration, count, bytes } from '../lib/format'
  import BondMap, { type Lane } from '../components/BondMap.svelte'
  import StatCard from '../components/StatCard.svelte'
  import Sparkline from '../components/Sparkline.svelte'
  import TimeChart from '../components/TimeChart.svelte'
  import LinkCard from '../components/LinkCard.svelte'
  import LinkDrawer from '../components/LinkDrawer.svelte'

  let { st }: { st: ServerState } = $props()
  const totals = $derived(store.totals)
  const down = $derived(st.sessions.reduce((a, s) => a + s.engine.txBps, 0))
  const up = $derived(st.sessions.reduce((a, s) => a + s.engine.rxBps, 0))
  const [dv, du] = $derived(bpsParts(down))
  const [uv, uu] = $derived(bpsParts(up))
  const selected = $derived.by(() => {
    for (const s of st.sessions) for (const l of s.links ?? []) if (`${s.id}/${key(l)}` === store.selected) return l
    return undefined
  })
  const lanesFor = (s: ServerState['sessions'][number]): Lane[] => (s.links ?? []).map((l) => ({
    key: `${s.id}/${key(l)}`, name: l.name, label: l.label, kind: l.kind, state: l.state, backup: l.backup,
    rtt: l.rttMs, loss: l.lossPct, up: l.rxBps, down: l.txBps,
    winRatio: l.wins + l.dups ? l.wins / (l.wins + l.dups) : 0,
  }))
  async function kick(id: string, name: string) {
    if (!confirm(`Disconnect ${name}? It will reconnect automatically unless it is switched off.`)) return
    await api('POST', `/api/x/sessions/${id}/kick`)
    store.toast(`${name} disconnected`)
  }
  const host = location.hostname
  const speed = $derived(st.speedTest)
  const ago = (t: string) => duration((Date.now() - new Date(t).getTime()) / 1000)
  async function testNow() {
    await api('POST', '/api/x/speedtest')
    store.toast('Server speed test started')
  }
</script>

{#each st.warnings as w}
  <div class="banner"><TriangleAlert size={16} />{w}</div>
{/each}

<section class="stats">
  <StatCard title="Clients" value={String(st.sessions.length)} sub={`up ${duration(st.uptime)} · listening ${st.listen}`} hero />
  <StatCard title="To clients" value={dv} unit={du} sub="downstream, before redundancy">
    <Sparkline values={totals.slice(-90).map((p) => p.rx)} color="var(--cyan)" height={34} />
  </StatCard>
  <StatCard title="From clients" value={uv} unit={uu} sub="upstream, de-duplicated">
    <Sparkline values={totals.slice(-90).map((p) => p.tx)} color="var(--accent-2)" height={34} />
  </StatCard>
  <StatCard title="Internet connections" value={count((st.egress.tcpActive ?? 0) + (st.egress.udpActive ?? 0))}
    sub={`${count(st.egress.tcpActive ?? 0)} TCP · ${count(st.egress.udpActive ?? 0)} UDP · ${count(st.egress.blocked ?? 0)} blocked · ${bytes((st.egress.bytesDown ?? 0) + (st.egress.bytesUp ?? 0))} total`} />
  {#if speed?.enabled}
    <StatCard title="Server internet"
      value={speed.last && !speed.last.error ? count(Math.round(speed.last.downMbps)) : '—'} unit={speed.last && !speed.last.error ? 'Mbit/s down' : ''}
      sub={speed.running ? 'testing…' : speed.last?.error ? `test failed: ${speed.last.error}` : speed.last
        ? `${count(Math.round(speed.last.upMbps))} up · ping ${speed.last.pingMs.toFixed(0)} ms · ${ago(speed.last.at)} ago${speed.last.limited ? ' · LIMITED' : ''}`
        : 'first test soon'}>
      <button class="btn sm" disabled={speed.running} onclick={testNow}>Test now</button>
    </StatCard>
  {/if}
</section>

<section class="card chart">
  <h3 class="section-title">Server throughput</h3>
  <TimeChart times={totals.map((p) => p.t)} format={(v) => bps(v, 0)} floor={1e6} height={170}
    series={[{ label: 'To clients', color: 'var(--cyan)', values: totals.map((p) => p.rx) }, { label: 'From clients', color: 'var(--accent-2)', values: totals.map((p) => p.tx) }]} />
</section>

{#if st.sessions.length === 0}
  <section class="empty card">
    <div class="ic"><Terminal size={22} /></div>
    <h2>No clients connected yet</h2>
    <p class="muted">Install the client on a laptop, desktop or router and point it here:</p>
    <code class="mono">net-bond-007 install --server {host}:{st.listen.split(':').pop()} --token &lt;your token&gt;</code>
  </section>
{/if}

{#each st.sessions as s (s.id)}
  <section class="sess glass">
    <div class="sh">
      <div>
        <h2>{s.name} <span class="chip">{s.os}</span> <span class="chip accent">{s.ip}</span>
          <span class="chip mono" class:warn={s.version !== store.info?.version} title={s.version !== store.info?.version ? `Client runs v${s.version}, server runs v${store.info?.version}` : 'Same version as the server'}>v{s.version}{s.version !== store.info?.version ? ' ⚠' : ''}</span></h2>
        <p class="muted">session {s.id} · connected {duration((Date.now() - s.created) / 1000)} · mode {s.engine.mode}{s.engine.mode === 'smart' || s.engine.mode === 'custom' ? ` ×${s.engine.redundancy}` : ''}{s.engine.arq ? ` · ${count(s.engine.retransmits)} packets re-sent` : ''}</p>
      </div>
      <button class="btn sm danger" onclick={() => kick(s.id, s.name)}><UserX size={14} />Disconnect</button>
    </div>
    <BondMap lanes={lanesFor(s)} leftTitle={s.name} leftSub={s.ip} rightTitle="This server" rightSub={st.listen}
      egress={s.engine.rxBps + s.engine.txBps} winSide="right" height={Math.max(260, 120 + (s.links?.length ?? 0) * 70)}
      onselect={(k) => (store.selected = k)} />
    <div class="links">
      {#each s.links ?? [] as l, i (key(l))}
        <LinkCard link={l} downKey="tx" delay={i * 40} onopen={() => (store.selected = `${s.id}/${key(l)}`)} />
      {/each}
    </div>
  </section>
{/each}

{#if selected}
  <LinkDrawer link={selected} editable={false} downKey="tx" onclose={() => (store.selected = null)} />
{/if}

<style>
  .banner { display: flex; align-items: center; gap: 9px; padding: 10px 14px; border-radius: 12px; background: var(--warn-bg); color: var(--warn); border: 1px solid rgba(245, 165, 36, 0.35); font-size: 13px; }
  .stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; }
  .chart { min-width: 0; padding: 16px 18px; display: grid; gap: 10px; }
  .sess { padding: 16px; display: grid; gap: 10px; }
  .sh { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; }
  h2 { margin: 0; font-size: 17px; font-weight: 700; display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .sh p { margin: 4px 0 0; font-size: 12px; }
  .links { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 12px; }
  .empty { padding: 34px; display: grid; justify-items: center; gap: 8px; text-align: center; }
  .empty .ic { width: 48px; height: 48px; border-radius: 14px; display: grid; place-items: center; background: var(--accent-bg); color: var(--accent-2); }
  .empty p { margin: 0; }
  code { background: var(--bg-2); border: 1px solid var(--line); padding: 8px 12px; border-radius: 9px; user-select: all; }
  @media (max-width: 1100px) { .stats { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  @media (max-width: 520px) { .stats { grid-template-columns: 1fr; } .links { grid-template-columns: 1fr; } }
</style>
