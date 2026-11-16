<script lang="ts">
  // MCP servers: tool servers a model can use when the factory runs it. Each
  // server's tools are picked here (McpTools); a run picks which servers to use.
  // The factory only ever sends a server's URL and header values back masked,
  // and this card shows header names only. "#harness/mcp" opens here.
  import { onMount } from "svelte";
  import Icon from "../Icon.svelte";
  import ConfirmDialog from "../library/ConfirmDialog.svelte";
  import McpForm from "./McpForm.svelte";
  import McpTools from "./McpTools.svelte";
  import { toast } from "../../lib/stores";
  import { ago, plural } from "../../lib/format";
  import type { ConfirmSpec } from "../../lib/library";
  import {
    listMcp,
    testMcp,
    deleteMcp,
    mcpAllowed,
    mcpState,
    tokText,
    sentence,
    MCP_TOKENS_WARN,
    type McpServer,
    type McpTest,
  } from "../../lib/harness";

  const uid = $props.id();

  let servers = $state<McpServer[]>([]);
  let manyTools = $state(15);
  let loaded = $state(false);
  let loadError = $state("");
  let reloading = $state(false);

  let adding = $state(false);
  let editing = $state("");
  let open = $state<Record<string, boolean>>({});
  let testing = $state<Record<string, boolean>>({});
  /** A test request that failed outright (the server's own errors are in its row). */
  let rowErr = $state<Record<string, string>>({});
  /** The server just added and its first test. */
  let added = $state<{ server: McpServer; test: McpTest } | null>(null);

  let confirmOpen = $state(false);
  let confirmSpec = $state<ConfirmSpec | null>(null);

  const names = $derived(servers.map((s) => s.name));
  const showForm = $derived(adding || (loaded && !loadError && !servers.length));

  async function load() {
    try {
      const r = await listMcp();
      servers = r.servers ?? [];
      if (r.many_tools > 0) manyTools = r.many_tools;
      loadError = "";
    } catch (e) {
      loadError = (e as Error).message || "The factory didn't answer.";
    } finally {
      loaded = true;
    }
  }

  onMount(() => {
    load();
  });

  async function retryLoad() {
    reloading = true;
    await load();
    reloading = false;
  }

  /** Put a server the factory sent back into the list. */
  function keep(s: McpServer) {
    const i = servers.findIndex((x) => x.name === s.name);
    if (i >= 0) servers[i] = s;
    else servers.push(s);
  }

  async function test(s: McpServer) {
    testing[s.name] = true;
    rowErr[s.name] = "";
    try {
      const t = await testMcp(s.name);
      if (t.server) keep(t.server);
      else await load();
      if (t.ok) toast(`${s.name} answered: ${plural(t.tools?.length ?? 0, "tool")}.`);
    } catch (e) {
      rowErr[s.name] = (e as Error).message || "The factory didn't answer.";
    } finally {
      testing[s.name] = false;
    }
  }

  function onAdded(r: { server: McpServer; test?: McpTest }) {
    adding = false;
    keep(r.server);
    if (r.test) {
      added = { server: r.server, test: r.test };
      // A big catalog starts with nothing picked: open the picker.
      if (r.test.ok && (r.test.tools?.length ?? 0) > manyTools) open[r.server.name] = true;
    }
    load();
  }

  function onEdited(r: { server: McpServer; test?: McpTest }) {
    editing = "";
    keep(r.server);
    rowErr[r.server.name] = "";
    if (r.test?.ok) toast(`Saved. ${r.server.name} answered: ${plural(r.test.tools?.length ?? 0, "tool")}.`);
    else toast(`Saved ${r.server.name}.`);
  }

  function askDelete(s: McpServer) {
    confirmSpec = {
      title: `Delete ${s.name}?`,
      body: "The factory forgets this MCP server: its URL, headers and the tools you picked. The server itself isn't changed.",
      confirm: "Delete",
      danger: true,
      run: async () => {
        await deleteMcp(s.name);
        servers = servers.filter((x) => x.name !== s.name);
        delete open[s.name];
        delete rowErr[s.name];
        if (added?.server.name === s.name) added = null;
        if (editing === s.name) editing = "";
        toast(`${s.name} deleted.`);
        load();
      },
    };
    confirmOpen = true;
  }

  function testedText(s: McpServer): string {
    const t = s.tested_at ? Date.parse(s.tested_at) : NaN;
    return isFinite(t) ? `Tested ${ago(t)}` : "Never tested";
  }
  function testedTitle(s: McpServer): string {
    const t = s.tested_at ? Date.parse(s.tested_at) : NaN;
    return isFinite(t) ? new Date(t).toLocaleString() : "";
  }
</script>

<section class="card" id="h-mcp" tabindex="-1" aria-labelledby="{uid}-title">
  <div class="hd">
    <h2 id="{uid}-title">MCP servers</h2>
    {#if servers.length}<span class="chip">{servers.length}</span>{/if}
    <div class="sp">
      {#if servers.length}
        <button class="small" onclick={() => (adding = !adding)} aria-expanded={adding} aria-controls="{uid}-add">
          <Icon name="plus" size={14} />Add a server
        </button>
      {/if}
    </div>
  </div>
  <p class="sub top">
    The factory's list of MCP servers. None of this is built into an image: when you run a model, the Run dialog picks
    which of these it connects to, and a running model can add more from any chat with <span class="mono">/mcp</span>.
    Here you choose which of a server's tools a model gets, and whether their descriptions go with every request in full
    or on demand.
  </p>

  {#if !loaded}
    <p class="state"><span class="spin" aria-hidden="true"></span>Loading the MCP servers…</p>
  {:else if loadError}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span>
        <b>The MCP servers didn't load.</b>
        <p>{sentence(loadError)} Check that the factory is running, then try again.</p>
        <button class="small" onclick={retryLoad} disabled={reloading}>
          {#if reloading}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="retry" size={14} />{/if}Try again
        </button>
      </span>
    </div>
  {:else}
    {#if showForm}
      <div class="add" id="{uid}-add">
        {#if !servers.length}
          <p class="empty">No MCP servers yet. Add one with its URL: the factory asks it for its tools right away.</p>
        {/if}
        <McpForm taken={names} ondone={onAdded} oncancel={servers.length ? () => (adding = false) : undefined} />
      </div>
    {/if}

    {#if added}
      {@const t = added.test}
      {@const s = servers.find((x) => x.name === added?.server.name) ?? added.server}
      <div class="callout {t.ok ? 'ok' : 'wa'}" role="status">
        <Icon name={t.ok ? "check" : "warn"} size={16} />
        <span>
          {#if t.ok}
            <b>Added {s.name}: it offers {plural(t.tools?.length ?? 0, "tool")}, {tokText(t.total_tokens)} tokens of context.</b>
            <p>
              {#if !(t.tools?.length ?? 0)}It has no tools right now.
              {:else if s.tools == null}All of them are on. Untick the ones a model doesn't need under Tools.
              {:else if !s.tools.length}None are picked yet: tick the ones a model needs below.
              {:else}{plural(s.tools.length, "tool")} picked.{/if}
            </p>
          {:else}
            <b>Added {s.name}, but it didn't list its tools.</b>
            <p>{sentence(t.error) || "The server didn't answer."} Check its URL and headers, then test it again.</p>
          {/if}
          <span class="cbtns">
            {#if !t.ok}
              <button class="small" onclick={() => ((editing = s.name), (added = null))}><Icon name="pen" size={14} />Change the URL or headers</button>
            {/if}
            <button class="small ghost" onclick={() => (added = null)}>Dismiss</button>
          </span>
        </span>
      </div>
    {/if}

    {#if servers.length}
      <ul class="list">
        {#each servers as s (s.name)}
          {@const chip = mcpState(s)}
          {@const hdrs = Object.keys(s.headers ?? {})}
          {@const cat = s.catalog ?? []}
          {@const allowed = mcpAllowed(s).length}
          {@const pickId = `${uid}-t-${s.name}`}
          <li class="srv">
            <div class="row">
              <span class="ti" aria-hidden="true"><Icon name="plug" size={16} /></span>
              <span class="who">
                <b class="nm mono">{s.name}</b>
                <span class="url mono" title={s.url}>{s.url}</span>
              </span>
              <span class="chip {chip.cls}" title={s.error ? sentence(s.error) : ""}>{chip.label}</span>
              <span class="acts">
                <button
                  class="small"
                  onclick={() => (open[s.name] = !open[s.name])}
                  aria-expanded={!!open[s.name]}
                  aria-controls={pickId}>
                  <Icon name={open[s.name] ? "chevronDown" : "chevronRight"} size={14} />Tools
                </button>
                <button class="small" onclick={() => test(s)} disabled={testing[s.name]} title="Ask the server for its tools again">
                  {#if testing[s.name]}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="retry" size={14} />{/if}Test
                </button>
                <button
                  class="small icon ghost"
                  onclick={() => (editing = editing === s.name ? "" : s.name)}
                  aria-expanded={editing === s.name}
                  aria-label="Change {s.name}'s URL or headers"
                  title="Change the URL or headers"><Icon name="pen" size={14} /></button>
                <button class="small icon ghost danger" onclick={() => askDelete(s)} aria-label="Delete {s.name}" title="Delete">
                  <Icon name="trash" size={14} />
                </button>
              </span>
            </div>

            <p class="meta">
              {#if hdrs.length}<span>Headers: <span class="mono">{hdrs.join(", ")}</span></span>{/if}
              {#if s.loopback}<span title="The factory reaches it from model containers through the engine's host name">On this computer</span>{/if}
              <span title={testedTitle(s)}>{testedText(s)}</span>
            </p>

            {#if s.error}
              <p class="err"><Icon name="errc" size={14} />{sentence(s.error)}</p>
            {/if}
            {#if rowErr[s.name]}
              <p class="err"><Icon name="errc" size={14} />The test didn't run: {sentence(rowErr[s.name])}</p>
            {/if}

            {#if cat.length}
              {#if s.tools != null && !s.tools.length}
                <p class="picks wa">
                  <Icon name="warn" size={14} />No tools are selected yet, so a model gets none from this server until you pick some.
                  {#if !open[s.name]}<button class="small ghost" onclick={() => (open[s.name] = true)}>Pick tools</button>{/if}
                </p>
              {:else}
                {@const req = s.request_tokens ?? s.selected_tokens}
                <p class="picks" class:wa={req > MCP_TOKENS_WARN}>
                  {#if req > MCP_TOKENS_WARN}<Icon name="warn" size={14} />{/if}
                  {s.tools == null ? `All ${plural(cat.length, "tool")}` : `${allowed} of ${plural(cat.length, "tool")}`} ·
                  {tokText(req)} tokens per request
                  {#if s.on_demand}<span class="chip pr" title="One line per tool; the model reads a tool's arguments when it needs them ({tokText(s.selected_tokens)} tokens in full)">On demand</span>{/if}
                </p>
              {/if}
            {:else if !s.error}
              <p class="picks">Test it to see its tools.</p>
            {/if}

            {#if editing === s.name}
              <div class="edit">
                <McpForm existing={s} ondone={onEdited} oncancel={() => (editing = "")} />
              </div>
            {/if}

            {#if open[s.name]}
              <McpTools server={s} id={pickId} onsaved={keep} />
            {/if}
          </li>
        {/each}
      </ul>
    {/if}
  {/if}

  <p class="fine">
    A model container reaches a <span class="mono">127.0.0.1</span> or <span class="mono">localhost</span> URL through
    <span class="mono">host.docker.internal</span> (Docker) or <span class="mono">host.containers.internal</span> (Podman),
    keeping the original Host header. Pick servers when you run a model.
  </p>
</section>

<ConfirmDialog bind:open={confirmOpen} spec={confirmSpec} />

<style>
  .card { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .hd { margin-bottom: -6px; flex-wrap: wrap; }
  .sub.top { font-size: 12.5px; line-height: 1.45; }
  .state { display: flex; align-items: center; gap: 10px; color: var(--mu); font-size: 13px; }
  .spin.sm { width: 12px; height: 12px; }
  .callout button { margin-top: 8px; }
  .cbtns { display: flex; flex-wrap: wrap; gap: 6px; }
  .add { display: flex; flex-direction: column; gap: 8px; }
  .empty { padding: 0; font-size: 12.5px; line-height: 1.5; }

  .list { list-style: none; margin: 0; padding: 0; }
  .srv { padding: 12px 0; border-top: 1px solid var(--ln); min-width: 0; }
  .srv:first-child { border-top: 0; padding-top: 2px; }
  .row {
    display: grid;
    grid-template-columns: 32px minmax(0, 1fr) auto auto;
    gap: 12px;
    align-items: center;
  }
  .ti { width: 32px; height: 32px; border-radius: 9px; background: var(--p3); display: grid; place-items: center; color: var(--mu); }
  .who { display: flex; flex-direction: column; min-width: 0; }
  .nm { font-size: 13px; font-weight: 600; color: var(--ink); overflow-wrap: anywhere; }
  .url { font-size: 12px; color: var(--mu); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .acts { display: flex; gap: 6px; align-items: center; }

  .meta, .picks, .err {
    margin: 6px 0 0 44px;
    font-size: 12px;
    line-height: 1.45;
  }
  .meta { display: flex; flex-wrap: wrap; gap: 0 14px; color: var(--fa); }
  .meta .mono { font-size: 11.5px; color: var(--mu); }
  .picks { display: flex; align-items: center; flex-wrap: wrap; gap: 4px 6px; color: var(--mu); font-variant-numeric: tabular-nums; }
  .picks.wa { color: var(--wa); }
  .picks button { min-height: 24px; height: 24px; }
  .err { display: flex; align-items: flex-start; gap: 6px; color: var(--er); overflow-wrap: anywhere; }
  .err :global(.ico), .picks :global(.ico) { margin-top: 1px; flex: none; }
  .edit { margin: 10px 0 0 44px; }
  .srv > :global(.pick) { margin-left: 44px; }

  .fine { color: var(--fa); font-size: 12px; line-height: 1.5; padding-top: 4px; }
  .fine .mono { font-size: 11.5px; color: var(--mu); }

  @media (max-width: 760px) {
    .row { grid-template-columns: 32px minmax(0, 1fr) auto; }
    .acts { grid-column: 1 / -1; flex-wrap: wrap; }
    .meta, .picks, .err, .edit, .srv > :global(.pick) { margin-left: 0; }
  }
</style>
