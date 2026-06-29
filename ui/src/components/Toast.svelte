<script lang="ts">
  import { toastMsg } from "../lib/stores";
</script>

{#if $toastMsg}
  <div class="toast-pop show">{$toastMsg}</div>
{/if}

<style>
  .toast-pop {
    position: fixed;
    left: 50%;
    bottom: 28px;
    transform: translateX(-50%);
    background: #1d2230;
    border: 1px solid #3a4356;
    color: #e6ebf5;
    padding: 10px 16px;
    border-radius: 10px;
    font-size: 13px;
    box-shadow: 0 12px 30px rgba(0, 0, 0, 0.5);
    z-index: 200;
  }
</style>
