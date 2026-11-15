<script lang="ts">
  // Library (sheet frame f11). Images, containers and model files used to be
  // three pages; here each model is one row with everything it has on this
  // machine and the next thing to do with it, and Cleanup finds the space that
  // nothing uses. "#library/<image ref or model id>" opens that row.
  import { tick, untrack } from "svelte";
  import Icon from "../components/Icon.svelte";
  import LibraryRow from "../components/library/LibraryRow.svelte";
  import Cleanup from "../components/library/Cleanup.svelte";
  import Budget from "../components/library/Budget.svelte";
  import RunDialog from "../components/library/RunDialog.svelte";
  import LogsDialog from "../components/library/LogsDialog.svelte";
  import ConfigDialog from "../components/library/ConfigDialog.svelte";
  import ConfirmDialog from "../components/library/ConfirmDialog.svelte";
  import CodeLine from "../components/CodeLine.svelte";
  import { live, watch, refresh } from "../lib/live.svelte";
  import { build } from "../lib/build.svelte";
  import { catalog, ensembles, sysinfo, resources, buildTemplate, toast } from "../lib/stores";
  import { routeArg, go, href } from "../lib/router";
  import { post, labelVal } from "../lib/api";
  import { addToPlan } from "../lib/plan.svelte";
  import { gb, engineName } from "../lib/format";
  import {
    buildLibrary, candidates, findRow, sharedWeights, reclaimable,
    type LibRow, type LibImage, type LibContainer, type RowState, type Candidate, type ConfirmSpec, type RowActions,
  } from "../lib/library";

  $effect(() => watch("containers", "images", "files", "storage", "resources", "harness"));

  const lib = $derived(
    buildLibrary({
      images: live.images,
      containers: live.containers,
      files: live.files,
      catalog: $catalog,
      storage: live.storage,
      jobs: build.jobs,
      ensembleName: (id) => $ensembles.find((e) => e.id === id)?.name ?? "",
    }),
  );
  const cands = $derived(candidates(lib, live.storage, $catalog));
  const loaded = $derived(live.loaded.images && live.loaded.containers && live.loaded.files);
  const listError = $derived(live.errors.images && live.errors.containers ? live.errors.images : "");
  const downEngines = $derived(($sysinfo?.engines ?? []).filter((e) => !e.reachable));

  // ---- filters ----
  type Show = "all" | RowState;
  const SEGS: { id: Show; label: string }[] = [
    { id: "all", label: "All" },
    { id: "running", label: "Running" },
    { id: "ready", label: "Ready to run" },
    { id: "tar", label: "Saved as .tar" },
    { id: "building", label: "Building" },
    { id: "weights", label: "Weights only" },
  ];
  let show = $state<Show>("all");
  let q = $state("");
  const count = (s: Show) => (s === "all" ? lib.rows.length : lib.rows.filter((r) => r.states.has(s)).length);
  const segs = $derived(SEGS.filter((s) => s.id === "all" || s.id === show || count(s.id) > 0));

  function matches(r: LibRow, text: string): boolean {
    const t = text.trim().toLowerCase();
    if (!t) return true;
    const hay = [r.name, r.modelId, r.modality, ...r.images.map((i) => i.ref), ...r.containers.map((c) => `${c.name} :${c.port}`), ...r.tars.map((x) => x.name)]
      .join(" ")
      .toLowerCase();
    return t.split(/\s+/).every((w) => hay.includes(w));
  }
  const rows = $derived(lib.rows.filter((r) => (show === "all" || r.states.has(show)) && matches(r, q)));

  // ---- disk ----
  const imagesGB = $derived([...lib.rows.flatMap((r) => r.images), ...lib.untagged].reduce((s, i) => s + (i.sizeGB ?? 0), 0));
  const onDisk = $derived(
    live.storage
      ? live.storage.weights.gb + live.storage.tars.gb + live.storage.leftovers.gb + live.storage.media.gb + imagesGB
      : lib.rows.reduce((s, r) => s + r.disk.total, 0),
  );
  const diskTitle = $derived(
    live.storage
      ? `Weights ${gb(live.storage.weights.gb)} · images ${gb(imagesGB)} · saved .tar ${gb(live.storage.tars.gb)} · leftovers ${gb(live.storage.leftovers.gb)} · media ${gb(live.storage.media.gb)}`
      : "",
  );
  const diskText = $derived(
    `${gb(onDisk)} on disk` + (live.storage?.disk_free_gb != null ? ` · ${gb(live.storage.disk_free_gb)} free` : ""),
  );
  const reclaim = $derived(reclaimable(live.storage, cands));
  let cleanupCard: ReturnType<typeof Cleanup> | undefined = $state();

  // ---- per-row helpers ----
  function est(c: LibContainer): { gb: number; gpu: boolean } | null {
    const run = $resources?.running ?? [];
    const mid = labelVal(c.info.Labels, "local-llm.model");
    const r = run.find((x) => x.name === c.name) ?? run.find((x) => !!mid && x.model_id === mid);
    if (!r) return null;
    const gpu = r.compute === "vulkan" || r.compute === "cuda";
    const v = gpu ? r.vram_gb : r.ram_gb;
    return v > 0 ? { gb: v, gpu } : null;
  }
  const gatewayPorts = $derived(new Set((live.harness?.gateway?.models ?? []).map((m) => m.port)));
  const owui = $derived(live.harness?.openwebui?.url ?? "");

  // ---- expand / deep link ----
  let expanded = $state<Record<string, boolean>>({});
  let flash = $state("");
  let flashTimer: ReturnType<typeof setTimeout> | undefined;
  let handled = "";
  let missing = $state("");

  $effect(() => {
    const arg = $routeArg;
    if (!arg) {
      handled = "";
      missing = "";
      return;
    }
    if (!loaded || arg === handled) return;
    const r = findRow(lib.rows, arg);
    untrack(() => {
      if (!r) {
        missing = arg;
        return;
      }
      handled = arg;
      missing = "";
      expanded[r.key] = true;
      show = "all";
      q = "";
      flash = r.key;
      clearTimeout(flashTimer);
      flashTimer = setTimeout(() => (flash = ""), 2600);
      const id = "row-" + r.key.replace(/[^a-z0-9-]/gi, "_");
      tick().then(() => document.getElementById(id)?.scrollIntoView({ block: "center", behavior: "smooth" }));
    });
  });
  $effect(() => () => clearTimeout(flashTimer));

  // ---- dialogs ----
  let runOpen = $state(false);
  let runImage = $state<LibImage | null>(null);
  let runName = $state("");
  let logsOpen = $state(false);
  let logsFor = $state<LibContainer | null>(null);
  let logsName = $state("");
  let cfgOpen = $state(false);
  let cfgImage = $state<LibImage | null>(null);
  let confirmOpen = $state(false);
  let confirmSpec = $state<ConfirmSpec | null>(null);

  function ask(spec: ConfirmSpec) {
    confirmSpec = spec;
    confirmOpen = true;
  }

  function useTemplate(im: LibImage) {
    buildTemplate.set({ ref: im.ref, engine: im.engine });
    toast(`Loaded the settings of ${im.ref} into the build plan.`);
    go("plan");
  }

  function deleteWeights(r: LibRow) {
    const m = r.model;
    if (!m) return;
    const shared = sharedWeights(m, $catalog).filter((x) => live.files.find((f) => f.id === x.id)?.downloaded);
    const size = r.weightsFile?.gb ?? r.weightsGB;
    ask({
      title: `Delete the weights for ${r.name}?`,
      body:
        `Removes models/${m.file}. ` +
        (r.images.length ? "The image keeps its own copy, so it still runs. " : "") +
        `A future build downloads them again${m.size_gb ? ` (${gb(m.size_gb)})` : ""}.`,
      frees: size > 0 ? `Frees ${gb(size)}.` : undefined,
      note: shared.length ? `${shared.map((x) => x.name).join(" and ")} use${shared.length === 1 ? "s" : ""} the same file, so ${shared.length === 1 ? "its" : "their"} weights go too.` : undefined,
      confirm: "Delete weights",
      danger: true,
      run: async () => {
        await post("/api/model/delete", { model_id: m.id });
        toast(`Deleted the weights for ${r.name}.`);
        await refresh("files", "storage");
      },
    });
  }

  /** r = null for an untagged image (no row of its own). */
  function deleteImage(r: LibRow | null, im: LibImage) {
    const total = (im.sizeGB ?? 0) + (im.tar?.gb ?? 0);
    const lastCopy = !!r && r.images.length === 1 && !r.weightsGB && !r.tars.length;
    ask({
      title: r ? `Delete ${im.ref}?` : `Delete untagged image ${im.ref}?`,
      body: r
        ? `Removes the image from ${engineName(im.engine)}${im.tar ? ` and its saved copy, images/${im.tar.name}` : ""}.`
        : `It's left in ${engineName(im.engine)} by a rebuild; no tag points at it.`,
      frees: total > 0 ? `Frees about ${gb(total)}.` : undefined,
      note:
        r && lastCopy
          ? `It's the only copy of ${r.name} here: running it again means building it${r.model?.size_gb ? ` and downloading ${gb(r.model.size_gb)} of weights` : ""}.`
          : undefined,
      confirm: "Delete image",
      danger: true,
      run: async () => {
        await post("/api/image/delete", { ref: im.ref, engine: im.engine });
        toast(`Deleted ${im.ref}.`);
        await refresh("images", "storage");
      },
    });
  }

  const act: RowActions = {
    run: (r, im) => {
      runImage = im;
      runName = r.name;
      runOpen = true;
    },
    stop: (r, c) =>
      ask(
        c.running
          ? {
              title: `Stop ${r.name}?`,
              body: `Stops and removes its container${c.port ? ` on :${c.port}` : ""}. The image stays, so you can run it again.`,
              confirm: "Stop",
              danger: true,
              run: async () => {
                await post("/api/stop", { id: c.id || c.name, engine: c.engine });
                toast(`Stopped ${r.name}.`);
                await refresh("containers", "resources");
              },
            }
          : {
              title: "Remove the stopped container?",
              body: `${c.name} has stopped (${c.status}). Removing it doesn't touch the image; read its log first if you want to know why it stopped.`,
              confirm: "Remove",
              danger: true,
              run: async () => {
                await post("/api/stop", { id: c.id || c.name, engine: c.engine });
                toast(`Removed ${c.name}.`);
                await refresh("containers");
              },
            },
      ),
    logs: (r, c) => {
      logsFor = c;
      logsName = r.name;
      logsOpen = true;
    },
    config: (im) => {
      cfgImage = im;
      cfgOpen = true;
    },
    template: useTemplate,
    download: (im) => {
      const a = document.createElement("a");
      a.href = `/api/image/download?ref=${encodeURIComponent(im.ref)}&engine=${encodeURIComponent(im.engine)}`;
      a.download = "";
      document.body.appendChild(a);
      a.click();
      a.remove();
      toast(`Saving ${im.ref} as a .tar. ${engineName(im.engine)} packs it first, so the download can take a minute to start.`, 6000);
    },
    deleteImage: (r, im) => deleteImage(r, im),
    deleteWeights,
    build: (r) => {
      if (!r.model) return;
      addToPlan(r.model, $sysinfo);
      toast(`${r.name} is in the build plan. Check it, then queue it.`);
      go("plan");
    },
  };

  function onCandidate(c: Candidate) {
    if (!c.action) return;
    if (c.action.kind === "weights") {
      const id = c.action.modelId;
      const r = lib.rows.find((x) => x.modelId === id);
      if (r) deleteWeights(r);
    } else {
      const { ref, engine } = c.action;
      const im = lib.untagged.find((i) => i.ref === ref && i.engine === engine);
      if (im) deleteImage(null, im);
    }
  }

  function toggle(key: string) {
    expanded[key] = !expanded[key];
  }
</script>

<div class="lib">
  <div class="tools">
    <div class="seg" role="group" aria-label="Show">
      {#each segs as s (s.id)}
        <button type="button" class:on={show === s.id} aria-pressed={show === s.id} onclick={() => (show = s.id)}>
          {s.label} <b>{loaded ? count(s.id) : "—"}</b>
        </button>
      {/each}
    </div>
    <label class="filter">
      <Icon name="find" size={15} />
      <input type="search" bind:value={q} placeholder="Filter models" aria-label="Filter models" />
    </label>
    <div class="sp">
      {#if loaded}
        <span class="sub" title={diskTitle}>{diskText}</span>
      {/if}
      {#if reclaim > 0.05}
        <button type="button" onclick={() => cleanupCard?.reveal()} title="See what nothing is using, and remove it">
          <Icon name="trash" size={15} />Clean up {gb(reclaim)}
        </button>
      {/if}
    </div>
  </div>

  {#each downEngines as e (e.engine)}
    <div class="callout wa">
      <Icon name="warn" />
      <span>
        <b>{engineName(e.engine)} isn't running, so its images and containers aren't listed.</b>
        <p>{e.engine === "podman" ? "Start the Podman machine, then restart the factory so it reconnects." : "Start Docker Desktop; the Library picks it up within a minute."}</p>
        {#if e.engine === "podman"}<CodeLine cmd="podman machine start" />{/if}
      </span>
    </div>
  {/each}

  {#if missing}
    <div class="callout">
      <Icon name="info" />
      <span><b>Nothing here matches “{missing}”.</b><p>It may have been deleted, or it hasn't been built on this machine. <a href={href("catalog")}>Find it in the Catalog</a>.</p></span>
    </div>
  {/if}

  <section class="tb" aria-label="Models on this machine">
    <div class="tr h" aria-hidden="true">
      <span>Model</span><span class="hi">Image</span><span class="hc">Container</span><span class="hic">Image · container</span><span>On disk</span><span class="r">Actions</span>
    </div>
    {#if listError && !lib.rows.length}
      <div class="state">
        <div class="callout er">
          <Icon name="errc" />
          <span><b>Couldn't list images or containers.</b><p>{listError}. Check that Docker or Podman is running on <a href={href("system")}>System</a>; the Library retries on its own.</p></span>
        </div>
      </div>
    {:else if !loaded}
      <div class="state"><p class="empty"><span class="spin"></span> Looking at Docker, Podman and the models folder…</p></div>
    {:else if !lib.rows.length}
      <div class="state">
        <p class="big">Nothing on this machine yet.</p>
        <p class="sub">Pick models in the Catalog and build them; each one shows up here with its image, weights and container.</p>
        <a class="btn pri" href={href("catalog")}><Icon name="catalog" size={15} />Open the Catalog</a>
      </div>
    {:else if !rows.length}
      <div class="state">
        <p class="sub">No models match{q ? ` “${q}”` : ""}{show !== "all" ? ` in ${SEGS.find((s) => s.id === show)?.label}` : ""}.</p>
        <button type="button" onclick={() => { q = ""; show = "all"; }}>Show all</button>
      </div>
    {:else}
      {#each rows as r (r.key)}
        <LibraryRow
          row={r}
          expanded={!!expanded[r.key]}
          flash={flash === r.key}
          {est}
          {owui}
          {gatewayPorts}
          ontoggle={() => toggle(r.key)}
          {act} />
      {/each}
    {/if}
  </section>

  <div class="foot">
    <Cleanup bind:this={cleanupCard} storage={live.storage} loaded={live.loaded.storage} error={live.errors.storage} {cands} onaction={onCandidate} />
    <Budget />
  </div>
</div>

<RunDialog bind:open={runOpen} image={runImage} name={runName} />
<LogsDialog bind:open={logsOpen} container={logsFor} name={logsName} />
<ConfigDialog bind:open={cfgOpen} image={cfgImage} ontemplate={useTemplate} />
<ConfirmDialog bind:open={confirmOpen} spec={confirmSpec} />

<style>
  .lib {
    --cols: minmax(0, 1.3fr) minmax(0, 1.2fr) minmax(0, 1.05fr) 168px 232px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .tools { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  .seg {
    display: flex;
    gap: 2px;
    padding: 4px;
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--r);
    overflow-x: auto;
    max-width: 100%;
  }
  .seg button {
    min-height: 0;
    height: 28px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--rs);
    background: none;
    font-size: 13px;
    color: var(--mu);
    gap: 6px;
  }
  .seg button:hover { background: var(--p2); color: var(--ink); }
  .seg button b { font-family: var(--font-mono); font-size: 11px; color: var(--fa); font-weight: 500; }
  .seg button.on { background: var(--p3); color: var(--ink); }
  .filter {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 38px;
    padding: 0 12px;
    width: 220px;
    border: 1px solid var(--ln2);
    border-radius: var(--r);
    background: var(--p1);
    color: var(--fa);
  }
  .filter:focus-within { border-color: var(--pr); box-shadow: 0 0 0 3px var(--prs); }
  .filter input { border: 0; padding: 0; background: none; box-shadow: none; height: 100%; }
  .filter input:focus { box-shadow: none; }
  .sp { margin-left: auto; display: flex; gap: 10px; align-items: center; }
  .sp .sub { white-space: nowrap; }

  .tb { background: var(--p1); border: 1px solid var(--ln); border-radius: var(--rl); overflow: hidden; }
  .tr.h {
    display: grid;
    grid-template-columns: var(--cols);
    gap: 16px;
    padding: 10px 18px;
    background: var(--p2);
    font-size: 11px;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--fa);
    font-weight: 600;
  }
  .tr.h .r { text-align: right; }
  .hic { display: none; }
  .state { padding: 26px 18px; display: flex; flex-direction: column; align-items: flex-start; gap: 10px; }
  .state .big { font-family: var(--font-display); font-size: 17px; font-weight: 700; }
  .empty { display: flex; align-items: center; gap: 8px; padding: 0; }
  .foot { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 14px; align-items: start; }

  @media (max-width: 1240px) {
    .lib { --cols: minmax(0, 1.15fr) minmax(0, 1.5fr) 150px 232px; }
    .hi, .hc { display: none; }
    .hic { display: block; }
  }
  @media (max-width: 1100px) {
    .foot { grid-template-columns: 1fr; }
    .filter { width: 180px; }
  }
  @media (max-width: 760px) {
    .lib { padding-top: 2px; }
    .tools { gap: 10px; }
    .seg { order: 1; width: 100%; }
    .filter { order: 2; flex: 1; width: auto; }
    .sp { order: 3; margin-left: 0; width: 100%; justify-content: space-between; }
    .tr.h { display: none; }
    .tb > :global(.row:first-of-type) { border-top: 0; }
  }
</style>
