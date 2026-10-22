<script lang="ts">
  import { onMount } from 'svelte'
  import { store } from './lib/store.svelte'
  import TopBar from './components/TopBar.svelte'
  import Toasts from './components/Toasts.svelte'
  import Login from './components/Login.svelte'
  import ClientOverview from './pages/ClientOverview.svelte'
  import ServerOverview from './pages/ServerOverview.svelte'
  import Settings from './pages/Settings.svelte'
  import Logs from './pages/Logs.svelte'
  import Connections from './pages/Connections.svelte'
  import History from './pages/History.svelte'
  import Ports from './pages/Ports.svelte'
  import Bypass from './pages/Bypass.svelte'

  let bootErr = $state('')
  onMount(() => { store.boot().catch((e) => (bootErr = String(e))) })
</script>

{#if bootErr}
  <div class="center"><p>Cannot reach Net-Bond-007: {bootErr}</p></div>
{:else if !store.authed}
  <Login />
{:else}
  <TopBar />
  <main>
    {#if store.page === 'settings' && store.config && store.schema.length}
      <Settings />
    {:else if store.page === 'logs'}
      <Logs />
    {:else if store.page === 'connections'}
      <Connections />
    {:else if store.page === 'history'}
      <History />
    {:else if store.page === 'ports'}
      <Ports />
    {:else if store.page === 'bypass'}
      <Bypass />
    {:else if store.state?.role === 'client'}
      <ClientOverview st={store.state} />
    {:else if store.state?.role === 'server'}
      <ServerOverview st={store.state} />
    {:else}
      <div class="skeleton card"></div>
    {/if}
  </main>
  <Toasts />
{/if}

<style>
  main { max-width: 1480px; margin: 0 auto; padding: 18px 20px 60px; display: grid; gap: 14px; }
  .center { min-height: 100vh; display: grid; place-items: center; color: var(--muted); }
  .skeleton { height: 380px; background: linear-gradient(90deg, var(--panel) 0%, var(--panel-2) 50%, var(--panel) 100%); background-size: 200% 100%; animation: shimmer 1.4s linear infinite; }
  @media (max-width: 600px) { main { padding: 12px 10px 40px; } }
</style>
