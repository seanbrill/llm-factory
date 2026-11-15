<script lang="ts">
  // Start a container from an image: port, a persona or one-off system prompt
  // for this instance, and whether to enforce it. The run guardrail (409 +
  // needs_force) comes back as a warning with "Start anyway".
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import { api, post, type ApiError } from "../../lib/api";
  import { personas, toast } from "../../lib/stores";
  import { live, refresh } from "../../lib/live.svelte";
  import { nextFreePort, type LibImage } from "../../lib/library";
  import { containerPort, isRunning } from "../../lib/util";
  import { engineName, computeName } from "../../lib/format";

  let {
    open = $bindable(false),
    image,
    name,
    onstarted,
  }: { open?: boolean; image: LibImage | null; name: string; onstarted?: (port: number) => void } = $props();

  let port = $state(8080);
  let persona = $state("");
  let prompt = $state("");
  let always = $state(false);
  let baked = $state("");
  let busy = $state(false);
  let error = $state("");
  let guard = $state(""); // the guardrail's warning, awaiting "Start anyway"

  // Reset each time it opens for an image.
  let openedFor = "";
  $effect(() => {
    if (!open || !image) {
      openedFor = "";
      return;
    }
    const key = image.engine + image.ref;
    if (openedFor === key) return;
    openedFor = key;
    port = nextFreePort(live.containers);
    persona = "";
    prompt = "";
    always = false;
    error = "";
    guard = "";
    baked = "";
    const ref = image.ref, engine = image.engine;
    api<{ system_prompt?: string }>(`/api/image/config?ref=${encodeURIComponent(ref)}&engine=${encodeURIComponent(engine)}`)
      .then((c) => { if (openedFor === engine + ref) baked = c.system_prompt ?? ""; })
      .catch(() => { /* the field still works without the hint */ });
  });

  // A running model already on this port would be replaced: the factory removes
  // whatever container holds the name localllm-<port> before it starts.
  const clash = $derived(live.containers.find((c) => parseInt(containerPort(c), 10) === port && isRunning(c)));

  function onPersona() {
    const p = $personas.find((x) => x.id === persona);
    prompt = p ? p.prompt : "";
  }

  async function start(force = false) {
    if (!image) return;
    if (!port || port < 1 || port > 65535) {
      error = "Pick a port between 1 and 65535.";
      return;
    }
    const body: Record<string, unknown> = { ref: image.ref, port, engine: image.engine };
    if (prompt.trim()) body.system_prompt = prompt.trim();
    if (prompt.trim() && always) body.inject_mode = "always";
    if (force) body.force = true;
    busy = true;
    error = "";
    try {
      const r = await post<{ warning?: string; url?: string; port?: number }>("/api/run", body);
      open = false;
      if (r?.warning) toast(r.warning, 9000);
      else toast(`${name} is starting on :${port}.${r?.url ? " Also at " + r.url : ""}`, 5000);
      onstarted?.(port);
      refresh("containers", "resources");
      setTimeout(() => refresh("containers"), 1500);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 409 && err.data?.needs_force) guard = err.message;
      else error = err.message || "The factory didn't answer.";
    } finally {
      busy = false;
    }
  }
</script>

<Dialog bind:open title={`Run ${name}`} sub={image ? `${image.ref} · ${engineName(image.engine)} · ${computeName(image.compute)}` : ""}>
  <div class="grid">
    <label>
      <span class="lbl">Port</span>
      <input type="number" min="1" max="65535" bind:value={port} />
    </label>
    <label>
      <span class="lbl">Persona</span>
      <select bind:value={persona} onchange={onPersona}>
        <option value="">None: the image's own prompt</option>
        {#each $personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>
  </div>
  {#if clash}
    <div class="callout wa">
      <Icon name="warn" />
      <span><b>:{port} is in use by a running model.</b><p>Starting this one stops {clash.Names || "it"}. Pick another port to keep both.</p></span>
    </div>
  {/if}
  <label class="full">
    <span class="lbl">System prompt for this instance</span>
    <textarea rows="5" bind:value={prompt} placeholder={baked ? `Leave blank to keep the image's prompt: “${baked.slice(0, 160)}${baked.length > 160 ? "…" : ""}”` : "Leave blank to keep the image's own prompt."}></textarea>
  </label>
  <label class="check">
    <input type="checkbox" bind:checked={always} disabled={!prompt.trim()} />
    <span>Always use this prompt, even when a client sends its own system message</span>
  </label>
  {#if guard}
    <div class="callout wa">
      <Icon name="warn" />
      <span><b>{guard}</b><p>You can start it anyway; it may crash if the machine runs out of memory.</p></span>
    </div>
  {/if}
  {#if error}
    <div class="callout er" role="alert">
      <Icon name="errc" />
      <span><b>It didn't start.</b><p>{error}</p></span>
    </div>
  {/if}
  {#snippet actions()}
    <button class="ghost" type="button" onclick={() => (open = false)}>Cancel</button>
    {#if guard}
      <button class="primary" type="button" disabled={busy} onclick={() => start(true)}>{busy ? "Starting…" : "Start anyway"}</button>
    {:else}
      <button class="primary" type="button" disabled={busy} onclick={() => start()}><Icon name="play" size={14} />{busy ? "Starting…" : "Run"}</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .grid { display: grid; grid-template-columns: 140px 1fr; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 6px; }
  .full { margin-top: 12px; }
  .check { flex-direction: row; align-items: flex-start; gap: 9px; margin-top: 12px; color: var(--mu); }
  .check input { margin-top: 2px; }
  .callout { margin-top: 12px; }
  @media (max-width: 520px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
