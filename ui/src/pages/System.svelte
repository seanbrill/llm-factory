<script lang="ts">
  // System (sheet frame f13): which engine has the GPU and why the other
  // doesn't, how this Mac's memory is split between the two engine machines,
  // the build rules that keep builds from running out of memory, live memory
  // use with the cache drop, versions, and the FileFreak model archive.
  // #system/gpu jumps to the engines and GPU check, #system/memory to the
  // memory plan, #system/archive to the archive.
  import { onMount, tick } from "svelte";
  import EngineDetail from "../components/system/EngineDetail.svelte";
  import MemoryPlan from "../components/system/MemoryPlan.svelte";
  import BuildRules from "../components/system/BuildRules.svelte";
  import UsageCard from "../components/system/UsageCard.svelte";
  import VersionsCard from "../components/system/VersionsCard.svelte";
  import ArchiveCard from "../components/system/ArchiveCard.svelte";
  import { loadSysInfo, sysinfo } from "../lib/stores";
  import { routeArg } from "../lib/router";
  import { watch } from "../lib/live.svelte";
  import { engineList, loadEngineVersions } from "../lib/system.svelte";
  import { get } from "svelte/store";

  $effect(() => watch("resources"));

  let siState = $state<"loading" | "ok" | "error">(get(sysinfo) ? "ok" : "loading");
  onMount(() => {
    loadSysInfo().then(() => (siState = get(sysinfo) ? "ok" : "error"));
    loadEngineVersions();
  });
  function retry() {
    siState = "loading";
    loadSysInfo().then(() => (siState = get(sysinfo) ? "ok" : "error"));
    loadEngineVersions();
  }

  const engines = $derived(engineList($sysinfo));
  // The GPU check button sits on the GPU engine's card (or the first one).
  const checkOn = $derived(engines.find((e) => !!e.gpu)?.engine ?? engines[0]?.engine);

  // Deep links: scroll to the section after App has scrolled the page to the
  // top, move focus there, and highlight it for a moment.
  const TARGETS: Record<string, string> = { gpu: "sys-gpu", memory: "sys-memory", archive: "sys-archive" };
  let flash = $state("");
  $effect(() => {
    const id = TARGETS[$routeArg];
    if (!id || siState !== "ok") return;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let frame = 0;
    tick().then(() => {
      frame = requestAnimationFrame(() => {
        const el = document.getElementById(id);
        if (!el) return;
        const reduce = matchMedia("(prefers-reduced-motion: reduce)").matches;
        el.scrollIntoView({ block: "start", behavior: reduce ? "auto" : "smooth" });
        el.focus({ preventScroll: true });
        flash = id;
        timer = setTimeout(() => (flash = ""), 2200);
      });
    });
    return () => {
      cancelAnimationFrame(frame);
      clearTimeout(timer);
    };
  });
</script>

{#if siState === "loading" && !engines.length}
  <div class="card state"><span class="spin"></span>Reading the engines…</div>
{:else if !engines.length}
  <div class="card state">
    <p>Couldn't read the engines from the factory. Check that it's running, then try again.</p>
    <button class="small" onclick={retry}>Try again</button>
  </div>
{:else}
  <div class="sys">
    <div class="col">
      <section id="sys-gpu" class="engs" class:flash={flash === "sys-gpu"} tabindex="-1" aria-labelledby="sys-gpu-h">
        <h2 id="sys-gpu-h" class="vh">Engines and GPU access</h2>
        {#each engines as e (e.engine)}
          <EngineDetail {e} checkHere={e.engine === checkOn} />
        {/each}
      </section>
      <section id="sys-memory" class="card plan" class:flash={flash === "sys-memory"} tabindex="-1">
        <MemoryPlan />
      </section>
      <section id="sys-archive" class="card arc" class:flash={flash === "sys-archive"} tabindex="-1" aria-label="FileFreak archive">
        <ArchiveCard />
      </section>
    </div>
    <div class="col side">
      <section class="card rules"><BuildRules /></section>
      <section class="card"><UsageCard /></section>
      <section class="card"><VersionsCard /></section>
    </div>
  </div>
{/if}

<style>
  .sys {
    display: grid;
    grid-template-columns: minmax(0, 1.45fr) minmax(0, 1fr);
    gap: 16px;
    align-items: start;
  }
  .col { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
  .engs { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; border-radius: var(--rl); }
  .engs:has(> :global(.ecard:only-of-type)) { grid-template-columns: minmax(0, 1fr); }
  section { scroll-margin-top: 16px; }
  section:focus { outline: none; }
  section:focus-visible { outline: 2px solid var(--pr); outline-offset: 4px; }
  .flash { animation: flash 2.2s ease-out; }
  @keyframes flash {
    0%,
    55% { box-shadow: 0 0 0 2px var(--cv), 0 0 0 4px var(--gpu); }
    100% { box-shadow: 0 0 0 2px var(--cv), 0 0 0 4px transparent; }
  }
  .plan.flash,
  .arc.flash { animation-name: flash-pr; }
  @keyframes flash-pr {
    0%,
    55% { box-shadow: 0 0 0 2px var(--cv), 0 0 0 4px var(--pr); }
    100% { box-shadow: 0 0 0 2px var(--cv), 0 0 0 4px transparent; }
  }
  .vh {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  .state { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; color: var(--mu); font-size: 13px; }
  .state p { flex: 1; min-width: 220px; }
  .spin { width: 14px; height: 14px; }
  @media (max-width: 1240px) {
    .sys { grid-template-columns: minmax(0, 1fr); }
    .side { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; }
    .side .rules { grid-row: span 2; }
  }
  @media (max-width: 760px) {
    .sys,
    .col { gap: 12px; }
    .engs { grid-template-columns: minmax(0, 1fr); gap: 12px; }
    .side { display: flex; }
    section { scroll-margin-top: 72px; }
  }
  @media (prefers-reduced-motion: reduce) {
    .flash,
    .plan.flash,
    .arc.flash { animation: none; box-shadow: 0 0 0 2px var(--cv), 0 0 0 4px var(--gpu); }
  }
</style>
