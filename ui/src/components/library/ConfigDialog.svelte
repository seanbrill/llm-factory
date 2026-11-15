<script lang="ts">
  // The settings an image was built with (GET /api/image/config: its labels),
  // with "Use as template" to build another one like it.
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import { api } from "../../lib/api";
  import { catalog } from "../../lib/stores";
  import { engineName, computeName } from "../../lib/format";
  import type { LibImage } from "../../lib/library";

  interface ImageConfig {
    repository: string;
    tag: string;
    model_id: string;
    engine: string;
    compute: string;
    inject_mode: string;
    system_prompt: string;
    ctx_size: string;
    memory_gb: string;
    route: string;
    autostart: boolean;
  }

  let {
    open = $bindable(false),
    image,
    ontemplate,
  }: { open?: boolean; image: LibImage | null; ontemplate?: (im: LibImage) => void } = $props();

  let cfg = $state<ImageConfig | null>(null);
  let error = $state("");
  let loading = $state(false);

  let loadedFor = "";
  $effect(() => {
    if (!open || !image) {
      loadedFor = "";
      return;
    }
    const key = image.engine + image.ref;
    if (loadedFor === key) return;
    loadedFor = key;
    cfg = null;
    error = "";
    loading = true;
    api<ImageConfig>(`/api/image/config?ref=${encodeURIComponent(image.ref)}&engine=${encodeURIComponent(image.engine)}`)
      .then((c) => { if (loadedFor === key) cfg = c; })
      .catch((e) => { if (loadedFor === key) error = (e as Error).message || "The factory didn't answer."; })
      .finally(() => { if (loadedFor === key) loading = false; });
  });

  const modelName = $derived(cfg ? $catalog.find((m) => m.id === cfg!.model_id)?.name ?? cfg.model_id : "");
  const ctx = (s: string) => (parseInt(s, 10) > 0 ? `${parseInt(s, 10).toLocaleString()} tokens` : "The model's default");
  const mem = (s: string) => (parseFloat(s) > 0 ? `${parseFloat(s)} GB` : "No limit");
</script>

<Dialog bind:open title="Build settings" sub={image?.ref ?? ""}>
  {#if error}
    <div class="callout er" role="alert">
      <Icon name="errc" />
      <span><b>Couldn't read the image's settings.</b><p>{error}. If the image was just deleted, refresh the Library.</p></span>
    </div>
  {:else if loading || !cfg}
    <p class="empty"><span class="spin"></span> Reading the image…</p>
  {:else}
    <dl>
      <dt>Model</dt><dd>{modelName || "—"}</dd>
      <dt>Runs on</dt><dd>{engineName(cfg.engine)} · {computeName(cfg.compute)}</dd>
      <dt>Context</dt><dd>{ctx(cfg.ctx_size)}</dd>
      <dt>Memory limit</dt><dd>{mem(cfg.memory_gb)}</dd>
      <dt>Local URL</dt><dd>{#if cfg.route}<span class="mono">{cfg.route}.localhost</span>{:else}—{/if}</dd>
      <dt>Starts with the engine</dt><dd>{cfg.autostart ? "Yes" : "No"}</dd>
      <dt>System prompt</dt>
      <dd>
        {#if cfg.system_prompt}
          <p class="prompt">{cfg.system_prompt}</p>
          <span class="sub">{cfg.inject_mode === "always" ? "Always used, even when a client sends its own." : "Used when a client sends no system message."}</span>
        {:else}None{/if}
      </dd>
    </dl>
  {/if}
  {#snippet actions()}
    <button class="ghost" type="button" onclick={() => (open = false)}>Close</button>
    <button class="primary" type="button" disabled={!image} onclick={() => { if (image) { open = false; ontemplate?.(image); } }}>
      <Icon name="copy" size={14} />Use as template
    </button>
  {/snippet}
</Dialog>

<style>
  dl { display: grid; grid-template-columns: 170px minmax(0, 1fr); gap: 10px 14px; margin: 0; font-size: 13.5px; }
  dt { color: var(--mu); }
  dd { margin: 0; min-width: 0; }
  .prompt {
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--r);
    padding: 8px 10px;
    margin-bottom: 4px;
    max-height: 200px;
    overflow: auto;
  }
  .empty { display: flex; align-items: center; gap: 8px; }
  @media (max-width: 520px) {
    dl { grid-template-columns: 1fr; gap: 2px; }
    dd { margin-bottom: 8px; }
  }
</style>
