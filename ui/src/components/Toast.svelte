<script lang="ts">
  import { toastMsg } from "../lib/stores";
</script>

{#if $toastMsg}
  <div class="toast" role="status" aria-live="polite">{$toastMsg}</div>
{/if}

<style>
  .toast {
    position: fixed;
    left: 50%;
    bottom: 28px;
    transform: translateX(-50%);
    background: var(--p3);
    border: 1px solid var(--ln2);
    color: var(--ink);
    padding: 10px 16px;
    border-radius: var(--r);
    font-size: 13px;
    max-width: min(640px, calc(100vw - 32px));
    box-shadow: 0 12px 30px rgba(0, 0, 0, 0.5);
    z-index: 200;
    white-space: pre-line;
  }
  @media (max-width: 760px) {
    .toast { bottom: calc(84px + env(safe-area-inset-bottom, 0px)); }
  }
</style>
