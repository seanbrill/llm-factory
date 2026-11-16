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
  import { homeOf, tildePath, listMcp, mcpAllowed, tokText, type McpServer } from "../../lib/harness";
  import { live, refresh } from "../../lib/live.svelte";
  import { nextFreePort, type LibImage } from "../../lib/library";
  import { containerPort, isRunning } from "../../lib/util";
  import { engineName, computeName, gb } from "../../lib/format";
  import { labelVal } from "../../lib/api";
  import { catalog } from "../../lib/stores";
  import { href } from "../../lib/router";
  import {
    PRESET_LABEL, clean, ctxOptions, ctxText, estimate, getTuning, tuneArg, tuningLine,
    type EstimateResult, type Tuning, type TuningInfo,
  } from "../../lib/tuning";

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
  // Settings for this run (sheet frame f21): the saved tuning, the image's own,
  // or a preset, and the context window, without a rebuild.
  let tinfo = $state<TuningInfo | null>(null);
  let choice = $state("saved"); // "saved" | "baked" | a preset id
  let ctx = $state<number | undefined>(undefined); // context override for this run
  // MCP servers registered on the Harness page, connected for this run.
  let mcpServers = $state<McpServer[]>([]);
  let mcpOn = $state<string[]>([]);

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
    tinfo = null;
    choice = "saved";
    ctx = undefined;
    mcpServers = [];
    mcpOn = [];
    const ref = image.ref, engine = image.engine;
    if (image.harness) {
      listMcp()
        .then((l) => { if (openedFor === engine + ref) mcpServers = l.servers ?? []; })
        .catch(() => { /* no MCP section */ });
    }
    if (tunable) {
      getTuning(ref, engine)
        .then((t) => {
          if (openedFor !== engine + ref) return;
          tinfo = t;
          choice = t.saved ? "saved" : "baked";
        })
        .catch(() => { /* runs with what the image has */ });
    }
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
    const tun = runTuning();
    if (tun) body.tuning = tun;
    if (tinfo && choice === "baked" && tinfo.saved) body.tuning_base = "baked";
    if (h && mcpOn.length) body.mcp = mcpOn;
    busy = true;
    error = "";
    try {
      const r = await post<{ warning?: string; url?: string; port?: number; workspace?: string; mcp_notes?: string[] }>("/api/run", body);
      open = false;
      const ws = (r?.workspace ? ` Its workspace is ${tildePath(r.workspace, home)}.` : "") + (r?.mcp_notes?.length ? " " + r.mcp_notes.join(" ") : "");
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

  // ---- settings for this run ----
  const tunable = $derived.by(() => {
    if (!image || image.ensemble) return false;
    const m = $catalog.find((x) => x.id === labelVal(image.info.Labels, "local-llm.model"));
    return !!m && (m.runtime ?? "cpp") === "cpp" && /^(text|vision|code|reasoning)?$/.test(m.modality ?? "");
  });
  const runPresets = $derived((tinfo?.presets ?? []).filter((p) => p.id === "speed" || p.id === "balanced" || p.id === "long_context"));
  const base = $derived<Tuning>(
    choice === "saved" ? (tinfo?.effective ?? tinfo?.saved ?? {}) :
    choice === "baked" ? (tinfo?.baked ?? {}) :
    (runPresets.find((p) => p.id === choice)?.tuning ?? {}),
  );
  const ctxNow = $derived(ctx ?? base.ctx_size);
  const ctxList = $derived(ctxOptions(tinfo?.model?.trained_ctx ?? 0, ctxNow ?? 0));
  // What this run sends: nothing when it's the default (saved, else baked) as is.
  function runTuning(): Tuning | null {
    if (!tinfo) return null;
    const t: Tuning = choice === "saved" || choice === "baked" ? {} : { ...base, preset: choice as Tuning["preset"] };
    if (ctx && ctx !== base.ctx_size) t.ctx_size = ctx;
    const c = clean(t);
    return Object.keys(c).length ? c : null;
  }
  let runEst = $state<EstimateResult | null>(null);
  $effect(() => {
    if (!open || !image || !tinfo) { runEst = null; return; }
    const t = clean({ ...base, ...(ctx ? { ctx_size: ctx } : {}) });
    const ref = image.ref, engine = image.engine;
    const hd = setTimeout(() => {
      estimate({ ref, engine, tuning: t }).then((r) => (runEst = r)).catch(() => {
        const p = (tinfo?.presets ?? []).find((x) => x.id === choice);
        runEst = p && !ctx ? { tuning: t, estimate: p.estimate, fits: p.fits, warning: p.warning } : null;
      });
    }, 200);
    return () => clearTimeout(hd);
  });
  const memGB = $derived(tinfo?.engine?.mem_gb || 0);
  const memPct = $derived(runEst?.estimate && memGB ? Math.min(100, (runEst.estimate.total_gb / memGB) * 100) : 0);

  // ---- MCP for this run ----
  // What a server's tools cost this image per request: on demand only when
  // its harness takes that (older images get every tool in full).
  const mcpReq = (m: McpServer) => (image?.harness?.on_demand && m.on_demand ? m.request_tokens : m.selected_tokens) || 0;
  const mcpTokens = $derived(mcpServers.filter((m) => mcpOn.includes(m.name)).reduce((a, m) => a + mcpReq(m), 0));
  const mcpOldImage = $derived(!image?.harness?.on_demand && mcpServers.some((m) => mcpOn.includes(m.name) && m.on_demand));
  const runCtx = $derived(ctxNow || 0);
  function toggleMcp(name: string, on: boolean) {
    mcpOn = on ? [...mcpOn, name] : mcpOn.filter((n) => n !== name);
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
  {#if tunable && tinfo}
    <section class="tun" aria-labelledby="run-tun">
      <div class="th"><h3 class="lbl" id="run-tun">Settings for this run</h3><a class="lk" href={href("tune", tuneArg(image!.engine, image!.ref))} onclick={() => (open = false)}>Tune…</a></div>
      <div class="seg" role="radiogroup" aria-label="Settings for this run">
        {#if tinfo.saved}
          <button type="button" role="radio" aria-checked={choice === "saved"} class:on={choice === "saved"} onclick={() => { choice = "saved"; ctx = undefined; }}>
            <b>Saved</b><small>{tinfo.saved.preset && tinfo.saved.preset !== "custom" ? PRESET_LABEL[tinfo.saved.preset] : ctxText(tinfo.effective?.ctx_size)}</small>
          </button>
        {/if}
        <button type="button" role="radio" aria-checked={choice === "baked"} class:on={choice === "baked"} onclick={() => { choice = "baked"; ctx = undefined; }}>
          <b>Image's</b><small>{tinfo.baked?.ctx_size ? ctxText(tinfo.baked.ctx_size) : "as built"}</small>
        </button>
        {#each runPresets as p (p.id)}
          <button type="button" role="radio" aria-checked={choice === p.id} class:on={choice === p.id} disabled={p.fits === false} title={p.fits === false ? "Doesn't fit in the engine's memory" : p.explain}
            onclick={() => { choice = p.id; ctx = undefined; }}>
            <b>{p.label || PRESET_LABEL[p.id]}</b><small>{ctxText(p.tuning.ctx_size)}</small>
          </button>
        {/each}
      </div>
      <div class="ctx">
        <label>
          <span class="lbl">Context window</span>
          <select value={ctxNow ?? ""} onchange={(e) => (ctx = parseInt(e.currentTarget.value, 10) || undefined)}>
            {#if !ctxNow}<option value="">The image's default</option>{/if}
            {#each ctxList as n (n)}<option value={n}>{ctxText(n)} tokens</option>{/each}
          </select>
        </label>
        {#if runEst?.estimate}
          <div class="memb">
            <span class="bar" class:wa={runEst.fits === false || !!runEst.warning}><i style="width:{memPct}%"></i></span>
            <small><b>{gb(runEst.estimate.total_gb)}{memGB ? ` of ${Math.floor(memGB)} GB` : ""}</b> on {engineName(image!.engine)}{tuningLine({ ...base, ctx_size: undefined }) ? ` with ${tuningLine({ ...base, ctx_size: undefined })}` : ""}{runEst.fits === false ? ". It doesn't fit." : runEst.warning ? `. ${runEst.warning}` : ""}</small>
          </div>
        {:else}
          <p class="hint">{tuningLine({ ...base, ctx_size: undefined }) || "The image's own settings."}</p>
        {/if}
      </div>
    </section>
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
      <div class="mh"><h3 class="lbl" id="run-mcp"><Icon name="plug" size={13} />MCP servers</h3><a class="lk" href={href("harness")} onclick={() => (open = false)}>{mcpServers.length ? "Choose tools on the Harness page" : "Add one on the Harness page"}</a></div>
      {#if mcpServers.length}
        <div class="mcps" role="group" aria-labelledby="run-mcp">
          {#each mcpServers as m (m.name)}
            {@const allowed = mcpAllowed(m)}
            {@const none = m.tools != null && m.tools.length === 0}
            <label class="mcp" class:on={mcpOn.includes(m.name)} class:dis={none}>
              <input type="checkbox" checked={mcpOn.includes(m.name)} disabled={none} onchange={(e) => toggleMcp(m.name, e.currentTarget.checked)} />
              <span class="mt">
                <b>{m.name}</b>
                <small>{none ? "No tools chosen yet: pick some on the Harness page" : m.catalog?.length ? `${allowed.length} of its ${m.catalog.length} tools` : m.error ? "Its last test failed" : "Not tested yet"}</small>
              </span>
              {#if !none && m.selected_tokens}<span class="chip" class:ok={mcpOn.includes(m.name)}>{tokText(mcpReq(m))} tokens{image?.harness?.on_demand && m.on_demand ? " · on demand" : ""}</span>{/if}
            </label>
          {/each}
        </div>
        {#if mcpTokens}
          <p class="hint" class:warnt={runCtx > 0 && mcpTokens > runCtx * 0.25}>
            Their tool descriptions take {tokText(mcpTokens)} tokens of every request{runCtx ? `, ${Math.round((mcpTokens / runCtx) * 100)}% of a ${ctxText(runCtx)} context` : ""}.
          </p>
        {/if}
        {#if mcpOldImage}
          <p class="hint warnt">This image was built before tools on demand, so it gets every picked tool in full. Rebuild it to send them on demand.</p>
        {/if}
      {/if}
      <p class="hint">While it runs you can add more from any chat, Open WebUI included: type <span class="mono">/mcp help</span>.</p>
      {#if h.workspace}
        <label class="full">
          <span class="lbl">Workspace folder</span>
          <input bind:value={workspace} placeholder={defaultWs || "workspace/ in the factory folder"} spellcheck="false" autocomplete="off" aria-describedby="run-ws" />
        </label>
        <p class="hint" id="run-ws">
          Mounted read-write at <span class="mono">/workspace</span>, where the file{h.tools.includes("shell") ? " and shell" : ""} tools work, so what the
          model writes stays after the container stops. Blank means the factory's <span class="mono">workspace/</span> folder. Another project folder
          works too: its full path, or <span class="mono">~/…</span> inside your home folder. Create it first. System, hidden and whole home
          folders are refused.
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
  .tun { margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--ln); display: flex; flex-direction: column; gap: 10px; }
  .th { display: flex; align-items: center; gap: 10px; }
  .th h3 { margin: 0; }
  .lk { margin-left: auto; font-size: 12.5px; color: var(--pr); font-weight: 600; }
  .seg { display: flex; gap: 3px; padding: 3px; background: var(--p2); border: 1px solid var(--ln2); border-radius: var(--r); }
  .seg button { flex: 1; height: auto; display: flex; flex-direction: column; align-items: center; gap: 1px; padding: 7px 8px; border: 0; border-radius: var(--rs); background: transparent; color: var(--mu); }
  .seg button b { font-size: 13px; font-weight: 600; }
  .seg button small { font-size: 11.5px; color: var(--fa); font-family: var(--font-mono); }
  .seg button.on { background: var(--p3); color: var(--ink); box-shadow: inset 0 0 0 1px var(--pr); }
  .seg button.on small { color: var(--pr); }
  .seg button:disabled { opacity: 0.5; cursor: not-allowed; }
  .ctx { display: grid; grid-template-columns: auto minmax(0, 1fr); gap: 16px; align-items: end; }
  .ctx select { min-width: 180px; }
  .memb { display: flex; flex-direction: column; gap: 6px; padding-bottom: 3px; }
  .memb small { font-size: 12px; color: var(--mu); }
  .memb b { color: var(--ink); font-family: var(--font-mono); font-weight: 500; }
  .mh { display: flex; align-items: center; gap: 10px; margin-top: 6px; }
  .mh h3 { margin: 0; display: flex; align-items: center; gap: 6px; }
  .mcps { display: flex; flex-direction: column; gap: 6px; }
  .mcp { display: flex; flex-direction: row; align-items: center; gap: 12px; padding: 9px 12px; border: 1px solid var(--ln2); border-radius: var(--r); background: var(--p2); cursor: pointer; }
  .mcp.on { border-color: color-mix(in srgb, var(--pr) 55%, transparent); }
  .mcp.dis { opacity: 0.7; cursor: not-allowed; }
  .mcp input { accent-color: var(--pr); }
  .mt { display: flex; flex-direction: column; gap: 1px; min-width: 0; margin-right: auto; }
  .mt b { font-size: 13.5px; }
  .mt small { font-size: 12px; color: var(--mu); }
  .warnt { color: var(--wa); }
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
    .ctx { grid-template-columns: 1fr; }
    .seg { flex-wrap: wrap; }
  }
</style>
