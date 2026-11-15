<script lang="ts">
  // A modal dialog on the native <dialog>: Escape and the backdrop close it, focus
  // moves in on open and back to what had it on close. Rendered only while open,
  // so a closed one never counts as a modal for the app's key shortcuts.
  import type { Snippet } from "svelte";
  import Icon from "../Icon.svelte";

  let {
    open = $bindable(false),
    title,
    sub = "",
    wide = false,
    onclose,
    children,
    actions,
  }: {
    open?: boolean;
    title: string;
    sub?: string;
    wide?: boolean;
    onclose?: () => void;
    children?: Snippet;
    actions?: Snippet;
  } = $props();

  let el: HTMLDialogElement | undefined = $state();
  const id = "dlg-" + Math.random().toString(36).slice(2, 8);

  $effect(() => {
    if (!el) return;
    const prev = document.activeElement as HTMLElement | null;
    if (!el.open) el.showModal();
    // A press that starts and ends on the backdrop (the dialog element itself,
    // outside the box) closes it; a drag out of a text field doesn't.
    let downOnBackdrop = false;
    const down = (e: MouseEvent) => (downOnBackdrop = e.target === el);
    const up = (e: MouseEvent) => {
      if (downOnBackdrop && e.target === el) close();
      downOnBackdrop = false;
    };
    el.addEventListener("mousedown", down);
    el.addEventListener("mouseup", up);
    const node = el;
    return () => {
      node.removeEventListener("mousedown", down);
      node.removeEventListener("mouseup", up);
      prev?.focus?.();
    };
  });

  function close() {
    if (!open) return;
    open = false;
    onclose?.();
  }
</script>

{#if open}
  <dialog
    bind:this={el}
    class="dlg"
    class:wide
    aria-modal="true"
    aria-labelledby={id}
    oncancel={(e) => { e.preventDefault(); close(); }}>
    <div class="box">
      <header>
        <span class="tt">
          <h2 {id}>{title}</h2>
          {#if sub}<span class="sub">{sub}</span>{/if}
        </span>
        <button class="ghost small icon" type="button" aria-label="Close" onclick={close}><Icon name="xmark" size={15} /></button>
      </header>
      <div class="body">{@render children?.()}</div>
      {#if actions}<footer>{@render actions()}</footer>{/if}
    </div>
  </dialog>
{/if}

<style>
  .dlg {
    padding: 0;
    border: 1px solid var(--ln2);
    border-radius: 18px;
    background: var(--p2);
    color: var(--ink);
    width: min(540px, calc(100vw - 32px));
    max-height: calc(100dvh - 40px);
    box-shadow: var(--shadow);
    overflow: hidden;
  }
  .dlg.wide { width: min(880px, calc(100vw - 32px)); }
  .dlg::backdrop {
    background: color-mix(in srgb, var(--cv) 72%, transparent);
    backdrop-filter: blur(3px);
  }
  .box { display: flex; flex-direction: column; max-height: calc(100dvh - 42px); }
  header {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 18px 18px 0 22px;
  }
  .tt { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1; }
  h2 { font-size: 17px; margin: 0; overflow-wrap: anywhere; }
  .body { padding: 14px 22px 4px; overflow-y: auto; min-height: 0; }
  footer {
    display: flex;
    justify-content: flex-end;
    flex-wrap: wrap;
    gap: 8px;
    padding: 14px 22px 20px;
  }
</style>
