<script lang="ts">
  // Open WebUI (sheet frame f12): what setup does, Check and Connect through its
  // admin API, and what it found. The admin key lives only in this form; the
  // last result is kept in this browser (lib/harness OwuiMemo).
  import Icon from "../Icon.svelte";
  import {
    AREA_LABEL,
    itemTone,
    areaTone,
    reportVerdict,
    browserURL,
    whenText,
    sentence,
    TONE_CHIP,
    type HarnessStatus,
    type Tone,
  } from "../../lib/harness";
  import { hs, runConnector, memoCurrent } from "./state.svelte";

  let { st }: { st: HarnessStatus } = $props();

  const uid = $props.id();

  // ---- the form (the admin key is never stored) -----------------------------------
  let url = $state("");
  let key = $state("");
  let reach = $state("");
  let parts = $state({ models: true, terminal: true, web_search: true });
  let advanced = $state(false);
  let again = $state(false);
  let seeded = false;
  $effect(() => {
    if (seeded) return;
    seeded = true;
    url = st.openwebui.url || "";
    reach = st.openwebui.reach_host || "";
  });

  const PARTS = [
    { area: "models", label: "Add the gateway as a connection", did: "Adds the gateway as a connection", hint: "Every running model, listed by name" },
    { area: "terminal", label: "Add the terminal as a tool server", did: "Adds the terminal as a tool server", hint: "Visible to admins until you share it" },
    { area: "web_search", label: "Turn on web search", did: "Turns on web search", hint: "With DuckDuckGo, unless you picked an engine" },
  ] as const;

  const memo = $derived(hs.memo);
  const current = $derived(memoCurrent(st));
  const verdict = $derived(memo ? reportVerdict(memo.report) : null);
  const setUp = $derived(!!verdict && verdict.tone === "ok" && current);
  const showForm = $derived(!setUp || again);
  const link = $derived(browserURL(memo?.url || st.openwebui.url || ""));
  const host = $derived.by(() => {
    try {
      return link ? new URL(link).host : "";
    } catch {
      return "";
    }
  });
  const anyPart = $derived(parts.models || parts.terminal || parts.web_search);

  const chip = $derived.by((): { text: string; cls: string } => {
    if (hs.connecting) return { text: hs.connecting === "check" ? "Checking…" : "Setting up…", cls: "pr" };
    if (!memo || !verdict) return { text: "Not checked", cls: "" };
    if (!current) return { text: "Set it up again", cls: "wa" };
    return { text: verdict.chip, cls: TONE_CHIP[verdict.tone] };
  });

  const TONE_ICON: Record<Tone, string> = { ok: "check", warn: "warn", err: "errc", idle: "minus", busy: "clock" };

  async function run(kind: "check" | "connect") {
    const ok = await runConnector(
      kind,
      {
        url: url.trim(),
        api_key: key.trim(),
        reach_host: reach.trim(),
        models: parts.models,
        terminal: parts.terminal,
        web_search: parts.web_search,
      },
      st,
    );
    // Done with the key once Connect went through; keep it for a follow-up after a Check.
    if (ok && kind === "connect" && hs.memo && reportVerdict(hs.memo.report).tone === "ok") {
      key = "";
      again = false;
    }
  }

  function onKeyEnter(e: KeyboardEvent) {
    if (e.key === "Enter" && key.trim() && anyPart && !hs.connecting) run("connect");
  }
</script>

<section class="card" id="h-owui" tabindex="-1" aria-labelledby="{uid}-title">
  <div class="hd">
    <h2 id="{uid}-title">Open WebUI</h2>
    <span class="chip {chip.cls}">{chip.text}</span>
  </div>

  {#if !showForm && memo}
    <!-- Set up, with the keys and addresses in use now -->
    <ul class="does">
      {#each PARTS as p (p.area)}
        {@const tone = areaTone(memo.report.items, p.area)}
        <li class={tone ?? "idle"}>
          <Icon name={TONE_ICON[tone ?? "idle"]} size={15} stroke={2.2} />
          <span>{p.did}{#if tone === "idle"} <small>· skipped</small>{/if}</span>
        </li>
      {/each}
    </ul>
    <p class="meta">
      {"Open WebUI" + (memo.report.version ? " " + memo.report.version : "")}{#if host}{" · "}<span class="mono">{host}</span>{/if} ·
      {memo.kind === "connect" ? "set up" : "checked"} {whenText(memo.at)}
    </p>
    <div class="note"><Icon name="key" size={15} /><span>Your Open WebUI admin key is used once for setup and never saved.</span></div>
    <div class="btns">
      <button class="small" onclick={() => (again = true)}><Icon name="retry" size={14} />Run setup again</button>
      {#if link}
        <a class="btn sm" href={link} target="_blank" rel="noreferrer"><Icon name="external" size={14} />Open Open WebUI</a>
      {/if}
    </div>
  {:else}
    <p class="sub">
      Adds the parts above to Open WebUI through its admin API and lists every change. Your other connections and
      settings stay as they are.
    </p>

    {#if memo && !current}
      <div class="callout wa">
        <Icon name="warn" size={16} />
        <span>
          <b>A key or address changed since the last setup.</b>
          <p>Connect again so Open WebUI gets the new one.</p>
        </span>
      </div>
    {/if}

    <div class="form">
    <div class="fl">
      <label for="{uid}-url">Address, as the factory reaches it</label>
      <input id="{uid}-url" type="text" bind:value={url} placeholder={st.openwebui.default_url} spellcheck="false" autocomplete="off" />
    </div>
    <div class="fl">
      <label for="{uid}-key">Admin API key</label>
      <input
        id="{uid}-key"
        type="password"
        bind:value={key}
        placeholder="sk-…"
        autocomplete="off"
        spellcheck="false"
        aria-describedby="{uid}-keyhint"
        onkeydown={onKeyEnter} />
      <p class="hint" id="{uid}-keyhint">
        In Open WebUI: Admin Panel → Settings → General → <b>Enable API Keys</b>, then Settings → Account → API keys.
        It's used for this request only and never saved.
      </p>
    </div>
    </div>

    <fieldset class="parts">
      <legend class="sr">What to set up</legend>
      {#each PARTS as p (p.area)}
        {@const tone = memo ? areaTone(memo.report.items, p.area) : null}
        <label class="part">
          <input type="checkbox" bind:checked={parts[p.area]} />
          <span class="pt">
            {p.label}
            <small>{p.hint}</small>
          </span>
          {#if tone && tone !== "idle"}
            <span class="st {tone}" title="Last result"><Icon name={TONE_ICON[tone]} size={14} stroke={2.2} /></span>
          {/if}
        </label>
      {/each}
    </fieldset>

    <button class="small ghost more" onclick={() => (advanced = !advanced)} aria-expanded={advanced} aria-controls="{uid}-adv">
      <Icon name={advanced ? "chevronDown" : "chevronRight"} size={14} />More options
    </button>
    {#if advanced}
      <div class="fl" id="{uid}-adv">
        <label for="{uid}-reach">Open WebUI reaches this computer at</label>
        <input id="{uid}-reach" type="text" bind:value={reach} placeholder="host.docker.internal" spellcheck="false" autocomplete="off" />
        <p class="hint">
          <span class="mono">host.docker.internal</span> works with Docker Desktop. If Open WebUI runs with host networking
          (Linux <span class="mono">--network host</span>), use <span class="mono">127.0.0.1</span>.
        </p>
      </div>
    {/if}

    <div class="btns">
      <button
        class="small primary"
        onclick={() => run("connect")}
        disabled={!!hs.connecting || !key.trim() || !anyPart}
        title={!key.trim() ? "Paste an admin API key first" : !anyPart ? "Tick at least one part" : "Set up the ticked parts"}>
        {#if hs.connecting === "connect"}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="plug" size={14} />{/if}Connect
      </button>
      <button
        class="small"
        onclick={() => run("check")}
        disabled={!!hs.connecting || !key.trim()}
        title="See what's set up, without changing anything">
        {#if hs.connecting === "check"}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="find" size={14} />{/if}Check
      </button>
      {#if again}
        <button class="small ghost" onclick={() => (again = false)}>Cancel</button>
      {/if}
    </div>
  {/if}

  {#if hs.connectError}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span><b>That didn't go through.</b><p>{sentence(hs.connectError)}</p></span>
    </div>
  {/if}

  {#if memo && verdict}
    <details class="rep" open={showForm && verdict.tone === "err"}>
      <summary>
        <Icon name="chevronRight" size={14} />
        <span class="stx">
          {#if showForm}
            {`Last ${memo.kind === "connect" ? "setup" : "check"}, ${whenText(memo.at)}`}<span class="vline">{" · " + verdict.line}</span>
          {:else}
            What it found
          {/if}
        </span>
      </summary>
      <ul class="report">
        {#each memo.report.items as it, i (i)}
          {@const tone = itemTone(it.status)}
          <li>
            <span class="ri {tone}"><Icon name={TONE_ICON[tone]} size={14} stroke={2.2} /></span>
            <span class="rd"><b>{AREA_LABEL[it.area] ?? it.area}</b> {it.detail}</span>
          </li>
        {/each}
      </ul>
      {#if memo.report.admin}
        <p class="meta">
          Open WebUI {memo.report.version || ""} at <span class="mono">{memo.url}</span> ·
          {memo.kind === "check" ? "nothing changed (Check only reads)" : memo.report.changed ? "changes saved" : "nothing needed changing"}
        </p>
      {/if}
    </details>
  {/if}
</section>

<style>
  .card { display: flex; flex-direction: column; min-width: 0; container-type: inline-size; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .hd { margin-bottom: 10px; }
  .sub { font-size: 12.5px; line-height: 1.45; }

  .does { list-style: none; margin: 4px 0 0; padding: 0; display: flex; flex-direction: column; gap: 7px; font-size: 13px; }
  .does li { display: flex; gap: 8px; align-items: flex-start; }
  .does li :global(.ico) { margin-top: 2px; color: var(--fa); }
  .does li.ok :global(.ico) { color: var(--ok); }
  .does li.warn :global(.ico) { color: var(--wa); }
  .does li.err :global(.ico) { color: var(--er); }
  .does small { color: var(--fa); font-size: 12px; }

  .meta { color: var(--fa); font-size: 12px; margin-top: 10px; overflow-wrap: anywhere; line-height: 1.45; }
  .meta .mono { font-size: 11.5px; color: var(--mu); }
  .note {
    margin-top: 12px;
    padding: 9px 11px;
    border-radius: 9px;
    background: var(--p2);
    font-size: 12.5px;
    color: var(--mu);
    display: flex;
    gap: 8px;
  }
  .note :global(.ico) { margin-top: 1px; color: var(--fa); }
  .btns { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
  .btns .spin { width: 12px; height: 12px; }

  .callout { margin-top: 12px; }
  .fl { display: flex; flex-direction: column; gap: 5px; margin-top: 12px; }
  .fl label { font-size: 12px; color: var(--fa); font-weight: 500; }
  .fl input { font-size: 13px; padding: 7px 10px; }
  .fl .hint { font-size: 12px; line-height: 1.45; }

  .parts { border: 0; margin: 12px 0 0; padding: 0; min-width: 0; display: flex; flex-direction: column; }
  .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; padding: 0; }
  .part {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 7px 0;
    border-top: 1px solid var(--ln);
    cursor: pointer;
  }
  .part:first-of-type { border-top: 0; }
  .part input { margin: 0; flex: none; width: 15px; height: 15px; }
  .pt { flex: 1; min-width: 0; font-size: 13px; color: var(--ink); line-height: 1.3; }
  .pt small { display: block; color: var(--mu); font-size: 12px; }
  .st { display: grid; place-items: center; }
  .st.ok { color: var(--ok); }
  .st.warn { color: var(--wa); }
  .st.err { color: var(--er); }
  .more { align-self: flex-start; margin: 6px 0 0 -10px; }

  .rep { margin-top: 12px; border-top: 1px solid var(--ln); padding-top: 10px; }
  .rep summary {
    list-style: none;
    cursor: pointer;
    display: flex;
    align-items: flex-start;
    gap: 6px;
    font-size: 12.5px;
    color: var(--mu);
    font-weight: 500;
    border-radius: 6px;
  }
  .rep summary :global(.ico) { margin-top: 2px; transition: transform 0.15s ease; flex: none; }
  .stx { flex: 1; min-width: 0; line-height: 1.45; }
  .rep[open] summary :global(.ico) { transform: rotate(90deg); }
  .rep summary::-webkit-details-marker { display: none; }
  .rep summary:hover { color: var(--ink); }
  .vline { color: var(--fa); font-weight: 400; }
  .report { list-style: none; margin: 8px 0 0; padding: 0; font-size: 12.5px; }
  .report li {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr);
    gap: 8px;
    padding: 7px 0;
    border-top: 1px solid var(--ln);
    align-items: start;
  }
  .report li:first-child { border-top: 0; }
  .ri { color: var(--fa); padding-top: 1px; }
  .ri.ok { color: var(--ok); }
  .ri.warn { color: var(--wa); }
  .ri.err { color: var(--er); }
  .rd { color: var(--mu); overflow-wrap: anywhere; line-height: 1.45; }

  /* A wide card (it spans the row below 1240 px): fields side by side, parts in a row. */
  @container (min-width: 640px) {
    .form { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); gap: 0 14px; align-items: start; }
    .parts { flex-direction: row; gap: 10px; }
    .part { flex: 1; border: 1px solid var(--ln); border-radius: 10px; padding: 9px 11px; }
    .part:first-of-type { border-top: 1px solid var(--ln); }
    .does { flex-direction: row; flex-wrap: wrap; gap: 8px 22px; }
  }
  .rd b { font-weight: 600; color: var(--ink); margin-right: 2px; }
</style>
