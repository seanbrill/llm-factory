<script lang="ts">
  import { ICONS } from "../lib/icons";
  let { name, size = 16, cls = "" }: { name: string; size?: number; cls?: string } = $props();
</script>

{#if ICONS[name]}
  <svg
    class="ico {cls}"
    viewBox="0 0 24 24"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true">
    {@html ICONS[name]}
  </svg>
{/if}
