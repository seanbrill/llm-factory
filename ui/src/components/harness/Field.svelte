<script lang="ts">
  // A labelled read-only value in a code-styled box with its actions (Copy,
  // Change, …) on the right — the address and key fields of sheet frame f12.
  import type { Snippet } from "svelte";

  let {
    label,
    value,
    full = "",
    mono = true,
    muted = false,
    children,
  }: {
    label: string;
    value: string;
    /** The whole value when `value` is shortened (shown on hover). */
    full?: string;
    mono?: boolean;
    muted?: boolean;
    children?: Snippet;
  } = $props();

  const uid = $props.id();
</script>

<div class="fld">
  <span class="fl" id="{uid}-l">{label}</span>
  <div class="val" role="group" aria-labelledby="{uid}-l">
    <span class="v" class:mono class:muted title={full || value}>{value}</span>
    {#if children}<span class="act">{@render children()}</span>{/if}
  </div>
</div>

<style>
  .fld { display: flex; flex-direction: column; gap: 5px; margin-top: 12px; min-width: 0; }
  .fl { font-size: 12px; color: var(--fa); font-weight: 500; }
  .val {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 34px;
    padding: 3px 3px 3px 11px;
    border-radius: 9px;
    background: var(--cv);
    border: 1px solid var(--ln);
    min-width: 0;
  }
  .v {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 13px;
  }
  .v.mono { font: 12px/1.5 var(--font-mono); }
  .v.muted { color: var(--mu); }
  .act { display: flex; gap: 4px; flex: none; }
  .act :global(button), .act :global(.btn) { min-height: 26px; height: 26px; padding: 0 9px; font-size: 12.5px; }
  .act :global(.btn.icon), .act :global(button.icon) { width: 26px; padding: 0; }
  /* Labelled buttons are text-only here, as in the sheet ("Copy", "New"). */
  .act :global(.btn:not(.icon) > .ico) { display: none; }
</style>
