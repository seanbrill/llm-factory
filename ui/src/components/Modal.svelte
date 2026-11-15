<script lang="ts">
  import type { Snippet } from "svelte";
  let {
    open = $bindable(false),
    title = "",
    children,
    onclose,
  }: { open?: boolean; title?: string; children?: Snippet; onclose?: () => void } = $props();

  function close() {
    open = false;
    onclose?.();
  }
  const hid = "modal-" + Math.random().toString(36).slice(2, 8);
  function onkey(e: KeyboardEvent) {
    if (open && e.key === "Escape") {
      e.preventDefault();
      close();
    }
  }
</script>

<svelte:window onkeydown={onkey} />

{#if open}
  <div
    class="modal"
    role="presentation"
    onclick={(e) => { if (e.target === e.currentTarget) close(); }}>
    <div class="modal-box" role="dialog" aria-modal="true" aria-labelledby={title ? hid : undefined}>
      {#if title}<h3 id={hid}>{@html title}</h3>{/if}
      {@render children?.()}
    </div>
  </div>
{/if}

<style>
  .modal {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: color-mix(in srgb, var(--cv) 72%, transparent);
    backdrop-filter: blur(3px);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
  }
  .modal-box {
    background: var(--p2);
    border: 1px solid var(--ln2);
    border-radius: 18px;
    padding: 22px;
    width: min(520px, 100%);
    max-height: calc(100dvh - 40px);
    overflow-y: auto;
    box-shadow: var(--shadow);
  }
  .modal-box h3 {
    margin: 0 0 6px;
    font-size: 17px;
  }
</style>
