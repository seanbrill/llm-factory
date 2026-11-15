<script lang="ts">
  import { ICONS } from "../lib/icons";
  let {
    name,
    size = 16,
    cls = "",
    stroke = 1.8,
    label = "",
  }: { name: string; size?: number; cls?: string; stroke?: number; label?: string } = $props();
</script>

{#if ICONS[name]}
  <svg
    class="ico {cls}"
    viewBox="0 0 24 24"
    width={size}
    height={size}
    fill="none"
    stroke="currentColor"
    stroke-width={stroke}
    stroke-linecap="round"
    stroke-linejoin="round"
    role={label ? "img" : undefined}
    aria-label={label || undefined}
    aria-hidden={label ? undefined : "true"}>
    {@html ICONS[name]}
  </svg>
{/if}
