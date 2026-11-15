<script lang="ts">
  // A button that opens a small panel under it (persona, sampling, output
  // settings, the ⋯ menu). Escape or a click outside closes it and focus goes
  // back to the button.
  import { tick, type Snippet } from "svelte";
  import Icon from "../Icon.svelte";

  let {
    label,
    icon = "",
    iconOnly = false,
    title = "",
    align = "end",
    wide = false,
    open = $bindable(false),
    children,
  }: {
    label: string;
    icon?: string;
    iconOnly?: boolean;
    title?: string;
    align?: "start" | "end";
    wide?: boolean;
    open?: boolean;
    children: Snippet;
  } = $props();

  const id = "pop-" + Math.random().toString(36).slice(2, 8);
  let root: HTMLDivElement | undefined = $state();
  let btn: HTMLButtonElement | undefined = $state();
  let panel: HTMLDivElement | undefined = $state();

  function close(refocus = true) {
    open = false;
    if (refocus) btn?.focus();
  }

  $effect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (root && !root.contains(e.target as Node)) close(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        close();
      }
    };
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("keydown", onKey, true);
    tick().then(() => (panel?.querySelector<HTMLElement>("input, select, textarea, button") ?? panel)?.focus());
    return () => {
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey, true);
    };
  });
</script>

<div class="pop" bind:this={root}>
  <button
    class="small"
    class:icon={iconOnly}
    class:on={open}
    type="button"
    bind:this={btn}
    aria-expanded={open}
    aria-controls={id}
    aria-label={iconOnly ? label : undefined}
    title={title || (iconOnly ? label : undefined)}
    onclick={() => (open = !open)}>
    {#if icon}<Icon name={icon} size={15} />{/if}{#if !iconOnly}<span class="lb">{label}</span><Icon name="chevronDown" size={13} cls="cv" />{/if}
  </button>
  {#if open}
    <div class="panel {align}" class:wide {id} role="dialog" aria-label={title || label} tabindex="-1" bind:this={panel}>
      {@render children()}
    </div>
  {/if}
</div>

<style>
  .pop { position: relative; display: inline-flex; }
  button.on { background: var(--p3); border-color: var(--ln2); }
  .lb { white-space: nowrap; max-width: 220px; overflow: hidden; text-overflow: ellipsis; }
  button :global(.cv) { color: var(--fa); margin-left: -2px; }
  .panel {
    position: absolute;
    top: calc(100% + 6px);
    z-index: 30;
    width: 320px;
    max-width: calc(100vw - 32px);
    padding: 14px;
    border-radius: var(--r);
    border: 1px solid var(--ln2);
    background: var(--p2);
    box-shadow: 0 12px 32px color-mix(in srgb, black 45%, transparent);
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .panel.wide { width: 380px; }
  .panel.end { right: 0; }
  .panel.start { left: 0; }
  .panel:focus { outline: none; }
  @media (max-width: 760px) {
    .panel { position: fixed; left: 16px; right: 16px; top: auto; bottom: calc(84px + env(safe-area-inset-bottom, 0px)); width: auto; max-width: none; max-height: 70dvh; overflow: auto; }
    .panel.wide { width: auto; }
  }
</style>
