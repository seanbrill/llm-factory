<script lang="ts">
  // Personas: named system prompts to bake into a build or apply when running an
  // image. A list of cards, an inline editor, and where they're used.
  import Icon from "../components/Icon.svelte";
  import CopyButton from "../components/CopyButton.svelte";
  import PersonaEditor from "../components/personas/PersonaEditor.svelte";
  import { api, post } from "../lib/api";
  import { personas, toast } from "../lib/stores";
  import type { Persona } from "../lib/types";
  import { routeArg, go } from "../lib/router";
  import { untrack } from "svelte";
  import { listLoadError } from "../components/ensembles/util";

  let loading = $state(true);
  let loadError = $state("");
  async function load() {
    try {
      const r = await api<{ personas: Persona[] }>("/api/personas");
      personas.set(r.personas ?? []);
      loadError = "";
    } catch (e) {
      loadError = listLoadError(e, "personas.json");
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    load();
  });

  let editing = $state<Persona | null | undefined>(undefined); // undefined = closed, null = new
  let editorKey = $state(0);
  let topEl: HTMLElement | undefined = $state();
  function openEditor(p: Persona | null) {
    editing = p;
    editorKey++;
    queueMicrotask(() => topEl?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
  function closeEditor() {
    editing = undefined;
    if ($routeArg) go("personas");
  }
  async function saved(p: Persona) {
    closeEditor();
    await load();
    toast(`Persona “${p.name}” saved.`);
  }

  // "#personas/new" opens a new one; "#personas/<id>" opens that one's editor.
  let handledArg = "";
  $effect(() => {
    const arg = $routeArg;
    if (loading || !arg || arg === handledArg) return;
    handledArg = arg;
    untrack(() => {
      const p = arg === "new" ? null : $personas.find((x) => x.id === arg);
      if (p !== undefined) openEditor(p);
    });
  });
  async function del(p: Persona) {
    if (!confirm(`Delete the persona “${p.name}”? Images already built with it keep their prompt.`)) return;
    try {
      await post("/api/personas/delete", { id: p.id });
      toast(`Persona “${p.name}” deleted.`);
    } catch (e) {
      toast(`It wasn't deleted: ${(e as Error).message || "the factory didn't answer"}.`, 6000);
    }
    await load();
  }

  let open = $state<Record<string, boolean>>({});
  const list = $derived($personas);
  const words = (s: string) => (s.trim() ? s.trim().split(/\s+/).length : 0);
  // More than about four lines of the card: show the start, with Show all.
  const isLong = (s: string) => s.length > 220 || s.split("\n").length > 4;
</script>

<div class="pp" bind:this={topEl}>
  <div class="lead">
    <p>
      Named system prompts: who the model is and how it answers. Bake one into an image when you build a model, or apply
      one when you run an image, without rebuilding.
    </p>
    {#if editing === undefined && list.length && !loadError}<!-- the empty state has its own -->
      <button class="primary" onclick={() => openEditor(null)}><Icon name="plus" size={15} />New persona</button>
    {/if}
  </div>

  {#if editing !== undefined}
    {#key editorKey}
      <PersonaEditor persona={editing} existing={list} onclose={closeEditor} onsaved={saved} />
    {/key}
  {/if}

  {#if loading && !list.length}
    <div class="card state" aria-busy="true"><span class="spin" aria-hidden="true"></span>Loading your personas…</div>
  {:else if loadError}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span>
        <b>Your personas didn't load.</b>
        <p>{loadError}</p>
        <button class="small" onclick={load}><Icon name="retry" size={14} />Try again</button>
      </span>
    </div>
  {:else if !list.length && editing === undefined}
    <div class="card empty-state">
      <span class="ei" aria-hidden="true"><Icon name="personas" size={22} /></span>
      <h2>No personas yet</h2>
      <p>Write one once — a reviewer, a tutor, a terse analyst — and use it with any model you build or run.</p>
      <button class="primary" onclick={() => openEditor(null)}><Icon name="plus" size={15} />New persona</button>
    </div>
  {:else}
    <div class="grid">
      {#each list as p (p.id)}
        {#if editing?.id !== p.id}
          <article class="card per" aria-labelledby="per-{p.id}">
            <div class="ph">
              <span class="av" aria-hidden="true">{p.name.trim().charAt(0).toUpperCase() || "?"}</span>
              <h3 id="per-{p.id}">{p.name}</h3>
              <span class="wc">{words(p.prompt)} words</span>
            </div>
            <div class="prompt">
              <p id="per-{p.id}-text" class:clamp={isLong(p.prompt) && !open[p.id]}>{p.prompt}</p>
            </div>
            <div class="pf">
              {#if isLong(p.prompt)}
                <button class="small ghost" onclick={() => (open[p.id] = !open[p.id])} aria-expanded={!!open[p.id]} aria-controls="per-{p.id}-text">
                  {open[p.id] ? "Show less" : "Show all"}
                </button>
              {/if}
              <span class="sp"></span>
              <CopyButton text={p.prompt} label="Copy" title="Copy the prompt" />
              <button class="small" onclick={() => openEditor(p)}><Icon name="pen" size={14} />Edit</button>
              <button class="small ghost icon danger" onclick={() => del(p)} aria-label="Delete {p.name}" title="Delete">
                <Icon name="trash" size={15} />
              </button>
            </div>
          </article>
        {/if}
      {/each}
    </div>
  {/if}
</div>

<style>
  .pp { display: flex; flex-direction: column; gap: 16px; scroll-margin-top: 16px; }
  .lead { display: flex; align-items: center; gap: 18px; justify-content: space-between; }
  .lead p { color: var(--mu); font-size: 13.5px; max-width: 720px; line-height: 1.5; }
  .state { display: flex; align-items: center; gap: 10px; color: var(--mu); font-size: 13px; }
  .callout button { margin-top: 8px; }
  .empty-state { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 26px 24px; }
  .empty-state h2 { font-size: 17px; margin: 4px 0 0; }
  .empty-state p { color: var(--mu); font-size: 13px; max-width: 520px; line-height: 1.5; }
  .empty-state button { margin-top: 6px; }
  .ei { width: 44px; height: 44px; border-radius: 12px; background: var(--prs); color: var(--pr); display: grid; place-items: center; }

  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(340px, 1fr)); gap: 14px; align-items: start; }
  .per { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .ph { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .av {
    width: 30px;
    height: 30px;
    border-radius: 9px;
    background: var(--prs);
    color: var(--pr);
    display: grid;
    place-items: center;
    font: 700 14px/1 var(--font-display);
    flex: none;
  }
  h3 { font: 700 15px/1.25 var(--font-display); margin: 0; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .wc { color: var(--fa); font-size: 11.5px; white-space: nowrap; }
  .prompt {
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--cv);
    border: 1px solid var(--ln);
  }
  .prompt p {
    margin: 0;
    font-size: 13px;
    line-height: 1.55;
    color: var(--mu);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .prompt p.clamp {
    max-height: calc(13px * 1.55 * 4);
    overflow: hidden;
    mask-image: linear-gradient(to bottom, black 60%, transparent);
  }
  .pf { display: flex; align-items: center; gap: 6px; }
  .pf .sp { flex: 1; }
  .pf .ghost:first-child { margin-left: -10px; }

  @media (max-width: 760px) {
    .pp { padding-top: 12px; gap: 12px; }
    .lead { flex-direction: column; align-items: stretch; }
    .grid { grid-template-columns: minmax(0, 1fr); }
  }
</style>
