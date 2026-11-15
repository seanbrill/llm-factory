<script lang="ts">
  // The tail of a container's log (GET /api/container/logs), e.g. to see why a
  // model stopped right after it started.
  import { tick } from "svelte";
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { api } from "../../lib/api";
  import type { LibContainer } from "../../lib/library";

  const TAIL = 400;
  let { open = $bindable(false), container, name }: { open?: boolean; container: LibContainer | null; name: string } = $props();

  let text = $state("");
  let loading = $state(false);
  let error = $state("");
  let pre: HTMLPreElement | undefined = $state();

  async function load() {
    if (!container) return;
    loading = true;
    error = "";
    try {
      const q = `id=${encodeURIComponent(container.id || container.name)}&engine=${encodeURIComponent(container.engine)}&tail=${TAIL}`;
      text = (await api<{ logs: string }>("/api/container/logs?" + q)).logs ?? "";
      await tick();
      if (pre) pre.scrollTop = pre.scrollHeight;
    } catch (e) {
      error = (e as Error).message || "The factory didn't answer.";
    } finally {
      loading = false;
    }
  }

  let loadedFor = "";
  $effect(() => {
    if (!open || !container) {
      loadedFor = "";
      return;
    }
    if (loadedFor === container.id) return;
    loadedFor = container.id;
    text = "";
    load();
  });
</script>

<Dialog bind:open wide title={`Log: ${name}`} sub={container ? `${container.name} · ${container.status} · last ${TAIL} lines` : ""}>
  {#if error}
    <div class="callout er" role="alert">
      <Icon name="errc" />
      <span><b>Couldn't read the log.</b><p>{error}. The container may have been removed; refresh the Library and try again.</p></span>
    </div>
  {:else if loading && !text}
    <p class="empty"><span class="spin"></span> Reading the log…</p>
  {:else}
    <pre class="log" bind:this={pre} aria-label="Container log">{text || "(no output yet)"}</pre>
  {/if}
  {#snippet actions()}
    {#if text}<CopyButton text={text} label="Copy log" cls="" />{/if}
    <button type="button" onclick={load} disabled={loading}><Icon name="refresh" size={14} />{loading ? "Reading…" : "Refresh"}</button>
    <button class="primary" type="button" onclick={() => (open = false)}>Done</button>
  {/snippet}
</Dialog>

<style>
  .log {
    margin: 0;
    max-height: min(60dvh, 560px);
    overflow: auto;
    padding: 12px 14px;
    border-radius: var(--r);
    border: 1px solid var(--ln);
    background: color-mix(in srgb, var(--cv) 70%, black);
    font: 12px/1.55 var(--font-mono);
    color: var(--ink);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .empty { display: flex; align-items: center; gap: 8px; }
</style>
