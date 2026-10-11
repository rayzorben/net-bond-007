<script lang="ts">
  let { checked = $bindable(false), onchange, disabled = false, label = '' }:
    { checked?: boolean; onchange?: (v: boolean) => void; disabled?: boolean; label?: string } = $props()
  function flip() {
    if (disabled) return
    checked = !checked
    onchange?.(checked)
  }
</script>

<button type="button" class="toggle" class:on={checked} {disabled} role="switch" aria-checked={checked} aria-label={label} onclick={flip}>
  <span></span>
</button>

<style>
  .toggle {
    flex: none; width: 38px; height: 22px; border-radius: 99px; padding: 0; cursor: pointer;
    border: 1px solid var(--line-2); background: var(--panel-3); position: relative;
    transition: background 0.2s, border-color 0.2s;
  }
  .toggle span {
    position: absolute; top: 2px; left: 2px; width: 16px; height: 16px; border-radius: 99px;
    background: var(--muted); transition: transform 0.2s var(--spring), background 0.2s;
  }
  .toggle.on { background: var(--accent); border-color: var(--accent); }
  .toggle.on span { transform: translateX(16px); background: #fff; }
  .toggle:disabled { opacity: 0.45; cursor: default; }
</style>
