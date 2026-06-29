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
</script>

{#if open}
  <div
    class="modal"
    role="presentation"
    onclick={(e) => { if (e.target === e.currentTarget) close(); }}>
    <div class="modal-box">
      {#if title}<h3>{@html title}</h3>{/if}
      {@render children?.()}
    </div>
  </div>
{/if}

<style>
  .modal {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
  }
  .modal-box {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 14px;
    padding: 20px;
    width: min(520px, 100%);
  }
  .modal-box h3 {
    margin: 0 0 4px;
    font-size: 16px;
  }
</style>
