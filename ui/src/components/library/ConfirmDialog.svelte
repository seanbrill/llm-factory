<script lang="ts">
  // "Are you sure?" for anything that removes something: says what goes and what
  // it frees, runs the action, and keeps the dialog open with the error if it fails.
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import type { ConfirmSpec } from "../../lib/library";

  let { open = $bindable(false), spec }: { open?: boolean; spec: ConfirmSpec | null } = $props();
  let busy = $state(false);
  let error = $state("");

  $effect(() => {
    if (open) {
      error = "";
      busy = false;
    }
  });

  async function go() {
    if (!spec) return;
    busy = true;
    error = "";
    try {
      await spec.run();
      open = false;
    } catch (e) {
      error = (e as Error).message || "The factory didn't answer.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title={spec?.title ?? ""}>
  {#if spec}
    <p class="body">{spec.body}</p>
    {#if spec.frees}<p class="frees"><Icon name="disk" size={15} />{spec.frees}</p>{/if}
    {#if spec.note}
      <div class="callout wa"><Icon name="warn" /><span><b>{spec.note}</b></span></div>
    {/if}
    {#if error}
      <div class="callout er" role="alert"><Icon name="errc" /><span><b>That didn't work.</b><p>{error}</p></span></div>
    {/if}
  {/if}
  {#snippet actions()}
    <button class="ghost" type="button" onclick={() => (open = false)}>Cancel</button>
    <button class={spec?.danger ? "danger" : "primary"} type="button" disabled={busy} onclick={go}>
      {#if busy}<span class="spin"></span>{/if}{spec?.confirm ?? "OK"}
    </button>
  {/snippet}
</Dialog>

<style>
  .body { color: var(--ink); font-size: 14px; }
  .frees { display: flex; align-items: center; gap: 8px; margin-top: 10px; color: var(--mu); font-size: 13px; }
  .callout { margin-top: 12px; }
</style>
