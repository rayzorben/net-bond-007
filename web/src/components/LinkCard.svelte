<script lang="ts">
  import type { LinkView } from '../lib/types'
  import { bps, ms, pct, kindColor, kindLabel, stateTone, displayName } from '../lib/format'
  import KindIcon from './KindIcon.svelte'
  import Gauge from './Gauge.svelte'
  import Sparkline from './Sparkline.svelte'

  let { link, downKey = 'rx', onopen, delay = 0 }: { link: LinkView; downKey?: 'rx' | 'tx'; onopen: () => void; delay?: number } = $props()
  const hist = $derived((link.history ?? []).slice(-90))
  const winRatio = $derived(link.wins + link.dups ? link.wins / (link.wins + link.dups) : 0)
  const down = $derived(downKey === 'rx' ? link.rxBps : link.txBps)
  const upRate = $derived(downKey === 'rx' ? link.txBps : link.rxBps)
</script>

<button class="lc card" style="--kc:{kindColor(link.kind)};animation-delay:{delay}ms" onclick={onopen}>
  <div class="head">
    <div class="badge"><KindIcon kind={link.kind} size={18} /></div>
    <div class="ttl">
      <b>{displayName(link)}</b>
      <span>{kindLabel[link.kind] ?? link.kind}{link.label ? ` · ${link.name}` : ''}{link.local ? ` · ${link.local}` : ''}</span>
    </div>
    <span class="chip {stateTone(link.state)}"><span class="dot" class:live={link.state === 'up'}></span>{link.state}</span>
  </div>

  <div class="body">
    <Gauge value={link.state === 'down' ? 0 : link.quality} size={68} label="quality" />
    <div class="metrics">
      <div class="m">
        <span class="k">Latency</span><b class="num">{ms(link.rttMs)}</b>
        <Sparkline values={hist.map((s) => s.r)} color="var(--kc)" height={22} />
      </div>
      <div class="m">
        <span class="k">Down</span><b class="num">{bps(down)}</b>
        <Sparkline values={hist.map((s) => (downKey === 'rx' ? s.rx : s.tx))} color="var(--cyan)" height={22} />
      </div>
      <div class="m">
        <span class="k">Up</span><b class="num">{bps(upRate)}</b>
        <Sparkline values={hist.map((s) => (downKey === 'rx' ? s.tx : s.rx))} color="var(--accent-2)" height={22} />
      </div>
    </div>
  </div>

  <div class="foot">
    <div class="bar" title="Share of copies on this link that arrived first">
      <i style="width:{winRatio * 100}%"></i>
    </div>
    <span class="win"><b class="num">{pct(winRatio * 100, 0)}</b> races won</span>
    <span class="sep"></span>
    <span class="kv">loss <b class="num" class:bad={link.lossPct > 3}>{pct(link.lossPct)}</b></span>
    <span class="kv">jitter <b class="num">{ms(link.jitterMs)}</b></span>
    {#if link.backup}<span class="chip">backup</span>{/if}
  </div>
  {#if link.lastError && link.state !== 'up'}<div class="err">{link.lastError}</div>{/if}
</button>

<style>
  .lc {
    all: unset; box-sizing: border-box; display: grid; gap: 14px; padding: 16px; cursor: pointer; position: relative; overflow: hidden;
    background: var(--panel); border: 1px solid var(--line); border-radius: var(--r-lg);
    transition: border-color 0.15s, transform 0.15s var(--ease-out), box-shadow 0.15s;
  }
  .lc::before { content: ''; position: absolute; inset: 0 auto 0 0; width: 3px; background: var(--kc); opacity: 0.9; }
  .lc:hover { border-color: color-mix(in srgb, var(--kc) 50%, var(--line)); transform: translateY(-2px); box-shadow: 0 12px 30px -16px color-mix(in srgb, var(--kc) 60%, transparent); }
  .lc:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .head { display: flex; align-items: center; gap: 10px; }
  .badge { width: 36px; height: 36px; border-radius: 10px; display: grid; place-items: center; color: var(--kc); background: color-mix(in srgb, var(--kc) 15%, transparent); flex: none; }
  .ttl { display: grid; min-width: 0; flex: 1; }
  .ttl b { font-size: 14.5px; font-weight: 650; }
  .ttl span { font-size: 11.5px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .body { display: flex; gap: 14px; align-items: center; }
  .metrics { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; flex: 1; min-width: 0; }
  .m { display: grid; gap: 1px; min-width: 0; }
  .k { font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); font-weight: 650; }
  .m b { font-size: 14px; font-weight: 680; white-space: nowrap; }
  .foot { display: flex; align-items: center; gap: 10px; font-size: 11.5px; color: var(--muted); flex-wrap: wrap; }
  .bar { width: 70px; height: 6px; border-radius: 6px; background: var(--line); overflow: hidden; }
  .bar i { display: block; height: 100%; background: linear-gradient(90deg, #f59e0b, var(--gold)); box-shadow: 0 0 8px var(--gold); transition: width 0.5s var(--ease-out); }
  .win b { color: var(--gold); }
  .sep { flex: 1; }
  .kv b { color: var(--text-2); }
  .kv b.bad { color: var(--bad); }
  .err { font-size: 11.5px; color: var(--warn); background: var(--warn-bg); border-radius: 8px; padding: 6px 9px; }
</style>
