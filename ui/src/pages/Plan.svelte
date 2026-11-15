<script lang="ts">
  // Build plan (sheet frame f9): the models picked in the Catalog, in queue
  // order, each with its compute, where it compiles and runs and how long it
  // should take; the server's preflight (disk, engines, downloads, the queue)
  // with a fix for anything that would stop a build; then one click queues them
  // all. #plan/<model id> opens that build's settings. Cloning an image from the
  // Library lands here too ($buildTemplate).
  import { onMount, tick, untrack } from "svelte";
  import Icon from "../components/Icon.svelte";
  import PlanRow from "../components/plan/PlanRow.svelte";
  import Lanes from "../components/plan/Lanes.svelte";
  import Checks from "../components/plan/Checks.svelte";
  import Finish from "../components/plan/Finish.svelte";
  import { api } from "../lib/api";
  import { catalog, sysinfo, personas, buildTemplate, toast, loadSysInfo } from "../lib/stores";
  import { go, href, routeArg } from "../lib/router";
  import { live, watch } from "../lib/live.svelte";
  import { build } from "../lib/build.svelte";
  import { openPalette } from "../lib/ui.svelte";
  import { computeName, engineName, plural } from "../lib/format";
  import {
    cpuEngineId, engineOf, family, gpuEngineFor, isGPU, libFor, libraryIndex, schedule, shortName, type ScheduleItem,
  } from "../lib/catalog";
  import {
    plan, pre, checkPlan, planKey, preflightFor, queueItems, removeFromPlan, movePlanItem, clearPlan,
    updatePlanItem, cloneImage, effectiveCompute, type PlanItem,
  } from "../lib/plan.svelte";
  import { harnessProblem } from "../lib/baked";
  import type { Model } from "../lib/types";

  $effect(() => watch("images", "containers", "files"));

  // ---- loading ------------------------------------------------------------------
  let catLoading = $state(false);
  let catErr = $state("");
  async function loadCat() {
    catLoading = true;
    catErr = "";
    try {
      catalog.set((await api<Model[]>("/api/catalog")) ?? []);
    } catch (e) {
      catErr = (e as Error).message || "the factory didn't answer";
    } finally {
      catLoading = false;
    }
  }
  onMount(() => {
    if (!$catalog.length) loadCat();
    if (!$sysinfo) loadSysInfo();
    // Back from a terminal (Podman given more memory, Docker started): check again.
    const onFocus = () => plan.items.length && checkPlan(0);
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  });

  // Ask the server again whenever a build's model, engine or compute changes,
  // or the queue grows or shrinks.
  const activeJobs = $derived(build.jobs.filter((j) => j.active).length);
  $effect(() => {
    planKey(plan.items);
    activeJobs;
    checkPlan(250);
  });

  // ---- cloning an image (Library → "use as template") --------------------------------
  let cloning = $state(false);
  $effect(() => {
    const t = $buildTemplate;
    const cat = $catalog;
    if (!t || !cat.length) return;
    buildTemplate.set(null);
    cloning = true;
    cloneImage(t, cat, untrack(() => $sysinfo))
      .then(async (id) => {
        openRows = [...new Set([...openRows, id])];
        toast(`Copied the settings of ${t.ref} into the plan. Change what you need, then queue it.`, 5000);
        await tick();
        document.getElementById("plan-" + id)?.scrollIntoView({ block: "center" });
      })
      .catch((e) => toast(`Couldn't copy the settings of ${t.ref}: ${(e as Error).message}`, 8000))
      .finally(() => (cloning = false));
  });

  // ---- rows -------------------------------------------------------------------------
  const byId = $derived(new Map($catalog.map((m) => [m.id, m])));
  const lib = $derived(libraryIndex(live.images, live.containers, live.files, build.jobs));
  const rows = $derived(plan.items.map((it) => ({ it, m: byId.get(it.model_id), pf: preflightFor(it) })));

  // A later build in the same lane that compiles the same runtime, on the same
  // engine, for the same compute reuses the first one's compile.
  const reuse = $derived.by(() => {
    const out = new Set<string>();
    rows.forEach((r, i) => {
      const p = r.pf;
      if (!p?.compiles || p.error) return;
      const f = family(r.m);
      const earlier = rows.slice(0, i).some((o) =>
        o.pf?.compiles && !o.pf.error && o.it.engine === r.it.engine && o.pf.build_engine === p.build_engine &&
        o.pf.compute === p.compute && family(o.m) === f);
      if (earlier) out.add(r.it.model_id);
    });
    return out;
  });

  let openRows = $state<string[]>([]);
  const toggleRow = (id: string) =>
    (openRows = openRows.includes(id) ? openRows.filter((x) => x !== id) : [...openRows, id]);
  // #plan/<model id> opens that build's settings.
  $effect(() => {
    const a = $routeArg;
    if (!a) return;
    untrack(() => {
      if (!openRows.includes(a)) openRows = [...openRows, a];
    });
    tick().then(() => document.getElementById("plan-" + a)?.scrollIntoView({ block: "center" }));
  });

  function setCompute(it: PlanItem, c: string) {
    // Picking a compute picks its route: a GPU runs on an engine that has that
    // GPU — the one the build is already set to when it has it (Docker and
    // Podman can both), else the one with more memory; the CPU on the engine
    // with the most room (Docker on a Mac).
    let engine = it.engine;
    if (isGPU(c)) {
      if (engineOf($sysinfo, it.engine)?.gpu !== c) engine = gpuEngineFor($sysinfo, c)?.engine ?? engine;
    } else if (isGPU(it.compute)) engine = cpuEngineId($sysinfo);
    updatePlanItem(it.model_id, { compute: c, engine });
  }

  function switchEngine(from: string, to: string) {
    const gpuThere = engineOf($sysinfo, to)?.gpu ?? "";
    let n = 0;
    for (const it of plan.items) {
      if (it.engine !== from) continue;
      const m = byId.get(it.model_id);
      const patch: Partial<PlanItem> = { engine: to };
      // A GPU build can't follow to an engine without that GPU: build for the CPU there.
      if (isGPU(it.compute) && it.compute !== gpuThere && (!m?.computes?.length || m.computes.includes("cpu"))) patch.compute = "cpu";
      updatePlanItem(it.model_id, patch);
      n++;
    }
    toast(`${plural(n, "build")} now ${n === 1 ? "builds" : "build"} for ${engineName(to)}.`);
  }

  function remove(id: string) {
    const m = byId.get(id);
    removeFromPlan(id);
    openRows = openRows.filter((x) => x !== id);
    delete failures[id];
    toast(`Removed ${m ? m.name : id} from the plan.`);
  }

  // Clearing keeps what was there until the next change, so it can be put back.
  let cleared = $state<PlanItem[]>([]);
  function clearAll() {
    cleared = $state.snapshot(plan.items) as PlanItem[];
    clearPlan();
    openRows = [];
    failures = {};
  }
  function putBack() {
    plan.items = cleared;
    cleared = [];
  }

  // ---- drag to reorder (the grip's arrow keys and the settings' Move buttons do the same) ----
  let dragId = $state("");
  let dropAt = $state(-1);
  const dragFrom = $derived(plan.items.findIndex((i) => i.model_id === dragId));
  function dragStart(e: DragEvent, id: string) {
    dragId = id;
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", id);
    }
  }
  function dragOver(e: DragEvent, i: number) {
    if (!dragId) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
    dropAt = i;
  }
  function dropOn(e: DragEvent, i: number) {
    e.preventDefault();
    if (dragId && i !== dragFrom) movePlanItem(dragId, i);
    dragEnd();
  }
  function dragEnd() {
    dragId = "";
    dropAt = -1;
  }
  const dropMark = (i: number): "" | "above" | "below" =>
    !dragId || i !== dropAt || i === dragFrom ? "" : i < dragFrom ? "above" : "below";

  // ---- the lanes -----------------------------------------------------------------------
  const sched = $derived(
    schedule(
      rows
        .filter((r) => !r.pf?.error)
        .map((r): ScheduleItem => ({
          key: r.it.model_id,
          label: r.m ? shortName(r.m) : r.it.model_id,
          engine: r.it.engine,
          compute: r.pf?.compute ?? (r.m ? effectiveCompute(r.m, r.it.compute) : r.it.compute),
          eta: r.pf?.eta_seconds ?? 0,
          transfer: r.pf?.transfer ? r.pf.build_engine : "",
        })),
      build.jobs,
    ),
  );
  const unknownTimes = $derived([
    ...new Set(rows.filter((r) => r.pf && !r.pf.error && !r.pf.eta_seconds).map((r) => computeName(r.pf!.compute))),
  ]);
  const lanesUsed = $derived([...new Set(plan.items.map((i) => i.engine))]);
  const sub = $derived(
    plan.items.length === 1
      ? "Check its settings, then queue it — or add more from the Catalog to build them together."
      : `Drag to reorder. ${lanesUsed.length === 1 ? `${engineName(lanesUsed[0])} builds` : "Each engine builds"} one at a time` +
          (reuse.size ? ", so a later build can reuse an earlier one's compile." : ", in this order."),
  );

  // ---- queueing -------------------------------------------------------------------------
  let failures = $state<Record<string, string>>({});
  const ready = $derived(rows.filter((r) => r.m && !r.pf?.error && !harnessProblem(r.it.harness, r.m)));
  const held = $derived(rows.length - ready.length);
  const ahead = $derived(lanesUsed.reduce((n, e) => n + (pre.result?.queued?.[e] ?? 0), 0));
  const weights = $derived(rows.reduce((n, r) => n + (r.pf?.weights_gb ?? 0), 0));

  const goNote = $derived.by(() => {
    if (!ready.length) return "Fix the problems above first; nothing in the plan can build yet.";
    const parts = ["You can keep using the factory while they run."];
    if (ahead) parts.unshift(`They start after the ${plural(ahead, "build")} already in the queue.`);
    if (held) parts.push(`${plural(held, "build")} that can't start ${held === 1 ? "stays" : "stay"} in the plan.`);
    return parts.join(" ");
  });

  async function queue() {
    if (!ready.length || plan.submitting) return;
    failures = {};
    const items = ready.map((r) => r.it);
    const results = await queueItems(items, $catalog);
    const ok = results.filter((r) => r.ok);
    const bad = results.filter((r) => !r.ok);
    failures = Object.fromEntries(bad.map((r) => [r.model_id, r.error || "the factory refused it"]));
    if (ok.length) {
      const waits = ok.filter((r) => (r.position ?? 0) > 0).length;
      toast(
        `Queued ${plural(ok.length, "build")}.` + (waits ? ` ${waits === ok.length ? "They wait" : `${waits} wait`} behind builds already in the queue.` : ""),
        5000,
      );
    }
    if (!bad.length) go("builds");
    else toast(`${plural(bad.length, "build")} didn't queue; the plan says why.`, 6000);
  }
  const activeCount = $derived(build.jobs.filter((j) => j.active && j.kind === "model").length);
</script>

{#if !plan.items.length}
  <section class="card blank" aria-labelledby="blank-h">
    <span class="bi"><Icon name="builds" size={22} /></span>
    <h2 id="blank-h">{cloning ? "Copying the image's settings…" : "Nothing in the plan yet"}</h2>
    <p class="sub">
      Pick models in the Catalog — each card says whether it runs on this machine — or find one with ⌘K.
      They wait here so you can check them together, then queue them in one go.
    </p>
    <div class="acts">
      <a class="btn pri" href={href("catalog")}><Icon name="catalog" size={16} />Browse the catalog</a>
      <button type="button" onclick={() => openPalette("build ")}><span class="kbd">⌘K</span>Find a model</button>
    </div>
    <p class="hint">In ⌘K, <span class="kbd">↵</span> builds a model now and <span class="kbd">⌘↵</span> adds it here.</p>
    {#if cleared.length}
      <button type="button" class="ghost small" onclick={putBack}><Icon name="retry" size={14} />Put back the {plural(cleared.length, "build")} you cleared</button>
    {/if}
    {#if activeCount}
      <a href={href("builds")}>{plural(activeCount, "build")} {activeCount === 1 ? "is" : "are"} in the queue now<Icon name="arrowRight" size={14} /></a>
    {/if}
  </section>
{:else if !$catalog.length}
  <section class="card blank" aria-busy={catLoading}>
    {#if catErr}
      <div class="callout er">
        <Icon name="errc" size={16} />
        <span>
          <b>Couldn't load the catalog: {catErr}</b>
          <p>The factory may be restarting. Your plan is kept in this browser.</p>
          <span class="fixes"><button type="button" class="small" onclick={loadCat}>Try again</button></span>
        </span>
      </div>
    {:else}
      <p class="sub loading"><span class="spin" aria-hidden="true"></span>Loading the catalog…</p>
    {/if}
  </section>
{:else}
  <div class="plan">
    <section class="card main" aria-labelledby="plan-h">
      <div class="hd">
        <div class="ttl">
          <h2 id="plan-h">{plan.items.length === 1 ? "1 build" : `${plan.items.length} builds, in this order`}</h2>
          <p class="sub">{sub}</p>
        </div>
        <span class="sp">
          <button type="button" class="ghost small" onclick={clearAll}>Clear</button>
          <a class="btn sm" href={href("catalog")}><Icon name="plus" size={15} />Add models</a>
        </span>
      </div>

      <table class="ptbl">
        <thead>
          <tr>
            <th class="gc"><span class="sr">Order</span></th>
            <th>Model</th>
            <th>Compute</th>
            <th>Build <span aria-hidden="true">→</span><span class="sr">then</span> run</th>
            <th>Time</th>
            <th class="ac"><span class="sr">Settings and remove</span></th>
          </tr>
        </thead>
        <tbody>
          {#each rows as r, i (r.it.model_id)}
            <PlanRow
              it={r.it}
              m={r.m}
              index={i}
              count={rows.length}
              pf={r.pf}
              checking={pre.loading}
              si={$sysinfo}
              lib={libFor(lib, r.it.model_id)}
              personas={$personas}
              reuse={reuse.has(r.it.model_id)}
              failure={failures[r.it.model_id] ?? ""}
              expanded={openRows.includes(r.it.model_id)}
              dragging={dragId === r.it.model_id}
              drop={dropMark(i)}
              ontoggle={() => toggleRow(r.it.model_id)}
              onremove={() => remove(r.it.model_id)}
              onmove={(to) => movePlanItem(r.it.model_id, to)}
              oncompute={(c) => setCompute(r.it, c)}
              ondragstart={(e) => dragStart(e, r.it.model_id)}
              ondragover={(e) => dragOver(e, i)}
              ondrop={(e) => dropOn(e, i)}
              ondragend={dragEnd}
            />
          {/each}
        </tbody>
      </table>

      <Lanes {sched} unknown={unknownTimes} />
    </section>

    <div class="side">
      <Checks items={plan.items} {byId} si={$sysinfo} onswitch={switchEngine} />
      <Finish items={plan.items} weightsGB={weights} />
      <div class="go">
        <button type="button" class="primary big" onclick={queue} disabled={!ready.length || plan.submitting} aria-describedby="go-note">
          {#if plan.submitting}<span class="spin" aria-hidden="true"></span>Queuing…{:else}Queue {plural(ready.length, "build")}{/if}
        </button>
        <p class="note" id="go-note">{goNote}</p>
      </div>
    </div>
  </div>
{/if}

<style>
  .plan { display: grid; grid-template-columns: minmax(0, 1fr) 372px; gap: 16px; }
  .main { display: flex; flex-direction: column; gap: 22px; min-width: 0; align-self: start; }
  .main .hd { margin-bottom: -6px; align-items: flex-start; }
  .ttl { min-width: 0; }
  .ttl .sub { margin-top: 3px; }
  .hd .sp { flex: none; }
  /* Stretched to the plan's height, so the queue button can float beside all of it. */
  .side { display: flex; flex-direction: column; gap: 16px; min-width: 0; }

  .ptbl { width: 100%; border-collapse: collapse; font-size: 13px; }
  .ptbl th {
    text-align: left;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    font-weight: 500;
    padding: 0 8px 10px;
    white-space: nowrap;
  }
  .ptbl th.gc { width: 26px; padding: 0; }
  .ptbl th.ac { width: 64px; }

  /* The queue button floats at the bottom of the window until its place in
     the column scrolls into view, so it's always in reach. */
  .go {
    display: flex;
    flex-direction: column;
    gap: 8px;
    position: sticky;
    bottom: 16px;
    z-index: 5;
    padding: 10px 10px 12px;
    border: 1px solid var(--ln2);
    border-radius: 16px;
    background: var(--p2);
    box-shadow: var(--shadow);
  }
  .big { min-height: 46px; font-size: 14.5px; border-radius: 12px; }
  .big :global(.spin) { border-color: color-mix(in srgb, var(--onpr) 35%, transparent); border-top-color: var(--onpr); }
  .note { text-align: center; color: var(--mu); font-size: 12.5px; line-height: 1.45; }

  .blank { max-width: 640px; margin: 40px auto 0; display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 34px 28px; text-align: center; }
  .blank h2 { margin: 0; }
  .blank .sub { max-width: 480px; line-height: 1.5; }
  .bi { width: 46px; height: 46px; border-radius: 12px; display: grid; place-items: center; background: var(--prs); color: var(--pr); }
  .acts { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; margin-top: 6px; }
  .acts .kbd { margin-right: 2px; }
  .blank .hint { font-size: 12.5px; }
  .blank > a { display: inline-flex; align-items: center; gap: 6px; font-size: 13px; }
  .blank .callout { text-align: left; width: 100%; }
  .fixes { display: flex; gap: 6px; margin-top: 8px; }
  .loading { display: flex; align-items: center; gap: 10px; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  /* One column: the checks and options side by side under the plan, the queue
     button always in reach at the bottom. */
  @media (max-width: 1280px) {
    .plan { grid-template-columns: minmax(0, 1fr); }
    .side { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; }
    .go { grid-column: 1 / -1; }
  }
  @media (max-width: 760px) {
    .main { padding: 14px; }
    .main .hd { flex-direction: column; align-items: stretch; gap: 10px; }
    .hd .sp { justify-content: flex-end; }
    .ptbl thead { display: none; }
    .ptbl, .ptbl tbody { display: block; }
    .side { grid-template-columns: minmax(0, 1fr); }
    .go { bottom: calc(76px + env(safe-area-inset-bottom, 0px)); }
    .blank { margin-top: 8px; padding: 26px 18px; }
  }
</style>
