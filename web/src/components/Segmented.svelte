<script lang="ts">
  let { value, options, onchange, size = 'md' }:
    { value: string; options: { value: string; label: string; hint?: string }[]; onchange: (v: string) => void; size?: 'sm' | 'md' } = $props()
</script>

<div class="seg {size}" role="radiogroup">
  {#each options as o (o.value)}
    <button type="button" role="radio" aria-checked={value === o.value} class:active={value === o.value}
      title={o.hint ?? ''} onclick={() => value !== o.value && onchange(o.value)}>{o.label}</button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; gap: 2px; padding: 2px; border-radius: 9px; background: var(--bg-2); border: 1px solid var(--line); flex-wrap: wrap; }
  button {
    height: 28px; padding: 0 12px; border: 0; border-radius: 7px; background: transparent; cursor: pointer;
    font-size: 12.5px; font-weight: 560; color: var(--muted); transition: background 0.15s, color 0.15s;
  }
  .sm button { height: 24px; padding: 0 9px; font-size: 12px; }
  button:hover { color: var(--text); }
  button.active { background: var(--panel-3); color: var(--text); box-shadow: inset 0 0 0 1px var(--line-2), 0 1px 2px rgb(0 0 0 / 25%); }
</style>
