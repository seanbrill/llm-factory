<script lang="ts">
  // One MCP server's tool picker: tick the tools a model gets from it. Every
  // ticked tool's schema goes with every request, so the running total says what
  // they cost in context. Saving sends the ticked list (PUT tools: [...]).
  import { untrack } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import Icon from "../Icon.svelte";
  import { toast } from "../../lib/stores";
  import { plural } from "../../lib/format";
  import { updateMcp, mcpAllowed, tokText, sentence, MCP_TOKENS_WARN, type McpServer } from "../../lib/harness";

  let { server, id, onsaved }: { server: McpServer; id: string; onsaved: (s: McpServer) => void } = $props();

  const catalog = $derived(server.catalog ?? []);
  // What the server gives a model now, and the draft (starts from it).
  const saved = $derived(new Set(mcpAllowed(server).map((t) => t.name)));
  const picked = new SvelteSet<string>(untrack(() => mcpAllowed(server).map((t) => t.name)));

  // When the server changes under an untouched draft (a test found new tools, a
  // save went through), the draft follows it; edits in progress are kept.
  let base = untrack(() => new Set(saved));
  $effect(() => {
    const now = saved;
    untrack(() => {
      const untouched = picked.size === base.size && [...base].every((n) => picked.has(n));
      if (untouched) {
        picked.clear();
        for (const n of now) picked.add(n);
      }
      base = new Set(now);
    });
  });

  let q = $state("");
  let saving = $state(false);
  let error = $state("");

  const shown = $derived.by(() => {
    const s = q.trim().toLowerCase();
    if (!s) return catalog;
    return catalog.filter((t) => t.name.toLowerCase().includes(s) || (t.description || "").toLowerCase().includes(s));
  });
  const count = $derived(catalog.filter((t) => picked.has(t.name)).length);
  const total = $derived(catalog.reduce((n, t) => n + (picked.has(t.name) ? t.tokens || 0 : 0), 0));
  const dirty = $derived(count !== saved.size || catalog.some((t) => picked.has(t.name) !== saved.has(t.name)));
  const heavy = $derived(total > MCP_TOKENS_WARN);

  const oneLine = (s: string) => (s || "").replace(/\s+/g, " ").trim();

  function tick(name: string, on: boolean) {
    if (on) picked.add(name);
    else picked.delete(name);
  }
  function all(on: boolean) {
    for (const t of shown) tick(t.name, on);
  }
  function undo() {
    picked.clear();
    for (const n of saved) picked.add(n);
  }

  async function save() {
    saving = true;
    error = "";
    try {
      const tools = catalog.filter((t) => picked.has(t.name)).map((t) => t.name);
      const r = await updateMcp(server.name, { tools });
      onsaved(r.server);
      toast(tools.length ? `${server.name}: ${plural(tools.length, "tool")} picked.` : `${server.name}: no tools picked.`);
    } catch (e) {
      error = (e as Error).message || "The factory didn't answer.";
    } finally {
      saving = false;
    }
  }
</script>

<div class="pick" {id}>
  {#if !catalog.length}
    <p class="none">
      {#if server.error}The last test failed, so the factory doesn't know this server's tools yet. Fix the address or headers,
        then test it again.{:else}Test it to see its tools.{/if}
    </p>
  {:else}
    <div class="tbar">
      <span class="lbl">Tools a model gets</span>
      {#if catalog.length > 10}
        <input class="flt" type="search" bind:value={q} placeholder="Filter {catalog.length} tools" aria-label="Filter the tools" spellcheck="false" />
      {/if}
      <span class="sel">
        <button class="small ghost" onclick={() => all(true)} title={q ? "Tick the tools shown" : "Tick every tool"}>Select all</button>
        <button class="small ghost" onclick={() => all(false)} title={q ? "Untick the tools shown" : "Untick every tool"}>None</button>
      </span>
    </div>

    {#if !saved.size && !picked.size}
      <p class="hint nopick">
        <Icon name="info" size={14} />No tools are selected yet, so a model gets none from this server until you pick some.
      </p>
    {/if}

    <ul class="tools" aria-label="{server.name} tools">
      {#each shown as t (t.name)}
        <li>
          <label class="tool" title={oneLine(t.description)}>
            <input type="checkbox" checked={picked.has(t.name)} onchange={(e) => tick(t.name, e.currentTarget.checked)} />
            <span class="tn mono">{t.name}</span>
            <span class="tk">{tokText(t.tokens)} tok</span>
            <span class="td">{oneLine(t.description) || "No description"}</span>
          </label>
        </li>
      {:else}
        <li class="nomatch">No tool matches “{q.trim()}”.</li>
      {/each}
    </ul>

    <p class="sum" class:wa={heavy} aria-live="polite">
      <b>{tokText(total)} tokens of context</b> · {count} of {plural(catalog.length, "tool")} ticked
    </p>

    {#if heavy}
      <div class="callout wa">
        <Icon name="warn" size={16} />
        <span>
          <b>These tools take more than a quarter of a 32K context.</b>
          <p>
            Every request carries their schemas, and small local contexts (8–32K) fill up fast: there's less room for the
            chat and the model's answer. Pick fewer tools.
          </p>
        </span>
      </div>
    {/if}

    {#if dirty && server.tools == null}
      <p class="fine">Now it gets every tool, including ones the server adds later. Saving keeps just the ticked ones.</p>
    {/if}

    {#if error}
      <div class="callout er" role="alert">
        <Icon name="errc" size={16} />
        <span><b>The tools weren't saved.</b><p>{sentence(error)}</p></span>
      </div>
    {/if}

    <div class="btns">
      <button class="small primary" onclick={save} disabled={!dirty || saving}>
        {#if saving}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="check" size={14} />{/if}Save tools
      </button>
      {#if dirty}<button class="small ghost" onclick={undo} disabled={saving}>Undo changes</button>{/if}
    </div>
  {/if}
</div>

<style>
  .pick {
    margin-top: 10px;
    padding: 12px;
    border-radius: 10px;
    background: var(--p2);
    container-type: inline-size;
    min-width: 0;
  }
  .none { color: var(--mu); font-size: 12.5px; line-height: 1.5; }
  .tbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 8px; }
  .flt { width: auto; flex: 1 1 160px; max-width: 280px; font-size: 12.5px; padding: 5px 9px; }
  .sel { margin-left: auto; display: flex; gap: 2px; }
  .nopick { display: flex; align-items: center; gap: 6px; font-size: 12.5px; margin-bottom: 8px; }
  .nopick :global(.ico) { color: var(--wa); }

  .tools {
    list-style: none;
    margin: 0;
    padding: 0 4px 0 0;
    max-height: 340px;
    overflow-y: auto;
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 0 18px;
  }
  @container (min-width: 720px) {
    .tools { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  .tool {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr) auto;
    gap: 1px 9px;
    align-items: center;
    padding: 6px 0;
    border-top: 1px solid var(--ln);
    cursor: pointer;
  }
  .tool input { margin: 0; width: 15px; height: 15px; }
  .tn { color: var(--ink); font-size: 12.5px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tk { color: var(--fa); font-size: 11.5px; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .td {
    grid-column: 2 / -1;
    color: var(--mu);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .nomatch { color: var(--mu); font-size: 12.5px; padding: 8px 0; }

  .sum { margin-top: 10px; font-size: 12.5px; color: var(--mu); }
  .sum b { color: var(--ink); font-weight: 600; font-variant-numeric: tabular-nums; }
  .sum.wa b { color: var(--wa); }
  .callout { margin-top: 10px; }
  .fine { margin-top: 8px; color: var(--fa); font-size: 12px; line-height: 1.45; }
  .btns { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
</style>
