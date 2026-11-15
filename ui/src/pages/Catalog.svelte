<script lang="ts">
  // Catalog (sheet frame f8): pick any number of models and build them as one
  // batch. Every card says where the model can run on this machine right now,
  // and why not when it can't. Quick picks select a working set in one click;
  // the picks live in the build plan (lib/plan.svelte.ts), reviewed on #plan.
  // #catalog/<model id> opens that model's details.
  import Icon from "../components/Icon.svelte";
  import CodeLine from "../components/CodeLine.svelte";
  import ModelCard from "../components/catalog/ModelCard.svelte";
  import ModelDrawer from "../components/catalog/ModelDrawer.svelte";
  import QuickPicks from "../components/catalog/QuickPicks.svelte";
  import Tray from "../components/catalog/Tray.svelte";
  import { api } from "../lib/api";
  import { catalog, sysinfo, buildTemplate, loadSysInfo, toast } from "../lib/stores";
  import { routeArg, go } from "../lib/router";
  import { live, watch } from "../lib/live.svelte";
  import { build } from "../lib/build.svelte";
  import {
    plan, inPlan, togglePlan, addToPlan, removeFromPlan, clearPlan, quickBuild,
    browse, picks, savePick, removePick, pre, checkPlan, planKey, preflightFor,
  } from "../lib/plan.svelte";
  import {
    KINDS, kindOf, fitFor, cpuPath, gpuPath, runnable, libraryIndex, libFor, sortModels, SORTS,
    quickPicks, schedule, shortName, type QuickPick,
  } from "../lib/catalog";
  import { META, TAG_LIST, tip } from "../lib/meta";
  import type { Model } from "../lib/types";

  $effect(() => watch("images", "containers", "files"));

  // ---- loading ------------------------------------------------------------------
  let loading = $state(false);
  let loadErr = $state("");
  async function loadCatalog() {
    loading = true;
    loadErr = "";
    try {
      catalog.set((await api<Model[]>("/api/catalog")) ?? []);
    } catch (e) {
      loadErr = (e as Error).message || "the factory didn't answer";
    } finally {
      loading = false;
    }
  }
  // The app loads the catalog at start; ask again (and learn why) if it came back empty.
  let asked = false;
  $effect(() => {
    if (asked) return;
    asked = true;
    if (!$catalog.length) loadCatalog();
    if (!$sysinfo) loadSysInfo();
  });

  // Cloning an image (Library): the Build plan reads its settings.
  $effect(() => {
    if ($buildTemplate) go("plan");
  });

  // The tray shows the server's preflight for the plan.
  $effect(() => {
    planKey(plan.items);
    checkPlan(400);
  });

  // ---- data -----------------------------------------------------------------------
  const lib = $derived(libraryIndex(live.images, live.containers, live.files, build.jobs));
  const byId = $derived(new Map($catalog.map((m) => [m.id, m])));
  const legacyCount = $derived($catalog.filter((m) => m.legacy).length);
  const pool = $derived(browse.legacy ? $catalog : $catalog.filter((m) => !m.legacy));
  const fits = $derived(new Map(pool.map((m) => [m.id, fitFor(m, $sysinfo, libFor(lib, m.id))])));
  const sizeOk = (m: Model) => !browse.maxSize || (m.size_gb || 0) <= browse.maxSize;

  // Only strength tags some listed model carries.
  const tagsAvail = $derived(TAG_LIST.filter((t) => pool.some((m) => (META[m.id]?.strengths ?? []).includes(t))));

  function matches(m: Model, ignoreKind = false): boolean {
    if (!ignoreKind && browse.kind && kindOf(m) !== browse.kind) return false;
    if (!sizeOk(m)) return false;
    const tags = META[m.id]?.strengths ?? [];
    if (browse.tags.length && !browse.tags.some((t) => tags.includes(t))) return false;
    if (browse.fitsOnly && !fits.get(m.id)?.runsNow) return false;
    const q = browse.q.trim().toLowerCase();
    if (q) {
      const hay = `${m.name} ${m.id} ${m.params} ${m.description ?? ""} ${tags.join(" ")}`.toLowerCase();
      if (!q.split(/\s+/).every((w) => hay.includes(w))) return false;
    }
    return true;
  }

  const facet = $derived(pool.filter((m) => matches(m, true)));
  const kinds = $derived(
    KINDS.filter((k) => pool.some((m) => kindOf(m) === k.id)).map((k) => ({
      ...k,
      n: facet.filter((m) => kindOf(m) === k.id).length,
    })),
  );
  const shown = $derived(sortModels(facet.filter((m) => !browse.kind || kindOf(m) === browse.kind), browse.sort, $sysinfo));
  const filterCount = $derived(browse.tags.length + (browse.maxSize ? 1 : 0) + (browse.legacy ? 1 : 0));

  function toggleTag(t: string) {
    browse.tags = browse.tags.includes(t) ? browse.tags.filter((x) => x !== t) : [...browse.tags, t];
  }
  function clearFilters() {
    browse.q = "";
    browse.kind = "";
    browse.tags = [];
    browse.maxSize = 0;
    browse.fitsOnly = false;
  }

  // Engines: nothing builds while neither answers.
  const allDown = $derived(!!$sysinfo?.engines?.length && $sysinfo.engines.every((e) => !e.reachable));

  // ---- quick picks ---------------------------------------------------------------------
  const runsHere = (m: Model) => runnable(cpuPath(m, $sysinfo)) || runnable(gpuPath(m, $sysinfo));
  const builtIds = $derived(new Set([...lib.entries()].filter(([, e]) => e.images.length).map(([id]) => id)));
  const allPicks = $derived<QuickPick[]>([
    ...($sysinfo ? quickPicks($catalog, runsHere) : []),
    ...picks.saved
      .map((p) => ({ id: p.id, label: p.label, custom: true, models: p.ids.map((id) => byId.get(id)).filter((m): m is Model => !!m) }))
      .filter((p) => p.models.length),
  ]);
  // A pick adds what isn't built yet; it's "on" when all of that is in the plan.
  const wanted = (p: QuickPick) => {
    const w = p.models.filter((m) => !builtIds.has(m.id));
    return w.length ? w : p.models;
  };
  const pickOn = (p: QuickPick) => wanted(p).every((m) => inPlan(m.id));

  function applyPick(p: QuickPick) {
    const w = wanted(p);
    if (pickOn(p)) {
      for (const m of w) removeFromPlan(m.id);
      toast(`Removed ${p.label} from the plan.`);
      return;
    }
    const added = w.filter((m) => !inPlan(m.id));
    for (const m of added) addToPlan(m, $sysinfo);
    const skipped = p.models.filter((m) => !w.includes(m));
    toast(
      `Added ${added.map(shortName).join(", ")}.` +
        (skipped.length ? ` ${skipped.map(shortName).join(", ")} ${skipped.length > 1 ? "are" : "is"} already in your library.` : ""),
      5000,
    );
  }
  function forgetPick(p: QuickPick) {
    removePick(p.id);
    toast(`Forgot the pick ${p.label}.`);
  }
  function saveCurrent(label: string) {
    savePick(label, plan.items.map((i) => i.model_id));
    toast(`Saved ${plan.items.length} model${plan.items.length > 1 ? "s" : ""} as ${label.trim()}.`);
  }

  // ---- tray ----------------------------------------------------------------------------
  const planModels = $derived(plan.items.map((i) => byId.get(i.model_id)).filter((m): m is Model => !!m));
  const trayDownload = $derived.by((): number | null => {
    if (pre.result && !pre.loading) return pre.result.download_gb;
    if (!live.loaded.files) return null;
    return planModels.reduce((a, m) => a + (m.file && !libFor(lib, m.id).onDisk ? m.size_gb || 0 : 0), 0);
  });
  const trayPlan = $derived.by(() => {
    let blocked = 0;
    const items = [];
    for (const it of plan.items) {
      const pf = preflightFor(it);
      if (pf?.error) {
        blocked++;
        continue;
      }
      items.push({
        key: it.model_id, label: it.model_id, engine: it.engine, compute: pf?.compute ?? it.compute,
        eta: pf?.eta_seconds ?? 0, transfer: pf?.transfer ? pf.build_engine : "",
      });
    }
    const s = schedule(items, build.jobs);
    return { blocked, eta: s.known && items.length ? s.total : 0 };
  });

  // ---- details drawer --------------------------------------------------------------------
  const openModel = $derived($routeArg ? byId.get($routeArg) : undefined);
  function openDetails(m: Model) {
    go("catalog", m.id);
  }
  function closeDetails() {
    go("catalog");
  }
  async function buildNow(m: Model) {
    const r = await quickBuild(m, $sysinfo);
    if (r.ok) closeDetails();
  }
</script>

<div class="cat">
  <div class="tools">
    <label class="search">
      <Icon name="find" size={16} />
      <span class="sr">Search the catalog</span>
      <input type="search" bind:value={browse.q} placeholder={`Search ${pool.length} models`} autocomplete="off" spellcheck="false" />
    </label>
    <button type="button" class="filt" class:on={browse.filtersOpen || filterCount > 0} onclick={() => (browse.filtersOpen = !browse.filtersOpen)}
      aria-expanded={browse.filtersOpen} aria-controls="cat-filters">
      <Icon name="sliders" size={15} />Filters{#if filterCount}<span class="n">{filterCount}</span>{/if}
    </button>
    <label class="toggle">
      <input type="checkbox" class="tg" bind:checked={browse.fitsOnly} />Only what fits
    </label>
    <label class="sel-sort">
      <span>Sort</span>
      <select bind:value={browse.sort} aria-label="Sort models">
        {#each SORTS as s (s.id)}<option value={s.id}>{s.label}</option>{/each}
      </select>
      <Icon name="chevronDown" size={15} />
    </label>
  </div>

  {#if browse.filtersOpen}
    <div class="filters" id="cat-filters">
      {#if tagsAvail.length}
        <div class="frow">
          <span class="fl">Strengths</span>
          <div class="chips">
            {#each tagsAvail as t (t)}
              <button type="button" class="fchip" class:on={browse.tags.includes(t)} aria-pressed={browse.tags.includes(t)} title={tip("tag:" + t)} onclick={() => toggleTag(t)}>{t}</button>
            {/each}
          </div>
        </div>
      {/if}
      <div class="frow">
        <span class="fl">Size</span>
        <div class="chips">
          {#each [0, 2, 4, 8, 16] as sz (sz)}
            <button type="button" class="fchip" class:on={browse.maxSize === sz} aria-pressed={browse.maxSize === sz} onclick={() => (browse.maxSize = sz)}>{sz ? `≤ ${sz} GB` : "Any size"}</button>
          {/each}
        </div>
      </div>
      <div class="frow">
        <span class="fl">Older</span>
        <label class="toggle">
          <input type="checkbox" class="tg" bind:checked={browse.legacy} />
          Show {legacyCount} older model{legacyCount === 1 ? "" : "s"}
          <span class="hint">— superseded, kept so images built from them still work</span>
        </label>
        {#if filterCount || browse.q || browse.kind || browse.fitsOnly}
          <button type="button" class="ghost small clr" onclick={clearFilters}>Clear filters</button>
        {/if}
      </div>
    </div>
  {/if}

  <nav class="seg" aria-label="Kind of model">
    <button type="button" class:on={!browse.kind} aria-pressed={!browse.kind} onclick={() => (browse.kind = "")}>All <b>{facet.length}</b></button>
    {#each kinds as k (k.id)}
      <button type="button" class:on={browse.kind === k.id} aria-pressed={browse.kind === k.id} title={tip("mod:" + k.id)}
        onclick={() => (browse.kind = browse.kind === k.id ? "" : k.id)}>{k.label} <b>{k.n}</b></button>
    {/each}
  </nav>

  {#if allPicks.length || plan.items.length}
    <QuickPicks picks={allPicks} isOn={pickOn} onpick={applyPick} onremove={forgetPick} onsave={saveCurrent} canSave={plan.items.length > 0} />
  {/if}

  {#if allDown}
    <div class="callout er">
      <Icon name="errc" size={18} />
      <span>
        <b>Neither Docker nor Podman is answering, so nothing can build right now.</b>
        <p>Start one — Docker Desktop, or the Podman machine below — then restart the factory so it reconnects.</p>
        <CodeLine cmd="podman machine start" />
      </span>
    </div>
  {/if}

  {#if loading && !$catalog.length}
    <div class="state"><span class="spin"></span>Loading the catalog…</div>
  {:else if loadErr && !$catalog.length}
    <div class="callout er">
      <Icon name="errc" size={18} />
      <span>
        <b>Couldn't load the catalog: {loadErr}.</b>
        <p>Check that the factory is running, then try again.</p>
      </span>
    </div>
    <div><button type="button" onclick={loadCatalog}><Icon name="retry" size={15} />Try again</button></div>
  {:else if !$catalog.length}
    <p class="empty">The catalog is empty. The factory reads it from its catalog file when it starts.</p>
  {:else if !shown.length}
    <div class="state none">
      <p>No models match these filters.</p>
      <button type="button" onclick={clearFilters}>Clear filters</button>
    </div>
  {:else}
    <section class="grid" aria-label="Models">
      {#each shown as m (m.id)}
        <ModelCard
          {m}
          fit={fits.get(m.id) ?? fitFor(m, $sysinfo)}
          lib={libFor(lib, m.id)}
          selected={inPlan(m.id)}
          ontoggle={() => togglePlan(m, $sysinfo)}
          onopen={() => openDetails(m)} />
      {/each}
    </section>
  {/if}

  {#if plan.items.length}
    <Tray
      models={planModels}
      downloadGB={trayDownload}
      eta={trayPlan.eta}
      checking={pre.loading}
      blocked={trayPlan.blocked}
      onclear={() => {
        const n = plan.items.length;
        clearPlan();
        toast(`Cleared ${n} model${n > 1 ? "s" : ""} from the plan.`);
      }} />
  {/if}
</div>

{#if openModel}
  <ModelDrawer
    m={openModel}
    fit={fitFor(openModel, $sysinfo, libFor(lib, openModel.id))}
    lib={libFor(lib, openModel.id)}
    si={$sysinfo}
    selected={inPlan(openModel.id)}
    ontoggle={() => togglePlan(openModel, $sysinfo)}
    onbuild={() => buildNow(openModel)}
    onclose={closeDetails} />
{/if}

<style>
  .cat { display: flex; flex-direction: column; gap: 14px; }
  .tools { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .search {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 1 340px;
    min-width: 200px;
    height: 36px;
    padding: 0 6px 0 12px;
    border: 1px solid var(--ln2);
    border-radius: var(--r);
    background: var(--p1);
    color: var(--fa);
  }
  .search:focus-within { border-color: var(--pr); box-shadow: 0 0 0 3px var(--prs); }
  .search input { border: 0; background: none; padding: 0; height: 34px; box-shadow: none; font-size: 13.5px; }
  .search input:focus { box-shadow: none; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  .filt { height: 36px; min-height: 36px; background: var(--p1); border-color: var(--ln2); color: var(--mu); }
  .filt.on { color: var(--ink); }
  .filt .n {
    font: 600 11px var(--font-mono);
    background: var(--prs);
    color: var(--pr);
    border-radius: 99px;
    padding: 1px 7px;
  }
  .toggle { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--mu); white-space: nowrap; cursor: pointer; }
  .tools > .toggle { margin-left: auto; }
  .sel-sort {
    position: relative;
    display: flex;
    align-items: center;
    gap: 6px;
    height: 36px;
    padding: 0 10px 0 12px;
    border: 1px solid var(--ln);
    border-radius: var(--r);
    font-size: 13px;
    color: var(--mu);
    white-space: nowrap;
  }
  .sel-sort:focus-within { border-color: var(--pr); box-shadow: 0 0 0 3px var(--prs); }
  .sel-sort select {
    appearance: none;
    -webkit-appearance: none;
    width: auto;
    border: 0;
    background: none;
    padding: 0 18px 0 0;
    margin-right: -18px;
    color: var(--ink);
    font-weight: 500;
    font-size: 13px;
    cursor: pointer;
    box-shadow: none;
  }
  .sel-sort select:focus { box-shadow: none; }
  .sel-sort :global(.ico) { pointer-events: none; }
  .sel-sort option { background: var(--p2); color: var(--ink); }

  .filters {
    display: flex;
    flex-direction: column;
    gap: 10px;
    padding: 14px;
    border: 1px solid var(--ln);
    border-radius: var(--rl);
    background: var(--p1);
  }
  .frow { display: flex; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
  .fl { width: 76px; flex: none; font-size: 12.5px; color: var(--fa); padding-top: 4px; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; flex: 1; min-width: 0; }
  .fchip {
    min-height: 0;
    height: 26px;
    padding: 0 10px;
    border-radius: 99px;
    font-size: 12px;
    background: var(--p2);
    border-color: var(--ln);
    color: var(--mu);
  }
  .fchip.on { background: var(--prs); border-color: color-mix(in srgb, var(--pr) 55%, transparent); color: var(--ink); }
  .frow .toggle { padding-top: 3px; }
  .frow .hint { font-size: 12.5px; color: var(--fa); }
  .clr { margin-left: auto; }

  .seg {
    display: flex;
    gap: 2px;
    padding: 4px;
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--r);
    align-self: flex-start;
    max-width: 100%;
    overflow-x: auto;
    scrollbar-width: none;
  }
  .seg::-webkit-scrollbar { display: none; }
  .seg button {
    min-height: 0;
    height: 28px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--rs);
    background: none;
    color: var(--mu);
    font-size: 13px;
    font-weight: 500;
    gap: 6px;
    flex: none;
  }
  .seg button:hover { background: var(--p2); color: var(--ink); }
  .seg button b { font: 500 11px var(--font-mono); color: var(--fa); }
  .seg button.on { background: var(--p3); color: var(--ink); }

  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 12px; }
  .state { display: flex; align-items: center; gap: 10px; color: var(--mu); padding: 24px 4px; }
  .state.none { flex-direction: column; align-items: flex-start; }

  @media (max-width: 760px) {
    .cat { padding-top: 4px; }
    .search { flex: 1 1 100%; }
    .tools > .toggle { margin-left: 0; }
    .sel-sort { margin-left: auto; }
    .grid { grid-template-columns: minmax(0, 1fr); }
    .fl { width: 100%; padding-top: 0; }
  }
</style>
