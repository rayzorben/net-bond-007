<script lang="ts">
  import { Sun, Moon, LogOut, LayoutDashboard, SlidersHorizontal, ScrollText, Waypoints, CalendarClock } from '@lucide/svelte'
  import { store, toggleTheme } from '../lib/store.svelte'
  import { bps } from '../lib/format'

  const st = $derived(store.state)
  const connected = $derived(st?.role === 'client' ? st.server.connected : st?.role === 'server' ? true : false)
  const statusText = $derived.by(() => {
    if (!store.live) return 'Reconnecting…'
    if (!st) return 'Loading…'
    if (st.role === 'server') return `${st.sessions.length} client${st.sessions.length === 1 ? '' : 's'}`
    if (!st.server.enabled) return 'Bonding off'
    if (!st.server.address) return 'Not configured'
    const up = st.links.filter((l) => l.state === 'up' || l.state === 'degraded').length
    return connected ? `Bonded · ${up} link${up === 1 ? '' : 's'}` : 'Connecting…'
  })
  const myVer = $derived(store.info?.version ?? '')
  const peerVer = $derived(st?.role === 'client' ? st.server.version ?? '' : '')
  const mismatch = $derived(!!peerVer && !!myVer && peerVer !== myVer)
  const clientVers = $derived(st?.role === 'server' ? [...new Set(st.sessions.map((s) => s.version))].filter((v) => v !== myVer) : [])
  const rate = $derived(st?.role === 'client' ? st.engine.rxBps + st.engine.txBps : store.totals.at(-1) ? store.totals.at(-1)!.rx + store.totals.at(-1)!.tx : 0)
  type Page = typeof store.page
  const tabs: { id: Page; label: string; icon: any }[] = $derived([
    { id: 'overview', label: 'Overview', icon: LayoutDashboard },
    { id: 'connections', label: 'Connections', icon: Waypoints },
    ...(store.info?.role === 'server' ? [{ id: 'history', label: 'History', icon: CalendarClock }] : []),
    { id: 'settings', label: 'Settings', icon: SlidersHorizontal },
    { id: 'logs', label: 'Logs', icon: ScrollText },
  ] as { id: Page; label: string; icon: any }[])
</script>

<header class="top">
  <div class="brand">
    <img class="mark" src="/mark.png" width="30" height="30" alt="" />
    <span class="word">NET-BOND-<span class="grad-text">007</span></span>
    {#if store.info?.role === 'server'}<span class="chip accent">server</span>{/if}
    <span class="ver chip" class:warn={mismatch || clientVers.length > 0}
      title={mismatch ? `This client runs v${myVer} but the server runs v${peerVer}` : clientVers.length ? `Some clients run a different version: ${clientVers.join(', ')}` : 'Running version'}>
      v{myVer}{#if peerVer}<span class="sep">·</span>server v{peerVer}{/if}{#if mismatch || clientVers.length}&nbsp;⚠{/if}
    </span>
  </div>
  <nav>
    {#each tabs as t}
      {@const Icon = t.icon}
      <button class:active={store.page === t.id} onclick={() => (store.page = t.id)}><Icon size={15} />{t.label}</button>
    {/each}
  </nav>
  <div class="spacer"></div>
  <div class="status" class:ok={connected && store.live} class:warn={!connected || !store.live}>
    <span class="dot live"></span>{statusText}
    {#if connected && rate > 0}<span class="rate num">{bps(rate)}</span>{/if}
  </div>
  <button class="btn icon ghost" onclick={toggleTheme} aria-label="Toggle theme" title="Toggle theme">
    <span class="sun"><Sun size={17} /></span><span class="moon"><Moon size={17} /></span>
  </button>
  {#if store.authRequired}
    <button class="btn icon ghost" onclick={() => store.logout()} aria-label="Log out" title="Log out"><LogOut size={17} /></button>
  {/if}
</header>

<style>
  .top {
    position: sticky; top: 0; z-index: 20; height: 58px; display: flex; align-items: center; gap: 16px; padding: 0 20px;
    background: color-mix(in srgb, var(--bg-2) 82%, transparent); backdrop-filter: blur(12px); -webkit-backdrop-filter: blur(12px);
    border-bottom: 1px solid var(--line);
  }
  .brand { display: flex; align-items: center; gap: 9px; }
  .word { font-weight: 780; letter-spacing: 0.06em; font-size: 15px; }
  .ver { font-family: var(--mono); font-size: 11px; height: 20px; }
  .ver .sep { opacity: 0.5; margin: 0 1px; }
  .mark { display: block; filter: drop-shadow(0 0 6px rgba(34, 211, 238, 0.35)); }
  nav { display: flex; gap: 2px; padding: 3px; background: var(--panel); border: 1px solid var(--line); border-radius: 11px; }
  nav button { display: inline-flex; align-items: center; gap: 7px; height: 30px; padding: 0 12px; border: 0; border-radius: 8px; background: transparent; color: var(--muted); font-weight: 560; font-size: 13px; cursor: pointer; }
  nav button:hover { color: var(--text); }
  nav button.active { background: var(--panel-3); color: var(--text); box-shadow: inset 0 0 0 1px var(--line-2); }
  .spacer { flex: 1; }
  .status { display: inline-flex; align-items: center; gap: 8px; height: 30px; padding: 0 12px; border-radius: 99px; font-size: 12.5px; font-weight: 600; background: var(--panel); border: 1px solid var(--line); }
  .status.ok { color: var(--ok); }
  .status.warn { color: var(--warn); }
  .rate { color: var(--text-2); font-weight: 600; padding-left: 8px; border-left: 1px solid var(--line-2); }
  :global(:root[data-theme='light']) .sun { display: none; }
  :global(:root:not([data-theme='light'])) .moon { display: none; }
  .sun, .moon { display: inline-flex; }
  @media (max-width: 760px) {
    .top { gap: 8px; padding: 0 10px; }
    .word, .rate { display: none; }
    nav button { padding: 0 9px; font-size: 0; gap: 0; }
    .status { font-size: 0; padding: 0 9px; gap: 0; }
  }
</style>
