<script lang="ts" module>
  export interface Lane {
    key: string; name: string; label?: string; kind: string; state: string; backup: boolean; benched?: boolean
    rtt: number; loss: number
    up: number    // bits/s, left -> right
    down: number  // bits/s, right -> left
    winRatio: number // fraction of copies arriving on this lane that won the race
    winShare: number // fraction of all delivered packets this lane delivered first (last interval)
    dupShare: number // recent fraction of this lane's arrivals that lost the race (spare copies)
    quality: number  // 0..100
    winner: boolean  // the single lane delivering the most packets first right now
    role: LaneRole
  }
  export type LaneRole = 'speed' | 'redundancy' | 'direct' | 'idle' | ''
  /** Traffic going straight out a local link instead of through the bond. */
  export interface Direct { via: string; kind: string; why: string }
</script>

<script lang="ts">
  import { onMount } from 'svelte'
  import { Laptop, Server, Globe, Router, Zap, ShieldCheck, Moon, Route } from '@lucide/svelte'
  import KindIcon from './KindIcon.svelte'
  import { bps, ms, kindColor, cssColor, displayName } from '../lib/format'

  let { lanes, leftTitle, leftSub = '', rightTitle, rightSub = '', egress = 0, winSide = 'left', height = 380, router = false, direct = null, onselect }:
    { lanes: Lane[]; leftTitle: string; leftSub?: string; rightTitle: string; rightSub?: string; egress?: number
      winSide?: 'left' | 'right'; height?: number; router?: boolean; direct?: Direct | null; onselect?: (key: string) => void } = $props()

  const roleText: Record<string, string> = {
    speed: 'Carrying unique traffic: adds speed',
    redundancy: 'Mostly sending spare copies that lose the race: adds redundancy',
    direct: 'Carrying new connections straight to the internet, bypassing the bond',
    idle: 'No traffic right now',
  }
  const healthTone = (q: number) => (q >= 80 ? 'ok' : q >= 50 ? 'warn' : 'bad')

  // Direct bypass: an arc over the top from this device straight to the internet.
  const bypass = $derived.by(() => {
    const p0 = { x: L + 30, y: cy - 22 }, p3 = { x: G - 18, y: cy - 18 }
    const top = Math.max(-10, cy - (height / 2) * 1.55)
    const p1 = { x: L + (G - L) * 0.25, y: top }, p2 = { x: L + (G - L) * 0.75, y: top }
    return { p0, p1, p2, p3, mid: bez(p0, p1, p2, p3, 0.5) }
  })

  let w = $state(900)
  let canvas: HTMLCanvasElement

  const L = $derived(Math.max(70, w * 0.1))
  const S = $derived(w - Math.max(150, w * 0.2))
  const G = $derived(w - Math.max(46, w * 0.05))
  const cy = $derived(height / 2)

  const lanePos = $derived.by(() => {
    const n = lanes.length
    const spread = Math.min(height - 70, Math.max(n - 1, 0) * 78)
    return lanes.map((ln, i) => {
      const y = n <= 1 ? cy : cy - spread / 2 + (spread * i) / (n - 1)
      const p0 = { x: L + 34, y: cy }, p3 = { x: S - 34, y: cy }
      const p1 = { x: p0.x + (p3.x - p0.x) * 0.3, y }, p2 = { x: p0.x + (p3.x - p0.x) * 0.7, y }
      return { ln, p0, p1, p2, p3, mid: bez(p0, p1, p2, p3, 0.5) }
    })
  })

  type P = { x: number; y: number }
  function bez(a: P, b: P, c: P, d: P, t: number): P {
    const u = 1 - t
    return {
      x: u * u * u * a.x + 3 * u * u * t * b.x + 3 * u * t * t * c.x + t * t * t * d.x,
      y: u * u * u * a.y + 3 * u * u * t * b.y + 3 * u * t * t * c.y + t * t * t * d.y,
    }
  }

  // ---- particle simulation (plain objects; not reactive) ----
  interface Particle { lane: string; dir: 1 | -1; t: number; dur: number; win: boolean; size: number }
  interface Burst { x: number; y: number; age: number; color: string }
  let parts: Particle[] = []
  let bursts: Burst[] = []
  const acc = new Map<string, number>()
  let colors: Record<string, string> = {}

  function rate(bitsPerSec: number) {
    // particles/s: a trickle when idle (probes), log-scaled with traffic.
    if (bitsPerSec <= 0) return 0.6
    return Math.min(40, 0.6 + 7 * Math.log10(1 + bitsPerSec / 50e3))
  }

  function refreshColors() {
    const k = ['ethernet', 'wifi', 'cellular', 'usb', 'bluetooth', 'other']
    colors = Object.fromEntries(k.map((x) => [x, cssColor(`var(--k-${x})`)]))
    for (const v of ['gold', 'bad', 'warn', 'line-2', 'faint', 'accent', 'cyan', 'text', 'info', 'panel']) colors[v] = cssColor(`var(--${v})`)
  }
  const kc = (kind: string) => colors[['ethernet', 'wifi', 'cellular', 'usb', 'bluetooth'].includes(kind) ? kind : 'other']

  onMount(() => {
    refreshColors()
    const mo = new MutationObserver(refreshColors)
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    let raf = 0, last = performance.now(), colorTick = 0
    const frame = (now: number) => {
      const dt = Math.min(0.05, (now - last) / 1000)
      last = now
      if ((colorTick += dt) > 2) { colorTick = 0; refreshColors() }
      step(dt)
      draw()
      raf = requestAnimationFrame(frame)
    }
    raf = requestAnimationFrame(frame)
    return () => { cancelAnimationFrame(raf); mo.disconnect() }
  })

  function step(dt: number) {
    const live = new Set<string>()
    for (const { ln } of lanePos) {
      live.add(ln.key)
      if (ln.state !== 'up' && ln.state !== 'degraded') continue
      const dur = Math.min(3.2, 0.7 + ln.rtt / 55)
      for (const dir of [1, -1] as const) {
        const id = ln.key + dir
        const r = rate(dir === 1 ? ln.up : ln.down)
        let a = (acc.get(id) ?? Math.random()) + r * dt
        while (a >= 1) {
          a -= 1
          const arrivesAtWinSide = (dir === -1) === (winSide === 'left')
          parts.push({
            lane: ln.key, dir, t: 0, dur: dur * (0.92 + Math.random() * 0.16),
            win: arrivesAtWinSide ? Math.random() < ln.winRatio : true,
            size: 1.6 + Math.random() * 1.4,
          })
        }
        acc.set(id, a)
      }
    }
    const egressRate = rate(egress)
    let a = (acc.get('egress') ?? 0) + egressRate * dt
    while (a >= 1) { a -= 1; parts.push({ lane: 'egress', dir: Math.random() < 0.6 ? -1 : 1, t: 0, dur: 0.9, win: true, size: 2 }) }
    acc.set('egress', a)
    if (direct) {
      let d = (acc.get('direct') ?? 0) + 6 * dt
      while (d >= 1) { d -= 1; parts.push({ lane: 'direct', dir: Math.random() < 0.65 ? -1 : 1, t: 0, dur: 1.6, win: true, size: 2 }) }
      acc.set('direct', d)
    }

    const next: Particle[] = []
    for (const p of parts) {
      p.t += dt / p.dur
      if (p.lane !== 'egress' && p.lane !== 'direct' && !live.has(p.lane)) continue
      if (p.lane === 'direct' && !direct) continue
      if (p.t >= 1) {
        if (p.lane === 'direct') continue
        const arrivesAtWinSide = (p.dir === -1) === (winSide === 'left')
        if (p.lane !== 'egress' && arrivesAtWinSide && p.win && Math.random() < 0.5) {
          const x = p.dir === 1 ? S - 30 : L + 30
          bursts.push({ x, y: cy, age: 0, color: colors.gold })
        }
        continue
      }
      next.push(p)
    }
    parts = next.length > 1500 ? next.slice(-1500) : next
    bursts = bursts.filter((b) => (b.age += dt) < 0.6)
  }

  function draw() {
    if (!canvas) return
    const dpr = window.devicePixelRatio || 1
    if (canvas.width !== Math.round(w * dpr) || canvas.height !== Math.round(height * dpr)) {
      canvas.width = Math.round(w * dpr)
      canvas.height = Math.round(height * dpr)
    }
    const ctx = canvas.getContext('2d')!
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, w, height)

    // node halos
    halo(ctx, L, cy, 70, colors.accent)
    halo(ctx, S, cy, 70, colors.cyan)

    const byKey = new Map(lanePos.map((p) => [p.ln.key, p]))
    const t = performance.now() / 1000
    if (direct) {
      const { p0, p1, p2, p3 } = bypass
      const c = kc(direct.kind)
      ctx.save()
      ctx.beginPath()
      ctx.moveTo(p0.x, p0.y)
      ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
      ctx.lineWidth = 10
      ctx.strokeStyle = alpha(c, 0.12)
      ctx.stroke()
      ctx.lineWidth = 2.4
      ctx.setLineDash([10, 6])
      ctx.lineDashOffset = -t * 30
      ctx.strokeStyle = alpha(c, 0.9)
      ctx.stroke()
      ctx.restore()
    }
    for (const lp of lanePos) {
      const { ln, p0, p1, p2, p3 } = lp
      const up = ln.state === 'up' || ln.state === 'degraded'
      const c = ln.state === 'down' ? colors.bad : kc(ln.kind)
      ctx.save()
      if (direct) ctx.globalAlpha = 0.45
      ctx.beginPath()
      ctx.moveTo(p0.x, p0.y)
      ctx.bezierCurveTo(p1.x, p1.y, p2.x, p2.y, p3.x, p3.y)
      if (up) {
        // glow + body
        const traffic = Math.log10(1 + (ln.up + ln.down) / 1e5)
        ctx.lineWidth = 7 + traffic * 2.4
        ctx.strokeStyle = alpha(c, ln.backup ? 0.05 : 0.1)
        ctx.stroke()
        const dim = ln.backup || ln.benched
        ctx.lineWidth = 2.2
        ctx.strokeStyle = alpha(c, dim ? 0.3 : ln.state === 'degraded' ? 0.55 : 0.85)
        if (dim) ctx.setLineDash([6, 6])
        ctx.stroke()
      } else {
        ctx.setLineDash([4, 7])
        ctx.lineDashOffset = ln.state === 'connecting' ? -t * 20 : 0
        ctx.lineWidth = 1.6
        ctx.strokeStyle = alpha(ln.state === 'connecting' ? colors.info : c, 0.55)
        ctx.stroke()
      }
      ctx.restore()
    }

    // egress pipe
    ctx.save()
    ctx.lineCap = 'round'
    const g = ctx.createLinearGradient(S, 0, G, 0)
    g.addColorStop(0, alpha(colors.cyan, 0.7))
    g.addColorStop(1, alpha(colors.accent, 0.7))
    ctx.lineWidth = 8 + Math.log10(1 + egress / 1e5) * 3
    ctx.strokeStyle = alpha(colors.cyan, 0.1)
    line(ctx, S + 34, cy, G - 26, cy)
    ctx.lineWidth = 2.4
    ctx.strokeStyle = g
    line(ctx, S + 34, cy, G - 26, cy)
    ctx.restore()

    // particles
    for (const p of parts) {
      let x: number, y: number, c: string
      if (p.lane === 'direct') {
        const b = bypass
        ;({ x, y } = bez(b.p0, b.p1, b.p2, b.p3, p.dir === 1 ? p.t : 1 - p.t))
        c = direct ? kc(direct.kind) : colors.cyan
      } else if (p.lane === 'egress') {
        const tt = p.dir === 1 ? p.t : 1 - p.t
        x = S + 34 + (G - 26 - S - 34) * tt
        y = cy + (Math.random() - 0.5) * 0.6
        c = colors.cyan
      } else {
        const lp = byKey.get(p.lane)
        if (!lp) continue
        const tt = p.dir === 1 ? p.t : 1 - p.t
        ;({ x, y } = bez(lp.p0, lp.p1, lp.p2, lp.p3, tt))
        c = kc(lp.ln.kind)
      }
      let a = 0.95
      if (!p.win && p.t > 0.7) a *= Math.max(0, 1 - (p.t - 0.7) / 0.3) // duplicate: fades, discarded
      const downstream = p.dir === -1
      ctx.beginPath()
      ctx.arc(x, y, downstream ? p.size + 0.4 : p.size * 0.8, 0, Math.PI * 2)
      ctx.fillStyle = alpha(p.win && p.t > 0.85 && p.lane !== 'egress' && p.lane !== 'direct' && downstream === (winSide === 'left') ? colors.gold : c, a)
      ctx.shadowColor = c
      ctx.shadowBlur = downstream ? 8 : 4
      ctx.fill()
      ctx.shadowBlur = 0
    }
    for (const b of bursts) {
      const k = b.age / 0.6
      ctx.beginPath()
      ctx.arc(b.x, b.y, 8 + k * 22, 0, Math.PI * 2)
      ctx.strokeStyle = alpha(b.color, 0.5 * (1 - k))
      ctx.lineWidth = 2
      ctx.stroke()
    }
  }

  function line(ctx: CanvasRenderingContext2D, x1: number, y1: number, x2: number, y2: number) {
    ctx.beginPath(); ctx.moveTo(x1, y1); ctx.lineTo(x2, y2); ctx.stroke()
  }
  function halo(ctx: CanvasRenderingContext2D, x: number, y: number, r: number, c: string) {
    const g = ctx.createRadialGradient(x, y, 0, x, y, r)
    g.addColorStop(0, alpha(c, 0.22))
    g.addColorStop(1, alpha(c, 0))
    ctx.fillStyle = g
    ctx.fillRect(x - r, y - r, r * 2, r * 2)
  }
  function alpha(c: string, a: number) {
    if (!c) return `rgba(128,128,128,${a})`
    if (c.startsWith('#')) {
      let h = c.slice(1)
      if (h.length === 3) h = h.split('').map((x) => x + x).join('')
      const n = parseInt(h, 16)
      return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`
    }
    const m = c.match(/rgba?\(([^)]+)\)/)
    if (m) { const [r, g, b] = m[1].split(',').map((s) => parseFloat(s)); return `rgba(${r},${g},${b},${a})` }
    return c
  }
</script>

<div class="map" bind:clientWidth={w} style="height:{height}px">
  <canvas bind:this={canvas} style="width:{w}px;height:{height}px"></canvas>

  <div class="node left" style="left:{L}px;top:{cy}px">
    <div class="orb">{#if router}<Router size={26} />{:else}<Laptop size={26} />{/if}</div>
    <div class="cap"><b>{leftTitle}</b><span>{leftSub}</span></div>
  </div>
  <div class="node right" class:bypassed={!!direct} style="left:{S}px;top:{cy}px">
    <div class="orb srv"><Server size={26} /></div>
    <div class="cap"><b>{rightTitle}</b><span>{direct ? 'bypassed for new connections' : rightSub}</span></div>
  </div>
  {#if direct}
    <div class="bypass" style="left:{bypass.mid.x}px;top:{bypass.mid.y}px;--kc:{kindColor(direct.kind)}" title={direct.why}>
      <Route size={13} /><b>Direct</b><span>via {direct.via} · bond bypassed</span>
    </div>
  {/if}
  <div class="node net" style="left:{G}px;top:{cy}px">
    <div class="orb sm"><Globe size={20} /></div>
    <div class="cap"><b>Internet</b><span>{bps(egress)}</span></div>
  </div>

  {#each lanePos as lp (lp.ln.key)}
    {@const ln = lp.ln}
    {@const live = ln.state === 'up' || ln.state === 'degraded'}
    <button class="lane {ln.state}" class:backup={ln.backup} style="left:{lp.mid.x}px;top:{lp.mid.y}px;--kc:{kindColor(ln.kind)}"
      onclick={() => onselect?.(ln.key)} title="Open {displayName(ln)} details">
      <span class="ic"><KindIcon kind={ln.kind} size={14} />
        {#if live}<i class="hd {healthTone(ln.quality)}" title="Health {Math.round(ln.quality)}/100 · {ms(ln.rtt)}, {ln.loss.toFixed(1)}% loss"></i>{/if}
      </span>
      <span class="nm">{displayName(ln)}</span>
      {#if live}
        <span class="st num">{ms(ln.rtt)}</span>
        <span class="st num down">↓ {bps(ln.down)}</span>
        <span class="st num up">↑ {bps(ln.up)}</span>
        {#if ln.role && !ln.backup && !ln.benched}
          <span class="role {ln.role}" title={roleText[ln.role]}>
            {#if ln.role === 'speed'}<Zap size={11} />{:else if ln.role === 'redundancy'}<ShieldCheck size={11} />{:else if ln.role === 'direct'}<Route size={11} />{:else}<Moon size={11} />{/if}
            {ln.role}
          </span>
        {/if}
        {#if ln.winner}<span class="crown" title="Delivering the most packets first right now ({Math.round(ln.winShare * 100)}%)">★</span>{/if}
        <span class="share" title="Delivered {Math.round(ln.winShare * 100)}% of packets first (speed) · {Math.round(ln.dupShare * 100)}% of its arrivals were spare copies (redundancy)">
          <i class="w" style="width:{Math.round(ln.winShare * 100)}%"></i>
        </span>
      {:else}
        <span class="st">{ln.state}</span>
      {/if}
      {#if ln.backup}<span class="st">backup</span>{/if}
      {#if ln.benched}<span class="st benched">benched</span>{/if}
    </button>
  {/each}

  {#if lanes.length === 0}
    <div class="empty">No links yet. Waiting for network interfaces…</div>
  {/if}
</div>

<style>
  .map { position: relative; width: 100%; overflow: hidden; }
  canvas { position: absolute; inset: 0; }
  .node { position: absolute; transform: translate(-50%, -50%); display: grid; justify-items: center; gap: 8px; pointer-events: none; }
  .orb {
    width: 64px; height: 64px; border-radius: 50%; display: grid; place-items: center; color: #fff;
    background: radial-gradient(circle at 30% 25%, #9b70ff, #6d3df5 70%);
    box-shadow: 0 0 0 1px rgba(255,255,255,.12) inset, 0 10px 30px -6px rgba(139, 92, 246, 0.7);
  }
  .orb.srv { background: radial-gradient(circle at 30% 25%, #4ee3f5, #0e9fb8 70%); box-shadow: 0 0 0 1px rgba(255,255,255,.12) inset, 0 10px 30px -6px rgba(34, 211, 238, 0.6); }
  .orb.sm { width: 44px; height: 44px; background: var(--panel-3); color: var(--text-2); box-shadow: inset 0 0 0 1px var(--line-2); }
  .cap { position: absolute; top: calc(100% + 6px); display: grid; justify-items: center; white-space: nowrap; }
  .cap b { font-size: 12.5px; font-weight: 650; }
  .cap span { font-size: 11px; color: var(--muted); }
  .lane {
    position: absolute; transform: translate(-50%, -50%); display: inline-flex; align-items: center; gap: 7px;
    height: 28px; padding: 0 10px 0 4px; border-radius: 99px; cursor: pointer; white-space: nowrap;
    background: var(--glass-strong); backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px);
    border: 1px solid color-mix(in srgb, var(--kc) 45%, var(--line)); font-size: 11.5px;
    box-shadow: 0 4px 16px -6px rgb(0 0 0 / 50%); transition: transform 0.15s var(--ease-out), border-color 0.15s;
  }
  .lane:hover { transform: translate(-50%, -50%) scale(1.04); border-color: var(--kc); }
  .lane .ic { width: 20px; height: 20px; border-radius: 99px; display: grid; place-items: center; color: var(--kc); background: color-mix(in srgb, var(--kc) 16%, transparent); }
  .lane .nm { font-weight: 650; }
  .lane .st { color: var(--muted); }
  .lane .down { color: var(--text-2); }
  .lane.down { border-color: color-mix(in srgb, var(--bad) 55%, var(--line)); }
  .lane.down .st, .lane.down .ic { color: var(--bad); }
  .lane.degraded { border-color: color-mix(in srgb, var(--warn) 55%, var(--line)); }
  .lane.connecting .st { color: var(--info); }
  .lane.backup { opacity: 0.75; }
  .lane .benched { color: var(--warn); }
  .lane .ic { position: relative; }
  .hd { position: absolute; right: -2px; bottom: -2px; width: 8px; height: 8px; border-radius: 50%; border: 1.5px solid var(--panel); }
  .hd.ok { background: var(--ok); box-shadow: 0 0 6px var(--ok); }
  .hd.warn { background: var(--warn); box-shadow: 0 0 6px var(--warn); }
  .hd.bad { background: var(--bad); box-shadow: 0 0 6px var(--bad); }
  .role { display: inline-flex; align-items: center; gap: 3px; height: 17px; padding: 0 6px; border-radius: 99px; font-size: 10px; font-weight: 650; text-transform: uppercase; letter-spacing: 0.04em; }
  .role.speed { color: var(--gold); background: var(--gold-bg); }
  .role.redundancy { color: var(--info); background: var(--info-bg); }
  .role.direct { color: var(--ok); background: var(--ok-bg); }
  .role.idle { color: var(--muted); background: color-mix(in srgb, var(--muted) 12%, transparent); }
  .share { position: absolute; left: 14px; right: 14px; bottom: -1px; height: 2px; border-radius: 2px; overflow: hidden; background: transparent; pointer-events: auto; }
  .share .w { display: block; height: 100%; background: var(--gold); box-shadow: 0 0 6px var(--gold); transition: width 0.6s var(--ease-out); }
  .node.bypassed { opacity: 0.55; }
  .bypass {
    position: absolute; transform: translate(-50%, -50%); display: inline-flex; align-items: center; gap: 6px;
    height: 26px; padding: 0 11px; border-radius: 99px; white-space: nowrap; font-size: 11.5px; color: var(--kc);
    background: var(--glass-strong); backdrop-filter: blur(10px); -webkit-backdrop-filter: blur(10px);
    border: 1px solid var(--kc); box-shadow: 0 0 18px -4px var(--kc);
  }
  .bypass b { font-weight: 700; text-transform: uppercase; letter-spacing: 0.06em; font-size: 10.5px; }
  .bypass span { color: var(--text-2); }
  .crown { color: var(--gold); font-size: 12px; filter: drop-shadow(0 0 4px var(--gold)); }
  .empty { position: absolute; left: 50%; top: 22%; transform: translateX(-50%); color: var(--muted); font-size: 13px; }
  @media (max-width: 720px) {
    .lane .up, .lane .st.num:first-of-type, .lane .role { display: none; }
  }
</style>
