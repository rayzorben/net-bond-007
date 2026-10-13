<script lang="ts">
  import { Server, KeyRound, ArrowRight } from '@lucide/svelte'
  import { store } from '../lib/store.svelte'
  let address = $state('')
  let token = $state('')
  let err = $state('')
  let busy = $state(false)
  async function go(e: Event) {
    e.preventDefault()
    busy = true
    const cfg = $state.snapshot(store.config)
    cfg.server = { ...cfg.server, address: address.trim(), token: token.trim(), enabled: true }
    err = (await store.saveConfig(cfg)) ?? ''
    busy = false
    if (!err) store.toast('Connecting to your server…', 'info')
  }
</script>

<section class="ob card">
  <div class="blob"></div>
  <div class="txt">
    <span class="chip accent">Step 1 of 1</span>
    <h1>Point Net-Bond-007 at your server</h1>
    <p>Every network this device can see joins the bond automatically. Ethernet, Wi-Fi, a tethered phone and more all act as one faster, more reliable connection.</p>
  </div>
  <form onsubmit={go}>
    <label><Server size={16} /><input class="input" placeholder="bond.example.com:7007" bind:value={address} required /></label>
    <label><KeyRound size={16} /><input class="input" placeholder="Access token" bind:value={token} type="password" /></label>
    {#if err}<div class="e">{err}</div>{/if}
    <button class="btn primary lg" disabled={busy || !address}>Connect <ArrowRight size={16} /></button>
  </form>
</section>

<style>
  .ob { position: relative; overflow: hidden; padding: 34px; display: grid; grid-template-columns: 1.2fr 1fr; gap: 30px; align-items: center; }
  .blob { position: absolute; right: -120px; top: -160px; width: 520px; height: 520px; background: radial-gradient(closest-side, rgba(139, 92, 246, 0.25), transparent); pointer-events: none; }
  h1 { font-size: 30px; letter-spacing: -0.02em; margin: 12px 0 8px; }
  p { color: var(--text-2); max-width: 520px; margin: 0; }
  form { display: grid; gap: 10px; position: relative; }
  label { display: flex; align-items: center; gap: 10px; color: var(--muted); }
  .e { color: var(--bad); font-size: 12.5px; }
  @media (max-width: 800px) { .ob { grid-template-columns: 1fr; padding: 22px; } }
</style>
