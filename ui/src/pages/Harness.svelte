<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import ModalityBadge from "../components/ModalityBadge.svelte";
  import { toast } from "../lib/stores";
  import {
    getHarnessStatus,
    saveTerminalSettings,
    startTerminal,
    stopTerminal,
    terminalLogs,
    regenerateGatewayKey,
    regenerateTerminalKey,
    checkOpenWebUI,
    connectOpenWebUI,
    terminalState,
    itemTone,
    maskKey,
    copyText,
    AREA_LABEL,
    type HarnessStatus,
    type TerminalStatus,
    type ConnectResult,
    type TerminalVariant,
  } from "../lib/harness";

  let st = $state<HarnessStatus | null>(null);
  let loadError = $state("");

  // Terminal settings form: seeded from the status until the user edits it.
  let form = $state({ variant: "slim", port: 8798, workspace: "", share: false, memory: 2048 });
  let formDirty = $state(false);
  let formError = $state("");
  let saving = $state(false);
  let busy = $state(""); // the terminal action in flight
  let showLogs = $state(false);
  let logs = $state("");

  // Open WebUI connector form. The admin key lives only in this page.
  let owuiURL = $state("");
  let owuiKey = $state("");
  let reachHost = $state("");
  let doModels = $state(true);
  let doTerminal = $state(true);
  let doWeb = $state(true);
  let showAdvanced = $state(false);
  let connecting = $state<"" | "check" | "connect">("");
  let result = $state<ConnectResult | null>(null);
  let connectError = $state("");

  let revealGateway = $state(false);
  let revealTerminal = $state(false);

  let gen = 0;
  async function load() {
    const my = ++gen;
    try {
      const s = await getHarnessStatus();
      if (my !== gen) return;
      st = s;
      loadError = "";
      if (!formDirty) seedForm(s.terminal);
      if (!owuiURL) owuiURL = s.openwebui.url;
      if (!reachHost) reachHost = s.openwebui.reach_host;
    } catch (e) {
      if (my === gen) loadError = (e as Error).message;
    }
  }

  function seedForm(t: TerminalStatus) {
    form = { variant: t.variant, port: t.port, workspace: t.workspace, share: t.share_claude_skills, memory: t.memory_mb };
  }

  // Poll: quickly while the terminal is starting, slowly otherwise.
  $effect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      await load();
      if (stopped) return;
      timer = setTimeout(tick, st?.terminal.op.active ? 1200 : 6000);
    };
    tick();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  });

  const term = $derived(st?.terminal ?? null);
  const termState = $derived(term ? terminalState(term) : { label: "…", tone: "idle" as const });
  const running = $derived(!!term?.container?.running);
  const exists = $derived(!!term?.container?.exists);
  const variants = $derived<TerminalVariant[]>(term?.variants ?? []);
  const variantDefault = (id: string) => variants.find((v) => v.id === id)?.memory_mb ?? 2048;
  const listedSkills = $derived((st?.skills.items ?? []).filter((s) => s.listed).length);

  function edit() {
    formDirty = true;
    formError = "";
  }

  function pickVariant(id: string) {
    if (form.memory === variantDefault(form.variant)) form.memory = variantDefault(id);
    form.variant = id;
    edit();
  }

  async function copy(text: string, what: string) {
    toast((await copyText(text)) ? `${what} copied.` : "Copy failed: select the text and copy it by hand.");
  }

  async function act(label: string, fn: () => Promise<unknown>, done?: string) {
    busy = label;
    try {
      await fn();
      if (done) toast(done);
    } catch (e) {
      toast((e as Error).message);
    } finally {
      busy = "";
      load();
    }
  }

  const start = (update = false) =>
    act(update ? "update" : "start", async () => {
      await startTerminal(update);
      await load();
    });
  const stop = () => act("stop", () => stopTerminal(false), "Terminal stopped.");
  function remove() {
    if (!confirm("Remove the terminal container? Files in the workspace folder are kept; anything installed inside the container is lost.")) return;
    act("remove", () => stopTerminal(true), "Terminal container removed.");
  }

  async function toggleLogs() {
    showLogs = !showLogs;
    if (showLogs) await refreshLogs();
  }
  async function refreshLogs() {
    try {
      const r = await terminalLogs(300);
      logs = r.logs || r.note || "(no output yet)";
    } catch (e) {
      logs = (e as Error).message;
    }
  }

  async function saveSettings(restart: boolean) {
    saving = true;
    formError = "";
    try {
      await saveTerminalSettings({
        variant: form.variant,
        port: Number(form.port),
        workspace: form.workspace.trim(),
        share_claude_skills: form.share,
        memory_mb: Number(form.memory) === variantDefault(form.variant) ? 0 : Number(form.memory),
      });
      formDirty = false;
      if (restart) {
        await startTerminal(false);
        toast("Saved. Restarting the terminal…");
      } else {
        toast("Terminal settings saved.");
      }
    } catch (e) {
      formError = (e as Error).message;
    } finally {
      saving = false;
      load();
    }
  }

  function cancelEdit() {
    formDirty = false;
    formError = "";
    if (term) seedForm(term);
  }

  async function regenGateway() {
    if (!confirm("Make a new gateway key? The old key stops working right away: Connect Open WebUI again (or paste the new key there) afterwards.")) return;
    act("gwkey", regenerateGatewayKey, "New gateway key made. Connect Open WebUI again to use it.");
  }
  async function regenTerminal() {
    if (!confirm("Make a new terminal key? Restart the terminal and Connect Open WebUI again afterwards.")) return;
    act("termkey", regenerateTerminalKey, "New terminal key made. Restart the terminal, then Connect again.");
  }

  async function runConnector(kind: "check" | "connect") {
    connecting = kind;
    connectError = "";
    try {
      const req = {
        url: owuiURL.trim(),
        api_key: owuiKey.trim(),
        reach_host: reachHost.trim(),
        models: doModels,
        terminal: doTerminal,
        web_search: doWeb,
      };
      result = kind === "check" ? await checkOpenWebUI(req) : await connectOpenWebUI(req);
      if (kind === "connect" && result.report.admin && !result.report.items.some((i) => i.status === "error")) {
        toast(result.report.changed ? "Open WebUI is connected." : "Open WebUI was already set up.");
      }
    } catch (e) {
      connectError = (e as Error).message;
      result = null;
    } finally {
      connecting = "";
    }
  }

  // In-page jump. The app routes on the URL hash, so a plain href="#connect"
  // would leave this page; scroll to the card instead.
  function jump(e: MouseEvent, id: string) {
    e.preventDefault();
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  const toneIcon: Record<string, string> = { ok: "check", warn: "alert", err: "x", idle: "chevron", busy: "refresh" };
  const elapsed = (ms: number) => (ms < 60_000 ? `${Math.round(ms / 1000)}s` : `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`);
</script>

{#if loadError && !st}
  <div class="card"><div class="empty">Couldn't load the harness status: {loadError}</div></div>
{:else if !st}
  <div class="card"><div class="empty"><span class="spinner"></span> Loading…</div></div>
{:else}
  {#if st.config_error}
    <div class="banner err"><Icon name="alert" /> {st.config_error}</div>
  {/if}

  <!-- Overview -->
  <div class="card">
    <p class="lead">
      Open WebUI can give a model tools: <b>web search</b>, a <b>terminal</b> where it reads, edits and runs files in a
      sandbox, and <b>skills</b> (instructions it loads when a task matches). This page wires the models you run here
      into all three. Use <a href="#harness" onclick={(e) => jump(e, "connect")}>Connect Open WebUI</a> below, or the <a href="#harness" onclick={(e) => jump(e, "manual")}>manual steps</a>.
    </p>
    <div class="pills">
      <span class="pill tone-{st.gateway.models.length ? 'ok' : 'idle'}"><Icon name="plug" size={13} /> Gateway · {st.gateway.models.length} model{st.gateway.models.length === 1 ? "" : "s"}</span>
      <span class="pill tone-{termState.tone}"><Icon name="terminal" size={13} /> Terminal · {termState.label}</span>
      <span class="pill tone-{listedSkills ? 'ok' : 'idle'}"><Icon name="book" size={13} /> Skills · {listedSkills}</span>
      {#if result?.report.web_search}
        <span class="pill tone-{result.report.web_search.enabled ? 'ok' : 'warn'}"><Icon name="globe" size={13} /> Web search · {result.report.web_search.enabled ? "on" : "off"}</span>
      {/if}
    </div>
  </div>

  <!-- Model gateway -->
  <div class="card">
    <h2><Icon name="plug" /> Model gateway</h2>
    <p class="hint">
      One OpenAI-compatible endpoint for every running chat, code, reasoning, vision and embedding model. Open WebUI
      needs a single connection; start or stop models on the Containers page and its model list follows.
    </p>
    <div class="kv">
      <span class="k">Base URL for Open WebUI</span>
      <span class="v"><code>{st.gateway.base_url}</code><button class="ghost icon-btn" title="Copy" onclick={() => copy(st!.gateway.base_url, "Base URL")}><Icon name="copy" size={14} /></button></span>
      <span class="k">From this computer</span>
      <span class="v"><code>{st.gateway.local_url}</code><button class="ghost icon-btn" title="Copy" onclick={() => copy(st!.gateway.local_url, "URL")}><Icon name="copy" size={14} /></button></span>
      <span class="k">API key</span>
      <span class="v">
        <code>{revealGateway ? st.gateway.key : maskKey(st.gateway.key)}</code>
        <button class="ghost icon-btn" title={revealGateway ? "Hide" : "Show"} onclick={() => (revealGateway = !revealGateway)}><Icon name="eye" size={14} /></button>
        <button class="ghost icon-btn" title="Copy" onclick={() => copy(st!.gateway.key, "Gateway key")}><Icon name="copy" size={14} /></button>
        <button class="ghost" onclick={regenGateway} disabled={busy === "gwkey"}><Icon name="key" size={14} /> Regenerate</button>
      </span>
    </div>
    {#if st.gateway.error}
      <div class="empty">{st.gateway.error}</div>
    {:else if !st.gateway.models.length}
      <div class="empty">No models are running. Start one from Images or Containers and it shows up here (and in Open WebUI) right away.</div>
    {:else}
      <table>
        <thead><tr><th>Model id</th><th>Type</th><th>Port</th><th>Image</th></tr></thead>
        <tbody>
          {#each st.gateway.models as m (m.id)}
            <tr>
              <td><code>{m.id}</code>{#if m.persona}<span class="muted"> · {m.persona}</span>{/if}</td>
              <td><ModalityBadge mod={m.modality} /></td>
              <td>{m.port}</td>
              <td class="muted">{m.ref}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    {/if}
  </div>

  <!-- Workspace terminal -->
  {#if term}
    <div class="card">
      <div class="row-between">
        <h2><Icon name="terminal" /> Workspace terminal</h2>
        <span class="pill tone-{termState.tone}">{termState.label}</span>
      </div>
      <p class="hint">
        A Linux sandbox (<a href="https://github.com/open-webui/open-terminal" target="_blank" rel="noreferrer">Open Terminal</a>)
        where the model lists, reads and edits files and runs commands. It sees only the workspace folder (read-write)
        and the skills (read-only), and listens on this computer's loopback only.
      </p>

      {#if term.restart_required && running && !term.op.active}
        <div class="banner warn">
          <Icon name="alert" /> The settings changed since the terminal started.
          <button class="small" onclick={() => start(false)} disabled={!!busy}>Restart to apply</button>
        </div>
      {/if}
      {#if term.spec_error}
        <div class="banner err"><Icon name="alert" /> {term.spec_error}</div>
      {/if}
      {#if term.error}
        <div class="banner err"><Icon name="alert" /> {term.error}</div>
      {/if}

      {#if term.op.active || term.op.error}
        <div class="op" class:failed={!!term.op.error}>
          {#if term.op.active}
            <div><span class="spinner"></span> {term.op.message} <span class="muted">({elapsed(term.op.elapsed_ms)})</span></div>
          {:else}
            <div><Icon name="alert" /> {term.op.error}</div>
            {#if term.op.detail}<pre class="log small">{term.op.detail}</pre>{/if}
          {/if}
          {#if term.op.active && term.op.lines.length}
            <pre class="log small">{term.op.lines.slice(-8).join("\n")}</pre>
          {/if}
        </div>
      {/if}

      <div class="actions">
        {#if !running}
          <button class="primary" onclick={() => start(false)} disabled={!!busy || term.op.active || !!term.spec_error}>
            <Icon name="play" size={14} /> {exists ? "Start" : term.image_present ? "Create & start" : `Download (${variants.find((v) => v.id === term!.variant)?.size ?? ""}) & start`}
          </button>
        {:else}
          <button onclick={stop} disabled={!!busy || term.op.active}><Icon name="stop" size={14} /> Stop</button>
          <button onclick={() => start(false)} disabled={!!busy || term.op.active} title="Recreate it if the settings changed">
            <Icon name="refresh" size={14} /> Restart
          </button>
        {/if}
        {#if term.op.active}
          <button onclick={stop}><Icon name="x" size={14} /> Cancel</button>
        {/if}
        <button class="ghost" onclick={() => start(true)} disabled={!!busy || term.op.active} title="Pull the newest {term.image} and recreate the container">
          <Icon name="download" size={14} /> Update image
        </button>
        <button class="ghost" onclick={toggleLogs}><Icon name="doc" size={14} /> {showLogs ? "Hide logs" : "Logs"}</button>
        {#if exists && !running}
          <button class="ghost danger" onclick={remove} disabled={!!busy}><Icon name="trash" size={14} /> Remove</button>
        {/if}
      </div>
      {#if showLogs}
        <div class="logs-head"><span class="muted">Last 300 lines</span><button class="ghost icon-btn" title="Refresh" onclick={refreshLogs}><Icon name="refresh" size={14} /></button></div>
        <pre class="log">{logs}</pre>
      {/if}

      <h3>Settings</h3>
      <div class="variants">
        {#each variants as v (v.id)}
          <button type="button" class="variant" class:sel={form.variant === v.id} onclick={() => pickVariant(v.id)}>
            <b>{v.label}</b> <span class="muted">{v.size}</span>
            <span class="desc">{v.desc}</span>
          </button>
        {/each}
      </div>

      <label class="field">
        <span>Workspace folder <span class="muted">(shared read-write as <code>~/workspace</code>)</span></span>
        <input type="text" bind:value={form.workspace} oninput={edit} placeholder={term.default_workspace} spellcheck="false" />
        <span class="hint">
          A folder on this computer. Leave empty for the factory's <code>workspace/</code> folder. Your home folder,
          hidden folders, system folders and the rest of the factory folder are refused. Create the folder first.
        </span>
      </label>

      <label class="check">
        <input type="checkbox" bind:checked={form.share} onchange={edit} disabled={!term.claude_skills_path} />
        <span>
          Also share <code>~/.claude/skills</code> (read-only)
          {#if !term.claude_skills_path}<span class="muted"> · restart the factory with the current start script to enable this</span>{/if}
        </span>
      </label>

      <div class="grid2">
        <label class="field">
          <span>Port <span class="muted">(127.0.0.1 only)</span></span>
          <input type="number" min="1024" max="65535" bind:value={form.port} oninput={edit} />
        </label>
        <label class="field">
          <span>Memory limit (MB)</span>
          <input type="number" min="512" step="256" bind:value={form.memory} oninput={edit} />
        </label>
      </div>

      {#if formError}<div class="banner err"><Icon name="alert" /> {formError}</div>{/if}
      {#if formDirty}
        <div class="actions">
          <button class="primary" onclick={() => saveSettings(running)} disabled={saving}>
            <Icon name="check" size={14} /> {running ? "Save & restart" : "Save"}
          </button>
          <button class="ghost" onclick={cancelEdit} disabled={saving}>Cancel</button>
        </div>
      {/if}

      <h3>What the model can see</h3>
      {#if term.mounts?.length}
        <ul class="mounts">
          {#each term.mounts as m (m.container)}
            <li><code>{m.container}</code> <span class="muted">←</span> <code>{m.host}</code> <span class="tag">{m.mode}</span></li>
          {/each}
        </ul>
      {/if}
      <div class="kv">
        <span class="k">URL for Open WebUI</span>
        <span class="v"><code>{term.url}</code><button class="ghost icon-btn" title="Copy" onclick={() => copy(term!.url, "Terminal URL")}><Icon name="copy" size={14} /></button></span>
        <span class="k">API key</span>
        <span class="v">
          <code>{revealTerminal ? term.key : maskKey(term.key)}</code>
          <button class="ghost icon-btn" title={revealTerminal ? "Hide" : "Show"} onclick={() => (revealTerminal = !revealTerminal)}><Icon name="eye" size={14} /></button>
          <button class="ghost icon-btn" title="Copy" onclick={() => copy(term!.key, "Terminal key")}><Icon name="copy" size={14} /></button>
          <button class="ghost" onclick={regenTerminal} disabled={busy === "termkey"}><Icon name="key" size={14} /> Regenerate</button>
        </span>
      </div>
    </div>
  {/if}

  <!-- Skills -->
  <div class="card">
    <h2><Icon name="book" /> Skills</h2>
    <p class="hint">
      A skill is a folder with a <code>SKILL.md</code>: a short description the model sees, plus instructions it loads
      when a task matches. The terminal shares the factory's <code>skills/</code> folder read-only, so a skill you add
      there is available in the next chat.
    </p>
    {#if st.skills.error}
      <div class="empty">{st.skills.error}</div>
    {:else if !st.skills.items.length}
      <div class="empty">No skills in <code>{st.skills.path}</code> yet.</div>
    {:else}
      <ul class="skills">
        {#each st.skills.items as s (s.dir)}
          <li>
            <div><code>{s.name}</code>{#if !s.listed}<span class="tag warn">not loaded</span>{/if}</div>
            <div class="muted">{s.description || "(no description)"}</div>
            {#each s.warnings ?? [] as w}<div class="warn-text"><Icon name="alert" size={12} /> {w}</div>{/each}
          </li>
        {/each}
      </ul>
    {/if}
    <p class="note">
      Folder: <code>{st.skills.path}</code>{#if term?.share_claude_skills} · also <code>{term.claude_skills_path}</code>{/if}. In a chat
      with the terminal selected, the model sees each skill's name and description and loads the full instructions with
      <code>view_skill</code>; you can also ask for one by name.
    </p>
  </div>

  <!-- Web search -->
  <div class="card">
    <h2><Icon name="globe" /> Web search</h2>
    <p class="hint">
      Web search runs inside Open WebUI, not the factory. When it's on, models get the <code>search_web</code> and
      <code>fetch_url</code> tools in chats where you switch on <b>Web Search</b>. Connect turns it on with DuckDuckGo
      (no API key needed) only if it's off, and keeps any engine you already chose.
    </p>
    <div class="kv">
      <span class="k">Status</span>
      <span class="v">
        {#if result?.report.web_search}
          {result.report.web_search.enabled ? "On" : "Off"}{#if result.report.web_search.engine} · {result.report.web_search.engine}{/if}
        {:else}
          <span class="muted">Unknown: run Check below.</span>
        {/if}
      </span>
    </div>
    <p class="note">Slim Open WebUI images can't use DuckDuckGo; pick another provider under Admin Panel → Settings → Web Search.</p>
  </div>

  <!-- Connector -->
  <div class="card" id="connect">
    <h2><Icon name="link" /> Connect Open WebUI</h2>
    <p class="hint">
      Uses an admin API key from Open WebUI (Settings → Account → API keys; API keys must be enabled under Admin Panel →
      Settings → General). The key is sent for this request only and never stored. Your other connections and settings
      are left as they are, and the result lists every change.
    </p>
    <div class="grid2">
      <label class="field">
        <span>Open WebUI URL <span class="muted">(as the factory reaches it)</span></span>
        <input type="text" bind:value={owuiURL} placeholder={st.openwebui.default_url} spellcheck="false" />
      </label>
      <label class="field">
        <span>Admin API key</span>
        <input type="password" bind:value={owuiKey} placeholder="sk-…" autocomplete="off" />
      </label>
    </div>
    <div class="opts">
      <label class="check"><input type="checkbox" bind:checked={doModels} /> <span>Models (gateway connection)</span></label>
      <label class="check"><input type="checkbox" bind:checked={doTerminal} /> <span>Workspace terminal</span></label>
      <label class="check"><input type="checkbox" bind:checked={doWeb} /> <span>Web search</span></label>
      <button class="ghost" onclick={() => (showAdvanced = !showAdvanced)}>{showAdvanced ? "Hide" : "More"} options</button>
    </div>
    {#if showAdvanced}
      <label class="field">
        <span>Open WebUI reaches this computer at</span>
        <input type="text" bind:value={reachHost} placeholder="host.docker.internal" spellcheck="false" />
        <span class="hint">
          <code>host.docker.internal</code> works with Docker Desktop. If Open WebUI runs with host networking (Linux
          <code>--network host</code>) use <code>127.0.0.1</code>.
        </span>
      </label>
    {/if}
    <div class="actions">
      <button onclick={() => runConnector("check")} disabled={!!connecting || !owuiKey.trim()}>
        {#if connecting === "check"}<span class="spinner"></span>{:else}<Icon name="search" size={14} />{/if} Check
      </button>
      <button class="primary" onclick={() => runConnector("connect")} disabled={!!connecting || !owuiKey.trim() || !(doModels || doTerminal || doWeb)}>
        {#if connecting === "connect"}<span class="spinner"></span>{:else}<Icon name="plug" size={14} />{/if} Connect
      </button>
    </div>
    {#if connectError}<div class="banner err"><Icon name="alert" /> {connectError}</div>{/if}
    {#if result}
      <ul class="report">
        {#each result.report.items as it, i (i)}
          <li class="tone-{itemTone(it.status)}">
            <span class="ri"><Icon name={toneIcon[itemTone(it.status)]} size={14} /></span>
            <span class="ra">{AREA_LABEL[it.area] ?? it.area}</span>
            <span class="rd">{it.detail}</span>
          </li>
        {/each}
      </ul>
      {#if result.report.admin}
        <p class="note">Open WebUI {result.report.version} at <code>{result.url}</code>{result.report.changed ? " · changes saved" : " · nothing changed"}.</p>
      {/if}
    {/if}
  </div>

  <!-- Manual setup -->
  <div class="card" id="manual">
    <h2><Icon name="list" /> Manual setup</h2>
    <ol class="steps">
      <li>
        <b>Models.</b> Admin Panel → Settings → Connections → OpenAI API → <b>+</b>. URL
        <code>{st.gateway.base_url}</code><button class="ghost icon-btn" title="Copy" onclick={() => copy(st!.gateway.base_url, "URL")}><Icon name="copy" size={13} /></button>,
        key: the gateway key above<button class="ghost icon-btn" title="Copy" onclick={() => copy(st!.gateway.key, "Gateway key")}><Icon name="copy" size={13} /></button>.
        Optionally set Connection type to <i>Local</i>.
      </li>
      <li>
        <b>Terminal.</b> Start the terminal above, then Admin Panel → Settings → Integrations → Open Terminal → <b>+</b>. URL
        <code>{term?.url}</code><button class="ghost icon-btn" title="Copy" onclick={() => copy(term?.url ?? "", "URL")}><Icon name="copy" size={13} /></button>,
        API key: the terminal key above<button class="ghost icon-btn" title="Copy" onclick={() => copy(term?.key ?? "", "Terminal key")}><Icon name="copy" size={13} /></button>,
        auth Bearer. New terminal connections are admin-only until you grant access there.
      </li>
      <li><b>Web search.</b> Admin Panel → Settings → Web Search: turn it on and choose an engine (DuckDuckGo needs no key).</li>
      <li>
        <b>Chat.</b> Pick a factory model, select the <i>LLM Factory workspace</i> terminal in the chat, and switch on Web Search when
        you need it. Models with good tool calling (e.g. Qwen 3.x, recent Llama, Mistral, Gemma) work best.
      </li>
    </ol>
    <p class="note">More detail, the security model and troubleshooting: <code>docs/HARNESS.md</code>.</p>
  </div>
{/if}

<style>
  h2 { font-size: 15px; margin: 0 0 8px; display: flex; align-items: center; gap: 8px; }
  h3 { font-size: 13.5px; margin: 20px 0 8px; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.4px; }
  .card + .card, .banner + .card { margin-top: 16px; }
  .lead { margin: 0 0 12px; line-height: 1.6; }
  .pills { display: flex; flex-wrap: wrap; gap: 8px; }
  .pill { display: inline-flex; align-items: center; gap: 6px; padding: 3px 10px; border-radius: 999px; font-size: 12.5px; border: 1px solid var(--border); background: #11141a; white-space: nowrap; }
  .tone-ok { color: var(--ok); border-color: #2a5a35; }
  .tone-warn { color: #e3b341; border-color: #5c4a1e; }
  .tone-err { color: var(--err); border-color: #6a2a2a; }
  .tone-busy { color: var(--accent); border-color: #2b4a80; }
  .tone-idle { color: var(--muted); }
  .kv { display: grid; grid-template-columns: max-content 1fr; gap: 8px 14px; align-items: center; margin: 12px 0; font-size: 13.5px; }
  .k { color: var(--muted); }
  .v { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; min-width: 0; }
  code { background: #0c0e13; padding: 1px 6px; border-radius: 4px; font-size: 12.5px; word-break: break-all; }
  .muted { color: var(--muted); }
  .icon-btn { padding: 4px 6px; display: inline-flex; align-items: center; }
  table { width: 100%; border-collapse: collapse; font-size: 13.5px; margin-top: 10px; }
  th, td { text-align: left; padding: 7px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  th { color: var(--muted); font-weight: 600; }
  .actions { display: flex; flex-wrap: wrap; gap: 8px; margin: 12px 0 4px; }
  .actions button { display: inline-flex; align-items: center; gap: 6px; }
  .banner { display: flex; align-items: center; gap: 8px; padding: 9px 12px; border-radius: 8px; font-size: 13.5px; margin: 10px 0; border: 1px solid var(--border); flex-wrap: wrap; }
  .banner.err { background: #2a1416; border-color: #6a2a2a; color: #ffb3ad; }
  .banner.warn { background: #2a2414; border-color: #5c4a1e; color: #f0d8a0; }
  .op { border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; margin: 10px 0; font-size: 13.5px; background: #11141a; }
  .op.failed { border-color: #6a2a2a; color: #ffb3ad; }
  .log { background: #06080c; border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; font: 12px/1.45 ui-monospace, monospace; color: #cbd3e1; max-height: 320px; overflow: auto; white-space: pre-wrap; margin: 8px 0 0; }
  .log.small { max-height: 160px; }
  .logs-head { display: flex; align-items: center; justify-content: space-between; margin-top: 8px; font-size: 12.5px; }
  .variants { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 10px; }
  .variant { text-align: left; display: flex; flex-direction: column; gap: 2px; padding: 10px 12px; background: #11141a; }
  .variant.sel { border-color: var(--accent); background: #142038; }
  .variant .desc { font-size: 12.5px; color: var(--muted); }
  .field { display: flex; flex-direction: column; gap: 5px; margin: 12px 0 0; font-size: 13.5px; }
  .field .hint { font-size: 12.5px; }
  .check { display: flex; align-items: center; gap: 8px; margin: 12px 0 0; font-size: 13.5px; }
  .check input { width: auto; }
  .grid2 { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 0 14px; }
  .opts { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 18px; }
  .mounts, .skills, .report { list-style: none; padding: 0; margin: 6px 0; font-size: 13.5px; }
  .mounts li { padding: 4px 0; display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .tag { font-size: 11.5px; padding: 1px 7px; border-radius: 6px; border: 1px solid var(--border); color: var(--muted); margin-left: 6px; }
  .tag.warn { color: #e3b341; border-color: #5c4a1e; }
  .skills li { padding: 8px 0; border-bottom: 1px solid var(--border); display: flex; flex-direction: column; gap: 3px; }
  .skills li:last-child { border-bottom: none; }
  .warn-text { color: #e3b341; font-size: 12.5px; }
  .report li { display: grid; grid-template-columns: 20px 92px 1fr; gap: 8px; padding: 7px 0; border-bottom: 1px solid var(--border); align-items: start; }
  .report li:last-child { border-bottom: none; }
  .report .ra { font-weight: 600; }
  .report .rd { color: var(--text); }
  .report li.tone-idle .ri { color: var(--muted); }
  .note { font-size: 12.5px; color: var(--muted); line-height: 1.6; margin: 10px 0 0; }
  .steps { margin: 6px 0 0; padding-left: 20px; font-size: 13.5px; line-height: 1.7; }
  .steps li + li { margin-top: 6px; }
  .spinner {
    display: inline-block; width: 13px; height: 13px; vertical-align: -2px; margin-right: 4px;
    border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%;
    animation: spin 0.7s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
