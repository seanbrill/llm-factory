<script lang="ts">
  // Start a container from an image: port, a persona or one-off system prompt
  // for this instance, and whether to enforce it. The run guardrail (409 +
  // needs_force) comes back as a warning with "Start anyway". An image with
  // baked tools says what it runs, and with the file or shell tools which host
  // folder the factory mounts at /workspace (its workspace/ folder by default).
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { api, post, type ApiError } from "../../lib/api";
  import { personas, sysinfo, toast } from "../../lib/stores";
  import { searchName, toolsText } from "../../lib/baked";
  import { homeOf, tildePath } from "../../lib/harness";
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
  let workspace = $state(""); // host folder for /workspace ("" = the factory's workspace/)

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
    workspace = "";
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
    if (h?.workspace && workspace.trim()) body.workspace = workspace.trim();
    busy = true;
    error = "";
    try {
      const r = await post<{ warning?: string; url?: string; port?: number; workspace?: string }>("/api/run", body);
      open = false;
      const ws = r?.workspace ? ` Its workspace is ${tildePath(r.workspace, home)}.` : "";
      if (r?.warning) toast(r.warning + ws, 9000);
      else toast(`${name} is starting on :${port}.${r?.url ? " Also at " + r.url : ""}${ws}`, 5000);
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

  // ---- baked tools ----
  const h = $derived(image?.harness ?? null);
  const home = $derived(homeOf(live.harness?.terminal));
  const defaultWs = $derived(tildePath(live.harness?.terminal?.default_workspace ?? "", home));
  const linux = $derived($sysinfo?.host_os === "linux");
  // To run it without the factory: the same mount, from the factory folder.
  const selfRun = $derived.by(() => {
    if (!image || !h) return "";
    const eng = image.engine === "podman" ? "podman" : "docker";
    const ws = workspace.trim() ? `"${workspace.trim()}:/workspace"` : `"$PWD/workspace:/workspace"`;
    return `${eng} run --rm -p 127.0.0.1:${port || 8080}:8080${h.workspace ? ` -v ${ws}` : ""} ${image.ref}`;
  });
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
  {#if h}
    <section class="tools" aria-labelledby="run-tools">
      <h3 class="lbl" id="run-tools"><Icon name="wrench" size={13} />Tools in this image</h3>
      <p class="sub">
        It runs {h.tools.length ? toolsText(h.tools) : "no tools (all are off)"} itself{h.search ? `, searching with ${searchName(h.search)}` : ""}. Any
        OpenAI-compatible client gets them at <span class="mono">http://localhost:{port || 8080}/v1</span>; no Open WebUI needed.
      </p>
      {#if h.tools.includes("shell")}
        <p class="sub warn"><Icon name="warn" size={13} /><span>The shell is on: the model can run commands in the container and change files in the workspace.</span></p>
      {/if}
      {#if h.workspace}
        <label class="full">
          <span class="lbl">Workspace folder</span>
          <input bind:value={workspace} placeholder={defaultWs || "workspace/ in the factory folder"} spellcheck="false" autocomplete="off" aria-describedby="run-ws" />
        </label>
        <p class="hint" id="run-ws">
          Mounted read-write at <span class="mono">/workspace</span>, where the file{h.tools.includes("shell") ? " and shell" : ""} tools work, so what the
          model writes stays after the container stops. Blank means the factory's <span class="mono">workspace/</span> folder. Another project folder
          works too (its full path); system, hidden and whole home folders don't.
        </p>
        {#if linux}
          <p class="hint">On Linux the model runs as user 10001, which needs to be able to write there. In the factory folder:</p>
          <CodeLine cmd="sudo chown 10001 workspace" />
        {/if}
      {/if}
      <details class="self">
        <summary><Icon name="chevronRight" size={13} />Run it without the factory</summary>
        <p class="hint">From the factory folder{h.workspace ? ", with the same folder at /workspace" : ""}:</p>
        <CodeLine cmd={selfRun} />
        <p class="hint">Add <span class="mono">-e HARNESS_SEARXNG_URL=…</span> to search with your SearXNG, or <span class="mono">-e HARNESS=off</span> to run it without tools.</p>
      </details>
    </section>
  {/if}
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
  .tools { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--ln); display: flex; flex-direction: column; gap: 8px; }
  .tools h3 { margin: 0; display: flex; align-items: center; gap: 6px; }
  .tools .sub { line-height: 1.5; }
  .tools .mono { font-size: 12px; color: var(--ink); }
  .tools .full { margin-top: 4px; }
  .warn { display: grid; grid-template-columns: 13px minmax(0, 1fr); gap: 6px; }
  .warn :global(.ico) { margin-top: 3px; color: var(--wa); }
  .hint { font-size: 12.5px; color: var(--mu); line-height: 1.45; }
  .self summary { display: flex; align-items: center; gap: 6px; cursor: pointer; font-size: 13px; font-weight: 500; color: var(--ink); list-style: none; }
  .self summary::-webkit-details-marker { display: none; }
  .self summary :global(.ico) { color: var(--fa); transition: transform 0.15s; }
  .self[open] summary :global(.ico) { transform: rotate(90deg); }
  .self[open] { display: flex; flex-direction: column; gap: 8px; }
  .self :global(.code) { align-items: flex-start; }
  .self :global(.code > span) { white-space: normal; overflow-wrap: anywhere; }
  @media (max-width: 520px) {
    .grid { grid-template-columns: 1fr; }
  }
</style>
