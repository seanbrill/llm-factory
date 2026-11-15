<script lang="ts">
  // Ensembles: several built images behind one model, with a Conductor that sends
  // each request to the right member. Saved definitions, an editor, and each
  // one's image build (through the shared build queue).
  import Icon from "../components/Icon.svelte";
  import EnsembleCard from "../components/ensembles/EnsembleCard.svelte";
  import EnsembleEditor from "../components/ensembles/EnsembleEditor.svelte";
  import HowItWorks from "../components/ensembles/HowItWorks.svelte";
  import { api, post } from "../lib/api";
  import { ensembles, toast } from "../lib/stores";
  import { build, watchQueue } from "../lib/build.svelte";
  import { live, watch, refresh } from "../lib/live.svelte";
  import { plural } from "../lib/format";
  import type { Ensemble } from "../lib/types";
  import { ensembleRef, listLoadError } from "../components/ensembles/util";
  import { routeArg, go } from "../lib/router";
  import { untrack } from "svelte";

  $effect(() => watch("images"));

  // ---- the list (own load, so a failure shows instead of an empty list) --------
  let loading = $state(true);
  let loadError = $state("");
  async function load() {
    try {
      const r = await api<{ ensembles: Ensemble[] }>("/api/ensembles");
      ensembles.set(r.ensembles ?? []);
      loadError = "";
    } catch (e) {
      loadError = listLoadError(e, "ensembles.json");
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    load();
  });

  // ---- editor --------------------------------------------------------------------------
  let editing = $state<Ensemble | null | undefined>(undefined); // undefined = closed, null = new
  let editorKey = $state(0);
  let topEl: HTMLElement | undefined = $state();
  function openEditor(e: Ensemble | null) {
    editing = e;
    editorKey++;
    refresh("images");
    queueMicrotask(() => topEl?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
  function closeEditor() {
    editing = undefined;
    if ($routeArg) go("ensembles");
  }
  async function saved(e: Ensemble) {
    closeEditor();
    await load();
    toast(`Ensemble “${e.name}” saved.`);
  }

  // "#ensembles/new" opens a new one; "#ensembles/<id>" opens that one's editor.
  let handledArg = "";
  $effect(() => {
    const arg = $routeArg;
    if (loading || !arg || arg === handledArg) return;
    handledArg = arg;
    untrack(() => {
      const e = arg === "new" ? null : $ensembles.find((x) => x.id === arg);
      if (e !== undefined) openEditor(e);
    });
  });

  async function del(e: Ensemble) {
    if (!confirm(`Delete the ensemble “${e.name}”? Its built image stays in the Library.`)) return;
    try {
      await post("/api/ensembles/delete", { id: e.id });
      toast(`Ensemble “${e.name}” deleted.`);
    } catch (err) {
      toast(`It wasn't deleted: ${(err as Error).message || "the factory didn't answer"}.`, 6000);
    }
    await load();
  }

  // ---- builds ----------------------------------------------------------------------------
  let starting = $state<Record<string, boolean>>({});
  let startErrors = $state<Record<string, string>>({});
  async function buildImage(e: Ensemble) {
    starting[e.id] = true;
    startErrors[e.id] = "";
    try {
      const r = await post<{ id: string; position: number }>("/api/ensemble/build", { id: e.id, compute: e.compute });
      toast(r.position > 0 ? `“${e.name}” is queued: ${plural(r.position, "build")} ahead.` : `Building “${e.name}”.`);
    } catch (err) {
      startErrors[e.id] = (err as Error).message || "The factory didn't answer.";
    } finally {
      starting[e.id] = false;
      watchQueue();
    }
  }

  // The shared queue toasts model builds only; say when an ensemble build ends.
  const seen = new Map<string, string>();
  $effect(() => {
    for (const j of build.jobs) {
      if (j.kind !== "ensemble") continue;
      const before = seen.get(j.id);
      seen.set(j.id, j.status);
      if (!before || before === j.status || j.active) continue;
      const name = $ensembles.find((e) => ensembleRef(e) === j.ref)?.name ?? j.name;
      if (j.status === "done") {
        toast(`“${name}” is built. Run it from the Library.`);
        refresh("images");
      } else if (j.status === "error") toast(`“${name}” didn't build: ${j.error || "see its log"}.`, 9000);
      else if (j.status === "canceled") toast(`The build of “${name}” was canceled.`);
    }
  });

  const list = $derived($ensembles);
</script>

<div class="ep" bind:this={topEl}>
  <div class="lead">
    <p>
      Put several built images behind one model. A small <b>Conductor</b> sends each request to the right member — chat,
      vision, image or video generation, speech — and starts members only when they're needed.
    </p>
    {#if editing === undefined && list.length && !loadError}<!-- the empty state has its own -->
      <button class="primary" onclick={() => openEditor(null)}><Icon name="plus" size={15} />New ensemble</button>
    {/if}
  </div>

  <div class="cols">
    <div class="main">
      {#if editing !== undefined}
        {#key editorKey}
          <EnsembleEditor ens={editing} existing={list} onclose={closeEditor} onsaved={saved} />
        {/key}
      {/if}

      {#if loading && !list.length}
        <div class="card state" aria-busy="true"><span class="spin" aria-hidden="true"></span>Loading your ensembles…</div>
      {:else if loadError}
        <div class="callout er" role="alert">
          <Icon name="errc" size={16} />
          <span>
            <b>Your ensembles didn't load.</b>
            <p>{loadError}</p>
            <button class="small" onclick={load}><Icon name="retry" size={14} />Try again</button>
          </span>
        </div>
      {:else if !list.length && editing === undefined}
        <div class="card empty-state">
          <span class="ei" aria-hidden="true"><Icon name="ensembles" size={22} /></span>
          <h2>No ensembles yet</h2>
          <p>
            Make one from images you've built: a chat model, plus any of vision, image generation, video or speech.
            {#if live.loaded.images && !live.images.length}Nothing is built yet, so start in the Catalog.{/if}
          </p>
          <button class="primary" onclick={() => openEditor(null)}><Icon name="plus" size={15} />New ensemble</button>
        </div>
      {/if}

      {#each list as e (e.id)}
        {#if editing?.id !== e.id}
          <EnsembleCard
            ens={e}
            starting={!!starting[e.id]}
            startError={startErrors[e.id] || ""}
            onbuild={() => buildImage(e)}
            onedit={() => openEditor(e)}
            ondelete={() => del(e)} />
        {/if}
      {/each}
    </div>

    <HowItWorks />
  </div>
</div>

<style>
  .ep { display: flex; flex-direction: column; gap: 16px; scroll-margin-top: 16px; }
  .lead { display: flex; align-items: center; gap: 18px; justify-content: space-between; }
  .lead p { color: var(--mu); font-size: 13.5px; max-width: 720px; line-height: 1.5; }
  .lead b { color: var(--ink); font-weight: 600; }
  .cols { display: grid; grid-template-columns: minmax(0, 1fr) 340px; gap: 16px; align-items: start; }
  .main { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
  .state { display: flex; align-items: center; gap: 10px; color: var(--mu); font-size: 13px; }
  .callout button { margin-top: 8px; }
  .empty-state { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 26px 24px; }
  .empty-state h2 { font-size: 17px; margin: 4px 0 0; }
  .empty-state p { color: var(--mu); font-size: 13px; max-width: 520px; line-height: 1.5; }
  .empty-state button { margin-top: 6px; }
  .ei { width: 44px; height: 44px; border-radius: 12px; background: var(--prs); color: var(--pr); display: grid; place-items: center; }

  @media (max-width: 1100px) {
    .cols { grid-template-columns: minmax(0, 1fr); }
  }
  @media (max-width: 760px) {
    .ep { padding-top: 12px; gap: 12px; }
    .lead { flex-direction: column; align-items: stretch; }
  }
</style>
