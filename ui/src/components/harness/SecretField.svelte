<script lang="ts">
  // A key field: masked until shown, Copy, and New (regenerate) when offered.
  import Icon from "../Icon.svelte";
  import Field from "./Field.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { maskKey } from "../../lib/harness";

  let {
    label,
    secret,
    what,
    onnew,
    busy = false,
  }: { label: string; secret: string; what: string; onnew?: () => void; busy?: boolean } = $props();

  let show = $state(false);
</script>

<Field {label} value={secret ? (show ? secret : maskKey(secret)) : "—"}>
  {#if secret}
    <button
      class="small icon ghost"
      onclick={() => (show = !show)}
      aria-pressed={show}
      aria-label="Show the {what}"
      title={show ? "Hide the key" : "Show the key"}>
      <Icon name={show ? "eyeoff" : "eye"} size={15} />
    </button>
    <CopyButton text={secret} label="Copy" title="Copy the {what}" />
  {/if}
  {#if onnew}
    <button class="small" onclick={onnew} disabled={busy} title="Make a new {what}; the old one stops working">
      {#if busy}<span class="spin" aria-hidden="true"></span>{/if}New
    </button>
  {/if}
</Field>

<style>
  .spin { width: 12px; height: 12px; }
</style>
