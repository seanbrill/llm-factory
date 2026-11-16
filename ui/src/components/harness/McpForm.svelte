<script lang="ts">
  // Add an MCP server, or change one's address and headers. Header values and
  // the URL are secrets: they're typed here, sent once, and the factory only
  // ever shows them masked. Editing keeps a header's stored value unless a new
  // one is typed (it sends MCP_MASK back for it). The factory tests a new
  // server right away; an edited one is tested after saving.
  import { untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import {
    addMcp,
    updateMcp,
    testMcp,
    sentence,
    MCP_MASK,
    MCP_NAME,
    type McpServer,
    type McpTest,
  } from "../../lib/harness";

  let {
    existing = null,
    taken = [],
    ondone,
    oncancel,
  }: {
    /** Edit this server (null = add one). */
    existing?: McpServer | null;
    /** Names already in use. */
    taken?: string[];
    ondone: (r: { server: McpServer; test?: McpTest }) => void;
    /** Leave out to hide Cancel. */
    oncancel?: () => void;
  } = $props();

  const uid = $props.id();

  interface Row {
    key: number;
    name: string;
    value: string;
    /** A header the factory already stores: its name is fixed, a blank value keeps it. */
    kept: boolean;
  }

  let seq = 0;
  let name = $state("");
  let url = $state("");
  let rows = $state<Row[]>(
    untrack(() => Object.keys(existing?.headers ?? {}).map((n) => ({ key: seq++, name: n, value: "", kept: true }))),
  );
  let busy = $state(false);
  let error = $state("");
  let tried = $state(false);

  const editing = $derived(!!existing);
  const HEADER_NAME = /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/;

  // The first thing that stops a save, as a sentence ("" = fine).
  const problem = $derived.by((): string => {
    if (!editing) {
      const n = name.trim();
      if (!n) return "Name the server.";
      if (!MCP_NAME.test(n)) return "Use lowercase letters, digits, - and _ in the name, up to 32.";
      if (taken.includes(n)) return `There's already a server called ${n}.`;
      if (!url.trim()) return "Paste the server's URL.";
    }
    if (url.trim() && !/^https?:\/\/\S+$/i.test(url.trim())) return "Start the URL with http:// or https://.";
    const seen = new Set<string>();
    for (const r of rows) {
      const h = r.name.trim();
      if (!h) {
        if (r.value) return "Name each header that has a value.";
        continue;
      }
      if (!HEADER_NAME.test(h)) return `“${h}” isn't a header name: no spaces or colons.`;
      if (seen.has(h.toLowerCase())) return `The ${h} header is there twice.`;
      seen.add(h.toLowerCase());
      if (!r.kept && !r.value) return `Type a value for ${h}.`;
    }
    return "";
  });

  // Editing: anything to save at all?
  const changed = $derived.by(() => {
    if (!editing) return true;
    if (url.trim()) return true;
    const before = Object.keys(existing?.headers ?? {});
    const named = rows.filter((r) => r.name.trim());
    if (named.length !== before.length) return true;
    return named.some((r) => !r.kept || r.value);
  });

  function headerMap(): Record<string, string> {
    const m: Record<string, string> = {};
    for (const r of rows) {
      const h = r.name.trim();
      if (h) m[h] = r.kept && !r.value ? MCP_MASK : r.value;
    }
    return m;
  }

  function addRow() {
    rows.push({ key: seq++, name: "", value: "", kept: false });
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    tried = true;
    if (problem || !changed || busy) return;
    busy = true;
    error = "";
    try {
      if (!existing) {
        const headers = headerMap();
        const r = await addMcp({ name: name.trim(), url: url.trim(), ...(Object.keys(headers).length ? { headers } : {}) });
        url = "";
        rows = [];
        ondone(r);
      } else {
        const r = await updateMcp(existing.name, { ...(url.trim() ? { url: url.trim() } : {}), headers: headerMap() });
        url = "";
        rows = [];
        // A new address or key: ask the server for its tools again.
        let test: McpTest | undefined;
        try {
          test = await testMcp(existing.name);
        } catch {
          test = undefined;
        }
        ondone({ server: test?.server ?? r.server, test });
      }
    } catch (err) {
      error = (err as Error).message || "The factory didn't answer.";
    } finally {
      busy = false;
    }
  }
</script>

<form class="mf" onsubmit={submit} aria-label={editing ? `Change ${existing?.name}` : "Add an MCP server"} novalidate>
  <div class="row1" class:edit={editing}>
    {#if !editing}
      <div class="fl nm">
        <label for="{uid}-name">Name</label>
        <input
          id="{uid}-name"
          type="text"
          bind:value={name}
          placeholder="github"
          maxlength="32"
          autocomplete="off"
          spellcheck="false"
          autocapitalize="off" />
      </div>
    {/if}
    <div class="fl">
      <label for="{uid}-url">{editing ? "New URL" : "URL"}</label>
      <input
        id="{uid}-url"
        type="text"
        inputmode="url"
        bind:value={url}
        placeholder={editing ? existing?.url : "http://127.0.0.1:8931/mcp"}
        autocomplete="off"
        spellcheck="false"
        autocapitalize="off"
        aria-describedby={editing ? `${uid}-urlhint` : undefined} />
      {#if editing}
        <p class="hint" id="{uid}-urlhint">Leave it empty to keep the address. Its path can hold a secret, so it isn't shown.</p>
      {/if}
    </div>
  </div>

  <fieldset class="hdrs">
    <legend class="lgd">Headers <span>· optional, such as Authorization</span></legend>
    {#each rows as r, i (r.key)}
      <div class="hr">
        {#if r.kept}
          <span class="hn mono" title="Stored header">{r.name}</span>
        {:else}
          <input
            type="text"
            bind:value={r.name}
            placeholder="Authorization"
            aria-label="Header {i + 1} name"
            autocomplete="off"
            spellcheck="false"
            autocapitalize="off" />
        {/if}
        <input
          type="password"
          bind:value={r.value}
          placeholder={r.kept ? "Unchanged" : "Bearer …"}
          aria-label="{r.name.trim() || `Header ${i + 1}`} value"
          autocomplete="off"
          spellcheck="false" />
        <button
          type="button"
          class="small icon ghost"
          onclick={() => rows.splice(i, 1)}
          aria-label="Remove the {r.name.trim() || `header ${i + 1}`} header"
          title="Remove this header"><Icon name="x" size={14} /></button>
      </div>
    {/each}
    <button type="button" class="small ghost addh" onclick={addRow}><Icon name="plus" size={14} />Add a header</button>
  </fieldset>

  {#if tried && problem}
    <p class="bad" role="alert"><Icon name="warn" size={14} />{problem}</p>
  {/if}
  {#if error}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span><b>{editing ? "The changes weren't saved." : "The server wasn't added."}</b><p>{sentence(error)}</p></span>
    </div>
  {/if}

  <div class="btns">
    <button type="submit" class="small primary" disabled={busy || (editing && !changed)}>
      {#if busy}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name={editing ? "check" : "plus"} size={14} />{/if}
      {editing ? "Save and test" : busy ? "Adding and testing…" : "Add and test"}
    </button>
    {#if oncancel}<button type="button" class="small ghost" onclick={oncancel} disabled={busy}>Cancel</button>{/if}
  </div>
</form>

<style>
  .mf {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 12px;
    border-radius: 10px;
    background: var(--p2);
    container-type: inline-size;
    min-width: 0;
  }
  .row1 { display: grid; grid-template-columns: minmax(0, 1fr); gap: 10px; }
  @container (min-width: 560px) {
    .row1:not(.edit) { grid-template-columns: minmax(120px, 0.6fr) minmax(0, 2fr); }
  }
  .fl { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .fl label, .lgd { font-size: 12px; color: var(--fa); font-weight: 500; }
  .lgd span { font-weight: 400; }
  input { font-size: 13px; padding: 7px 10px; }
  .fl .hint { font-size: 12px; line-height: 1.45; }

  .hdrs { border: 0; margin: 0; padding: 0; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
  .lgd { padding: 0; margin-bottom: 1px; }
  .hr { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.4fr) 28px; gap: 6px; align-items: center; }
  .hn {
    font-size: 12.5px;
    color: var(--ink);
    padding: 0 2px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .addh { align-self: flex-start; margin-left: -10px; }

  .bad { display: flex; align-items: center; gap: 6px; color: var(--wa); font-size: 12.5px; }
  .btns { display: flex; flex-wrap: wrap; gap: 8px; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
</style>
