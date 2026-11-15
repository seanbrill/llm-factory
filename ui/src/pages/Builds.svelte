<script lang="ts">
  // Builds (sheet frames f10 desktop, f17 phone): one lane per engine with its
  // running and queued builds, the builds that finished, and the open build —
  // a failure explained with its fixes, or a build's progress, place in line
  // or image — with its log. `#builds/<job id>` opens that build.
  import { tick } from "svelte";
  import { MediaQuery } from "svelte/reactivity";
  import Icon from "../components/Icon.svelte";
  import LaneCard from "../components/builds/LaneCard.svelte";
  import HistoryCard from "../components/builds/HistoryCard.svelte";
  import JobDetail from "../components/builds/JobDetail.svelte";
  import { build, queue, watchQueue } from "../lib/build.svelte";
  import { history, lanes } from "../lib/jobs";
  import { engineLanes, needsYou } from "../lib/builds";
  import { routeArg, href } from "../lib/router";
  import { sysinfo, loadSysInfo } from "../lib/stores";

  const phone = new MediaQuery("max-width: 760px");

  // A fresh queue and engine picture on arrival (the shell polls both slowly).
  watchQueue();
  loadSysInfo();

  const laneList = $derived(engineLanes(lanes(build.jobs), $sysinfo));
  const past = $derived(history(build.jobs));

  // The open build: the one in the URL, else what needs you (a failure no
  // later build fixed), else — on a desktop, which has room — what's running,
  // next, or the newest finished one.
  const linked = $derived($routeArg ? (build.jobs.find((j) => j.id === $routeArg) ?? null) : null);
  const gone = $derived(!!$routeArg && queue.loaded && !queue.error && !linked);
  const fallback = $derived.by(() => {
    const need = needsYou(build.jobs);
    if (need || phone.current) return need;
    return (
      build.jobs.find((j) => j.status === "running") ??
      laneList.flatMap((l) => l.queued)[0] ??
      past[0] ??
      null
    );
  });
  const open = $derived(linked ?? fallback);

  let tab = $state<"queue" | "earlier">("queue");

  // A deep link scrolls its build into view once it's on the page.
  let shown = "";
  $effect(() => {
    const id = linked?.id;
    if (!id || id === shown) return;
    shown = id;
    tick().then(() => {
      const el = phone.current ? document.getElementById("open-build") : document.getElementById("job-" + id);
      el?.scrollIntoView({ block: phone.current ? "start" : "nearest", behavior: "smooth" });
    });
  });
</script>

{#if phone.current}
  <div class="phead">
    <h2>Builds</h2>
    <div class="seg" role="tablist" aria-label="Show">
      <button role="tab" id="tab-queue" aria-controls="builds-list" aria-selected={tab === "queue"} class:on={tab === "queue"} onclick={() => (tab = "queue")}>Queue</button>
      <button role="tab" id="tab-earlier" aria-controls="builds-list" aria-selected={tab === "earlier"} class:on={tab === "earlier"} onclick={() => (tab = "earlier")}>Earlier</button>
    </div>
  </div>
{/if}

{#if queue.error && !build.jobs.length}
  <div class="callout er top">
    <Icon name="errc" />
    <span>
      <b>Can't reach the factory to read the build queue.</b>
      <p>{queue.error}. It keeps trying; if the factory was restarting, this clears on its own.</p>
    </span>
  </div>
{/if}

{#if !queue.loaded}
  <div class="loading" role="status"><span class="spin"></span>Loading the build queue…</div>
{:else}
  <div class="builds" class:phone={phone.current}>
    <div
      class="left"
      id="builds-list"
      role={phone.current ? "tabpanel" : undefined}
      aria-labelledby={phone.current ? `tab-${tab}` : undefined}>
      {#if !phone.current || tab === "queue"}
        {#each laneList as lane (lane.engine)}
          <LaneCard {lane} selected={open?.id ?? ""} compact={phone.current} />
        {/each}
      {/if}
      {#if !phone.current || tab === "earlier"}
        <HistoryCard jobs={past} selected={open?.id ?? ""} compact={phone.current} />
      {/if}
    </div>

    <div class="right" id="open-build">
      {#if gone}
        <div class="callout wa">
          <Icon name="info" />
          <span>
            <b>That build isn't in the list any more.</b>
            <p>The factory keeps its last 30 finished builds, and forgets them when it restarts. <a href={href("builds")}>Show the queue</a></p>
          </span>
        </div>
      {/if}
      {#if open}
        {#key open.id}
          <JobDetail job={open} compact={phone.current} />
        {/key}
      {:else if !phone.current}
        <section class="card none">
          <span class="ni"><Icon name="builds" size={22} /></span>
          <h2>{build.jobs.length ? "Pick a build to see its log" : "Nothing has been built yet"}</h2>
          <p class="sub">
            {build.jobs.length
              ? "Open any build on the left: a failure shows why it failed and how to fix it."
              : "Pick models in the Catalog and queue them. They build here, one lane per engine, and each build keeps its log."}
          </p>
          {#if !build.jobs.length}<a class="btn pri" href={href("catalog")}><Icon name="catalog" size={15} />Open the Catalog</a>{/if}
        </section>
      {:else if !build.jobs.length}
        <p class="empty">Nothing has been built yet. <a href={href("catalog")}>Pick models in the Catalog</a> and they build here.</p>
      {/if}
    </div>
  </div>
{/if}

<style>
  .builds {
    display: grid;
    grid-template-columns: 392px minmax(0, 1fr);
    gap: 16px;
    align-items: start;
  }
  .left { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  .right { display: flex; flex-direction: column; gap: 12px; min-width: 0; position: sticky; top: 0; }
  .top { margin-bottom: 14px; }
  .loading { display: flex; align-items: center; gap: 10px; color: var(--mu); padding: 18px 4px; font-size: 13px; }

  .none {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 8px;
    padding: 28px;
  }
  .none h2 { margin: 6px 0 0; font-size: 18px; }
  .none .sub { max-width: 46ch; }
  .none .btn { margin-top: 8px; }
  .ni {
    width: 40px;
    height: 40px;
    border-radius: 12px;
    display: grid;
    place-items: center;
    background: var(--prs);
    color: var(--pr);
  }

  @media (max-width: 1240px) {
    .builds { grid-template-columns: 340px minmax(0, 1fr); }
  }
  @media (max-width: 920px) {
    .builds { grid-template-columns: minmax(0, 1fr); }
    .right { position: static; }
  }

  /* Phone (f17): the page title with Queue / Earlier, then one column. */
  .phead { display: flex; align-items: center; gap: 10px; padding: 4px 0 12px; }
  .phead h2 { font-size: 22px; margin: 0; }
  .seg {
    margin-left: auto;
    display: flex;
    padding: 3px;
    border-radius: 10px;
    background: var(--p1);
    border: 1px solid var(--ln);
  }
  .seg button {
    min-height: 0;
    height: 28px;
    padding: 0 11px;
    border: 0;
    border-radius: 7px;
    background: transparent;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--mu);
  }
  .seg button.on { background: var(--p3); color: var(--ink); }
  .builds.phone { grid-template-columns: minmax(0, 1fr); gap: 12px; }
  .builds.phone .right { position: static; scroll-margin-top: 12px; }
</style>
