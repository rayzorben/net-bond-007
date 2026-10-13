<script lang="ts">
  import { bps, bytes, duration } from '../lib/format'
  import LinkShareBar from './LinkShareBar.svelte'

  export interface Conn {
    proto: string; inside: string; outside: string; name?: string; host?: string; local?: string
    txBps: number; rxBps: number; txBytes: number; rxBytes: number; first: number; last: number
    links: { id: number; name: string; kind: string; copies: number; wins: number }[] | null
  }
  let { conns, limit = 50 }: { conns: Conn[]; limit?: number } = $props()
  let showAll = $state(false)
  const shown = $derived(showAll ? conns : conns.slice(0, limit))
  const port = (ap: string) => ap.slice(ap.lastIndexOf(':') + 1)
  const ip = (ap: string) => ap.slice(0, ap.lastIndexOf(':')).replace(/^\[|\]$/g, '')
  const service: Record<string, string> = { '443': 'https', '80': 'http', '53': 'dns', '22': 'ssh', '3478': 'stun', '993': 'imaps', '853': 'dot' }
</script>

<div class="wrap">
  <table>
    <thead>
      <tr><th>Destination</th><th class="r">Down</th><th class="r">Up</th><th class="r">Total</th><th>Delivered by</th><th class="r">Age</th></tr>
    </thead>
    <tbody>
      {#each shown as c (c.proto + c.inside + c.outside)}
        {@const active = c.rxBps + c.txBps > 8000}
        <tr class:active>
          <td class="dst">
            <span class="dot" class:on={active}></span>
            <div>
              <b>{c.host || ip(c.outside)}</b>
              <span class="mono">{c.host ? ip(c.outside) + ' · ' : ''}{c.proto}/{port(c.outside)}{service[port(c.outside)] ? ` ${service[port(c.outside)]}` : ''}</span>
            </div>
          </td>
          <td class="r num">{bps(c.rxBps)}</td>
          <td class="r num">{bps(c.txBps)}</td>
          <td class="r num">{bytes(c.rxBytes + c.txBytes)}</td>
          <td><LinkShareBar links={c.links} by="wins" /></td>
          <td class="r num faint">{duration((Date.now() - c.first) / 1000)}</td>
        </tr>
      {:else}
        <tr><td colspan="6" class="empty">No connections right now.</td></tr>
      {/each}
    </tbody>
  </table>
  {#if conns.length > limit}
    <button class="btn sm ghost more" onclick={() => (showAll = !showAll)}>{showAll ? 'Show fewer' : `Show all ${conns.length}`}</button>
  {/if}
</div>

<style>
  .wrap { overflow-x: auto; }
  table { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  th { text-align: left; font-size: 10.5px; text-transform: uppercase; letter-spacing: 0.07em; color: var(--muted); font-weight: 650; padding: 6px 8px; border-bottom: 1px solid var(--line); white-space: nowrap; }
  td { padding: 7px 8px; border-bottom: 1px solid color-mix(in srgb, var(--line) 60%, transparent); white-space: nowrap; }
  .r { text-align: right; }
  tr:hover td { background: var(--panel-2); }
  tr:not(.active) td { color: var(--text-2); }
  .dst { display: flex; align-items: center; gap: 9px; max-width: 420px; }
  .dst div { display: grid; min-width: 0; }
  .dst b { font-weight: 600; overflow: hidden; text-overflow: ellipsis; }
  .dst .mono { font-size: 11px; color: var(--faint); }
  .dot { width: 7px; height: 7px; border-radius: 99px; background: var(--line-2); flex: none; }
  .dot.on { background: var(--ok); box-shadow: 0 0 6px var(--ok); }
  .empty { color: var(--muted); text-align: center; padding: 18px; }
  .more { margin-top: 6px; }
</style>
