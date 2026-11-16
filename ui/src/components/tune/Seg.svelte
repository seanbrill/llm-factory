<script lang="ts" generics="T extends string | number">
  // A segmented control (sheet frame f20): one choice from a few, as a row of
  // toggle buttons. A disabled option says why in its title.
  let {
    value,
    options,
    label,
    onchange,
  }: {
    value: T | undefined;
    options: { value: T; label: string; disabled?: boolean; title?: string }[];
    label: string;
    onchange: (v: T) => void;
  } = $props();
</script>

<div class="seg" role="group" aria-label={label}>
  {#each options as o (o.value)}
    <button
      type="button"
      class:on={value === o.value}
      aria-pressed={value === o.value}
      disabled={o.disabled}
      title={o.title}
      onclick={() => onchange(o.value)}>{o.label}</button>
  {/each}
</div>

<style>
  .seg { display: inline-flex; flex-wrap: wrap; gap: 2px; padding: 3px; background: var(--p2); border: 1px solid var(--ln2); border-radius: var(--r); }
  .seg button {
    height: auto;
    padding: 5px 11px;
    border: 0;
    border-radius: var(--rs);
    background: transparent;
    color: var(--mu);
    font-size: 12.5px;
    font-weight: 500;
  }
  .seg button:hover:not(:disabled) { color: var(--ink); }
  .seg button.on { background: var(--p3); color: var(--ink); box-shadow: inset 0 0 0 1px var(--ln2); }
  .seg button:disabled { color: var(--fa); text-decoration: line-through; text-decoration-color: var(--ln2); cursor: not-allowed; }
</style>
