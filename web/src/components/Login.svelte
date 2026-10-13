<script lang="ts">
  import { Lock } from '@lucide/svelte'
  import { store } from '../lib/store.svelte'
  let pw = $state('')
  let err = $state('')
  let busy = $state(false)
  async function submit(e: Event) {
    e.preventDefault()
    busy = true
    err = (await store.login(pw)) ?? ''
    busy = false
  }
</script>

<div class="wrap">
  <div class="blob"></div>
  <form class="card glass" onsubmit={submit}>
    <img class="logo" src="/logo.png" width="240" alt="Net-Bond-007" />
    <h1 class="sr"><Lock size={16} /> Net-Bond-007</h1>
    <p class="muted">This dashboard is password protected.</p>
    <input class="input" class:err={!!err} type="password" placeholder="Password" bind:value={pw} autocomplete="current-password" />
    {#if err}<div class="e">{err}</div>{/if}
    <button class="btn primary lg" disabled={busy || !pw}>Unlock</button>
  </form>
</div>

<style>
  .logo { width: 240px; height: auto; margin: -6px 0 2px; filter: drop-shadow(0 0 18px rgba(34, 211, 238, 0.25)); }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
  .wrap { min-height: 100vh; display: grid; place-items: center; position: relative; overflow: hidden; }
  .blob { position: absolute; width: 640px; height: 640px; background: radial-gradient(closest-side, rgba(139, 92, 246, 0.28), transparent); filter: blur(20px); }
  form { position: relative; width: min(360px, 92vw); padding: 28px; display: grid; gap: 12px; justify-items: center; text-align: center; }
  h1 { margin: 0; font-size: 24px; font-weight: 780; letter-spacing: 0.06em; }
  p { margin: 0 0 6px; }
  .btn { width: 100%; }
  .e { color: var(--bad); font-size: 12.5px; }
</style>
