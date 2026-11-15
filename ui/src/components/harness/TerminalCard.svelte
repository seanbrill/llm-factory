<script lang="ts">
  // The workspace terminal (Open Terminal in a container; sheet frame f12): its
  // image, the folder the model can edit, Claude skills sharing and limits — each
  // saved as you change it — plus start/stop, logs, the address and key Open
  // WebUI uses, and what the model can see.
  import Icon from "../Icon.svelte";
  import Field from "./Field.svelte";
  import SecretField from "./SecretField.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { engineName } from "../../lib/format";
  import {
    terminalView,
    terminalLogs,
    homeOf,
    tildePath,
    mbText,
    sentence,
    TONE_CHIP,
    type HarnessStatus,
  } from "../../lib/harness";
  import { hs, start, stop, restart, cancelStart, remove, newTerminalKey, saveSetting } from "./state.svelte";

  let { st }: { st: HarnessStatus } = $props();

  const uid = $props.id();
  const t = $derived(st.terminal);
  const view = $derived(terminalView(t));
  const running = $derived(!!t.container?.running);
  const exists = $derived(!!t.container?.exists);
  const opActive = $derived(!!t.op?.active);
  const home = $derived(homeOf(t));
  const variants = $derived(t.variants ?? []);
  const variant = $derived(variants.find((v) => v.id === t.variant));
  const locked = $derived(opActive || !!hs.busy);

  // ---- settings: each saves when you change it --------------------------------
  let editing = $state<"" | "workspace" | "limits">("");
  let wsDraft = $state("");
  let memDraft = $state(0);
  let portDraft = $state(0);
  let fieldError = $state("");
  let wsInput: HTMLInputElement | undefined = $state();
  let memInput: HTMLInputElement | undefined = $state();

  function openEditor(which: "workspace" | "limits") {
    fieldError = "";
    editing = which;
    if (which === "workspace") {
      wsDraft = t.workspace || "";
      queueMicrotask(() => wsInput?.focus());
    } else {
      memDraft = t.memory_mb;
      portDraft = t.port;
      queueMicrotask(() => memInput?.focus());
    }
  }

  function closeEditor() {
    editing = "";
    fieldError = "";
  }

  // In an editor's inputs: Escape cancels, Enter saves.
  function onEditorKey(e: KeyboardEvent, save: () => void) {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      closeEditor();
    } else if (e.key === "Enter") {
      e.preventDefault();
      save();
    }
  }

  async function saveWorkspace(value: string) {
    fieldError = await saveSetting({ workspace: value.trim() });
    if (!fieldError) editing = "";
  }

  async function saveLimits() {
    const mem = Math.round(Number(memDraft));
    const port = Math.round(Number(portDraft));
    if (!Number.isFinite(mem) || mem < 512) {
      fieldError = "Give it at least 512 MB.";
      return;
    }
    if (!Number.isFinite(port) || port < 1024 || port > 65535) {
      fieldError = "Pick a port from 1024 to 65535.";
      return;
    }
    // 0 keeps following the image's default (2 GB Slim, 4 GB Full).
    const def = variant?.memory_mb ?? 0;
    fieldError = await saveSetting({ memory_mb: mem === def ? 0 : mem, port });
    if (!fieldError) editing = "";
  }

  let variantError = $state("");
  async function pickVariant(id: string) {
    if (id === t.variant) return;
    variantError = await saveSetting({ variant: id });
  }

  let shareError = $state("");
  async function toggleShare(e: Event) {
    const on = (e.currentTarget as HTMLInputElement).checked;
    shareError = await saveSetting({ share_claude_skills: on });
  }

  // ---- logs ----------------------------------------------------------------------
  let showLogs = $state(false);
  let logs = $state("");
  let logsBusy = $state(false);
  async function loadLogs() {
    logsBusy = true;
    try {
      const r = await terminalLogs(300);
      logs = r.logs || (r.note ? r.note.charAt(0).toUpperCase() + r.note.slice(1) + "." : "No output yet.");
    } catch (e) {
      logs = "Couldn't read the logs: " + ((e as Error).message || "the factory didn't answer") + ".";
    } finally {
      logsBusy = false;
    }
  }
  function toggleLogs() {
    showLogs = !showLogs;
    if (showLogs) loadLogs();
  }

  const ERR_TITLE: Partial<Record<string, string>> = {
    start: "The terminal didn't start.",
    update: "The image wasn't updated.",
    stop: "The terminal didn't stop.",
    remove: "The container wasn't removed.",
    termkey: "No new key was made.",
  };

  const elapsed = (ms: number) => (ms < 60_000 ? `${Math.round(ms / 1000)} s` : `${Math.floor(ms / 60_000)} min ${Math.round((ms % 60_000) / 1000)} s`);
  const startText = $derived(exists ? "Start" : t.image_present ? "Create and start" : `Download${variant?.size ? ` (${variant.size})` : ""} and start`);
</script>

<section class="card" id="h-terminal" tabindex="-1" aria-labelledby="{uid}-title">
  <div class="hd">
    <h2 id="{uid}-title">Workspace terminal</h2>
    <span class="chip {TONE_CHIP[view.tone]}">
      {#if view.tone === "ok"}<span class="dot" aria-hidden="true"></span>{/if}{view.label}
    </span>
  </div>

  <!-- Image: Slim or Full -->
  <div class="segc" role="radiogroup" aria-label="Terminal image">
    {#each variants as v (v.id)}
      <label class:on={t.variant === v.id} title={v.desc}>
        <input
          type="radio"
          name="{uid}-variant"
          value={v.id}
          checked={t.variant === v.id}
          disabled={locked}
          onchange={() => pickVariant(v.id)} />
        <b>{v.label}</b>
        <span>{v.size}</span>
      </label>
    {/each}
  </div>
  {#if variant}<p class="vdesc">{variant.desc}</p>{/if}
  {#if variantError}<p class="ferr" role="alert">{variantError}</p>{/if}

  <!-- Workspace folder -->
  {#if editing === "workspace"}
    <div class="editor" role="group" aria-label="Workspace folder">
      <label class="el" for="{uid}-ws">Folder the model can read and edit</label>
      <input
        id="{uid}-ws"
        type="text"
        bind:this={wsInput}
        bind:value={wsDraft}
        placeholder={t.default_workspace}
        spellcheck="false"
        autocomplete="off"
        onkeydown={(e) => onEditorKey(e, () => saveWorkspace(wsDraft))} />
      <p class="hint">
        A folder on this computer (its full path, or <span class="mono">~/…</span> inside your home folder), shared
        read-write as <span class="mono">~/workspace</span>. Leave it empty for the
        factory's own <span class="mono">workspace/</span> folder. Your home folder, hidden folders, system folders and the
        rest of the factory folder are refused. Create the folder first.
      </p>
      {#if fieldError}<p class="ferr" role="alert">{fieldError}</p>{/if}
      <div class="ebtn">
        <button class="primary small" onclick={() => saveWorkspace(wsDraft)} disabled={hs.busy === "settings"}>Save</button>
        {#if t.workspace}
          <button class="small" onclick={() => saveWorkspace("")} disabled={hs.busy === "settings"}>Use the default</button>
        {/if}
        <button class="small ghost" onclick={closeEditor}>Cancel</button>
      </div>
    </div>
  {:else}
    <Field
      label="Folder the model can read and edit"
      value={tildePath(t.workspace_path, home) || "—"}
      full={t.workspace_path}>
      <button class="small" onclick={() => openEditor("workspace")} disabled={locked}>Change</button>
    </Field>
  {/if}

  <div class="rows">
    <!-- Share ~/.claude/skills -->
    <div class="row">
      <label class="rl" for="{uid}-share">
        Share my Claude skills
        <small>
          {#if t.claude_skills_path}
            {tildePath(t.claude_skills_path, home)}, read-only
          {:else}
            The factory doesn't know your home folder: restart it with the current start script
          {/if}
        </small>
      </label>
      <input
        id="{uid}-share"
        type="checkbox"
        class="tg"
        checked={t.share_claude_skills}
        disabled={locked || !t.claude_skills_path}
        onchange={toggleShare} />
    </div>
    {#if shareError}<p class="ferr" role="alert">{shareError}</p>{/if}

    <!-- Limits: memory and port -->
    {#if editing === "limits"}
      <div class="editor" role="group" aria-label="Limits">
        <div class="two">
          <span>
            <label class="el" for="{uid}-mem">Memory limit (MB)</label>
            <input
              id="{uid}-mem"
              type="number"
              min="512"
              step="256"
              bind:this={memInput}
              bind:value={memDraft}
              onkeydown={(e) => onEditorKey(e, saveLimits)} />
          </span>
          <span>
            <label class="el" for="{uid}-port">Port on 127.0.0.1</label>
            <input
              id="{uid}-port"
              type="number"
              min="1024"
              max="65535"
              bind:value={portDraft}
              onkeydown={(e) => onEditorKey(e, saveLimits)} />
          </span>
        </div>
        <p class="hint">
          {#if variant}The {variant.label} image starts with {mbText(variant.memory_mb)}. {/if}The terminal also has a limit
          of 1024 processes. Its port is published on this computer's loopback only.
        </p>
        {#if fieldError}<p class="ferr" role="alert">{fieldError}</p>{/if}
        <div class="ebtn">
          <button class="primary small" onclick={saveLimits} disabled={hs.busy === "settings"}>Save</button>
          <button class="small ghost" onclick={closeEditor}>Cancel</button>
        </div>
      </div>
    {:else}
      <div class="row">
        <span class="rl">
          Limits
          <small>{mbText(t.memory_mb)} memory · 127.0.0.1:{t.port} only</small>
        </span>
        <button class="small" onclick={() => openEditor("limits")} disabled={locked}>Edit</button>
      </div>
    {/if}
  </div>

  <!-- What needs you -->
  {#if hs.error && hs.errorFrom !== "gwkey"}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span>
        <b>{ERR_TITLE[hs.errorFrom] ?? "That didn't work."}</b>
        <p>{sentence(hs.error)}</p>
        <button class="small ghost dismiss" onclick={() => (hs.error = "")}>Dismiss</button>
      </span>
    </div>
  {/if}
  {#if t.spec_error}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span><b>These settings can't start a terminal.</b><p>{sentence(t.spec_error)}</p></span>
    </div>
  {/if}
  {#if t.error}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span><b>The container engine didn't answer.</b><p>{sentence(t.error)}</p></span>
    </div>
  {/if}
  {#if t.restart_required && running && !opActive}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span>
        <b>Restart to apply the new settings.</b>
        <p>It still runs with the old ones. Restarting recreates the container; files in the workspace are kept.</p>
        <button class="small" onclick={() => start(false)} disabled={!!hs.busy}>Restart now</button>
      </span>
    </div>
  {/if}
  {#if t.op?.error && !opActive}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>The terminal didn't start.</b>
        <p>
          {sentence(t.op.error)}
          {#if /port/i.test(t.op.error)}Pick another port under Limits, then start it again.{:else}Fix what it says, then start it again.{/if}
        </p>
        {#if t.op.detail}<pre class="log small">{t.op.detail}</pre>{/if}
        {#if /port/i.test(t.op.error)}<button class="small" onclick={() => openEditor("limits")} disabled={locked}>Change the port</button>{/if}
      </span>
    </div>
  {:else if t.container?.oom_killed && !running && !opActive}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>It stopped: it ran out of memory.</b>
        <p>Raise its memory limit, then start it again. The Full image needs more than Slim.</p>
        <button class="small" onclick={() => openEditor("limits")}>Edit limits</button>
      </span>
    </div>
  {/if}

  <!-- Starting: progress -->
  {#if opActive}
    <div class="op" aria-live="polite">
      <div class="opl">
        <span class="spin" aria-hidden="true"></span>
        <span class="msg">{t.op.message || "Starting…"}</span>
        <span class="el-t mono">{elapsed(t.op.elapsed_ms)}</span>
      </div>
      <div class="bar busy" aria-hidden="true"><i></i></div>
      {#if t.op.lines?.length}<pre class="log small">{t.op.lines.slice(-6).join("\n")}</pre>{/if}
    </div>
  {/if}

  <div class="acts">
    {#if opActive}
      <button class="small" onclick={cancelStart} disabled={hs.busy === "stop"}><Icon name="xmark" size={14} />Cancel</button>
    {:else if running}
      <button class="small" onclick={stop} disabled={!!hs.busy}>
        {#if hs.busy === "stop"}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="stopsq" size={14} />{/if}Stop
      </button>
      <button class="small" onclick={restart} disabled={!!hs.busy}>
        {#if hs.busy === "start"}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="retry" size={14} />{/if}Restart
      </button>
    {:else}
      <button class="small" onclick={() => start(false)} disabled={!!hs.busy || !!t.spec_error}>
        {#if hs.busy === "start"}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="play" size={13} />{/if}{startText}
      </button>
    {/if}
    <button class="small ghost" onclick={toggleLogs} aria-expanded={showLogs} aria-controls="{uid}-logs">
      <Icon name="doc" size={14} />{showLogs ? "Hide logs" : "Logs"}
    </button>
    <button
      class="small ghost"
      onclick={() => start(true)}
      disabled={!!hs.busy || opActive || !!t.spec_error}
      title="Download the newest {t.image} and recreate the container (workspace files are kept)">
      <Icon name="download" size={14} />Update
    </button>
    {#if exists && !running && !opActive}
      <button class="small ghost danger" onclick={remove} disabled={!!hs.busy}><Icon name="trash" size={14} />Remove</button>
    {/if}
  </div>

  {#if showLogs}
    <div class="logs" id="{uid}-logs">
      <div class="lh">
        <span>Last 300 lines · keys are hidden</span>
        <button class="small ghost icon" onclick={loadLogs} disabled={logsBusy} aria-label="Refresh the logs" title="Refresh">
          <Icon name="refresh" size={14} />
        </button>
      </div>
      <pre class="log">{logsBusy && !logs ? "Loading…" : logs}</pre>
    </div>
  {/if}

  <details class="more">
    <summary><Icon name="chevronRight" size={14} />Address, key and what the model can see</summary>
    <div class="moreb">
      <Field label="Address, as Open WebUI reaches it" value={t.url || "—"}>
        {#if t.url}<CopyButton text={t.url} label="Copy" title="Copy the terminal address" />{/if}
      </Field>
      <SecretField label="Key" secret={t.key} what="terminal key" onnew={newTerminalKey} busy={hs.busy === "termkey"} />
      {#if t.mounts?.length}
        <p class="lbl mt">What the model can see</p>
        <ul class="mounts">
          {#each t.mounts as m (m.container)}
            <li>
              <span class="mono">{m.container.replace(/^\/home\/user/, "~")}</span>
              <span class="from" title={m.host}>from {tildePath(m.host, home)}</span>
              <span class="chip {m.mode === 'read-write' ? 'pr' : ''}">{m.mode}</span>
            </li>
          {/each}
        </ul>
        <p class="fine">
          Nothing else on this computer: not your home folder, the container engine or the factory's settings. It can use
          the network.
        </p>
      {/if}
      <p class="fine">
        <span class="mono">{t.image}</span>{#if t.engine} on {engineName(t.engine)}{/if} ·
        {t.image_present ? "downloaded" : "not downloaded yet"}{#if t.local_url} · from this computer at
          <span class="mono">{t.local_url}</span>{/if}
      </p>
    </div>
  </details>
</section>

<style>
  .card { display: flex; flex-direction: column; min-width: 0; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .hd { margin-bottom: 12px; }
  .chip .dot { width: 6px; height: 6px; box-shadow: none; }

  /* Slim | Full */
  .segc { display: flex; padding: 3px; border: 1px solid var(--ln2); border-radius: 10px; gap: 2px; }
  .segc label {
    flex: 1;
    min-width: 0;
    padding: 6px 10px;
    border-radius: 7px;
    font-size: 12.5px;
    color: var(--mu);
    line-height: 1.25;
    cursor: pointer;
    position: relative;
  }
  .segc label:hover { color: var(--ink); }
  .segc label b { display: block; color: inherit; font-weight: 600; }
  .segc label.on { background: var(--p3); color: var(--ink); }
  .segc input { position: absolute; opacity: 0; width: 1px; height: 1px; pointer-events: none; }
  .segc label:has(input:focus-visible) { outline: 2px solid var(--pr); outline-offset: 1px; }
  .segc label:has(input:disabled) { cursor: not-allowed; }
  .vdesc { color: var(--fa); font-size: 12px; margin-top: 6px; line-height: 1.4; }

  .rows { margin-top: 8px; }
  .row {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 0;
    border-top: 1px solid var(--ln);
    font-size: 13px;
  }
  .rows > .row:first-child { border-top: 0; }
  .rl { flex: 1; min-width: 0; font-size: 13px; color: var(--ink); }
  .rl small { display: block; color: var(--mu); font-size: 12px; overflow-wrap: anywhere; }

  .editor {
    margin-top: 12px;
    padding: 12px;
    border-radius: 11px;
    background: var(--p2);
    border: 1px solid var(--ln2);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .rows .editor { margin: 6px 0; }
  .el { font-size: 12px; color: var(--fa); font-weight: 500; display: block; margin-bottom: 4px; }
  .editor .hint { font-size: 12px; line-height: 1.45; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .ebtn { display: flex; gap: 6px; flex-wrap: wrap; }
  .ferr { color: var(--er); font-size: 12.5px; margin-top: 4px; }

  .callout { margin-top: 12px; }
  .callout button { margin-top: 8px; }
  .dismiss { margin-left: -10px; }
  .log {
    margin: 8px 0 0;
    padding: 9px 11px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--cv) 70%, black);
    border: 1px solid var(--ln);
    font: 11.5px/1.5 var(--font-mono);
    color: var(--mu);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 240px;
    overflow: auto;
  }
  .log.small { max-height: 130px; }

  .op {
    margin-top: 12px;
    padding: 11px 12px;
    border-radius: 11px;
    background: var(--prs);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .opl { display: flex; align-items: center; gap: 9px; font-size: 13px; }
  .msg { flex: 1; min-width: 0; }
  .el-t { color: var(--mu); font-size: 11.5px; }
  .op .log { margin: 0; }

  .acts { display: flex; flex-wrap: wrap; gap: 4px; margin-top: 14px; }
  .acts button.ghost { padding: 0 8px; }
  .spin.sm { width: 12px; height: 12px; }

  .logs { margin-top: 10px; }
  .lh { display: flex; align-items: center; justify-content: space-between; font-size: 12px; color: var(--fa); }

  .more { margin-top: 12px; border-top: 1px solid var(--ln); padding-top: 10px; }
  .more summary {
    list-style: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 12.5px;
    color: var(--mu);
    font-weight: 500;
    border-radius: 6px;
    width: fit-content;
  }
  .more summary::-webkit-details-marker { display: none; }
  .more summary:hover { color: var(--ink); }
  .more summary :global(.ico) { transition: transform 0.15s ease; }
  .more[open] summary :global(.ico) { transform: rotate(90deg); }
  .moreb { padding-bottom: 2px; }
  .mt { margin-top: 14px; margin-bottom: 4px; }
  .mounts { list-style: none; margin: 0; padding: 0; }
  .mounts li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 0;
    border-top: 1px solid var(--ln);
    font-size: 12.5px;
    min-width: 0;
  }
  .mounts li:first-child { border-top: 0; }
  .mounts .mono { color: var(--ink); font-size: 12px; }
  .from { color: var(--mu); font-size: 12px; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .fine { color: var(--fa); font-size: 12px; line-height: 1.5; margin-top: 8px; overflow-wrap: anywhere; }
  .fine .mono { font-size: 11.5px; color: var(--mu); }
</style>
