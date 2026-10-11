<script lang="ts">
  import { CircleCheck, CircleAlert, Info } from '@lucide/svelte'
  import { store } from '../lib/store.svelte'
</script>

<div class="toasts" aria-live="polite">
  {#each store.toasts as t (t.id)}
    <div class="toast {t.kind}">
      {#if t.kind === 'ok'}<CircleCheck size={16} />{:else if t.kind === 'err'}<CircleAlert size={16} />{:else}<Info size={16} />{/if}
      <span>{t.text}</span>
    </div>
  {/each}
</div>

<style>
  .toasts { position: fixed; bottom: 18px; left: 50%; transform: translateX(-50%); display: grid; gap: 8px; z-index: 60; justify-items: center; }
  .toast { display: flex; align-items: center; gap: 9px; padding: 10px 14px; border-radius: 12px; background: var(--panel-2); border: 1px solid var(--line-2); box-shadow: var(--shadow); font-size: 13px; animation: fade-up 0.2s var(--ease-out); max-width: min(560px, 92vw); }
  .ok :global(svg) { color: var(--ok); }
  .err { border-color: rgba(251, 79, 110, 0.5); }
  .err :global(svg) { color: var(--bad); }
  .info :global(svg) { color: var(--info); }
</style>
