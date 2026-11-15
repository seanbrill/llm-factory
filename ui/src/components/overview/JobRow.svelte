<script lang="ts">
  // One build in an Overview lane (sheet frame f1): the running one with its
  // phase and progress, a queued one with its place in line.
  import Icon from "../Icon.svelte";
  import { catalog, sysinfo } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { clock, computeChip, computeName, dur, engineName } from "../../lib/format";
  import { compiledOn, phase, progress, routeText, timeText } from "../../lib/jobs";
  import { predictRoute } from "../../lib/plan.svelte";
  import { modMeta } from "../../lib/icons";
  import { kindText, placeText, shortRef } from "../../lib/overview";
  import type { BuildJob } from "../../lib/types";

  let { j, lane }: { j: BuildJob; lane: BuildJob[] } = $props();

  const m = $derived($catalog.find((x) => x.id === j.model_id));
  const running = $derived(j.status === "running");
  const p = $derived(running ? progress(j) : null);
  const ens = $derived(j.kind === "ensemble");

  // Where it compiles: read from the log once running, predicted while queued.
  const route = $derived.by((): { text: string; cls: string; title?: string } => {
    if (running) return { text: routeText(compiledOn(j), j.engine), cls: "chip" };
    if (!m) return { text: engineName(j.engine), cls: "chip" };
    const pr = predictRoute(m, { engine: j.engine, compute: j.compute }, $sysinfo);
    if (pr.blocked) return { text: "Can't build now", cls: "chip wa", title: pr.blocked };
    return { text: routeText(pr.buildEngine, j.engine), cls: "chip" };
  });
</script>

<div class="job" class:run={running}>
  <span class="ico"><Icon name={ens ? "ensembles" : modMeta(m?.modality).icon} size={17} /></span>
  <div class="mid">
    <a class="nm" href={href("builds", j.id)}>{j.name}</a>
    <div class="meta">
      {#if running}<span class="mono ref">{shortRef(j.ref)}</span>{/if}
      <span class={computeChip(j.compute)}>{computeName(j.compute)}</span>
      <span class={route.cls} title={route.title}>{route.text}</span>
      {#if !running}
        <span class="sub">{ens ? "ensemble" : kindText(m?.modality)}{#if j.eta_seconds > 0}{` · about ${dur(j.eta_seconds)} to build`}{/if}</span>
      {/if}
    </div>
  </div>
  <div class="right">
    {#if running}
      {clock(j.elapsed_ms)} elapsed
    {:else}
      {placeText(j, lane)}
      {#if j.wait_seconds > 0}<small>starts in about {dur(j.wait_seconds)}</small>{/if}
    {/if}
  </div>
  {#if running}
    <div class="prog">
      <div class="row">
        <span><b>{phase(j)}</b>{#if p != null}{` · ${Math.round(p * 100)}%`}{/if}</span>
        {#if j.eta_seconds > 0}<span>{timeText(j)}</span>{/if}
      </div>
      <div
        class="bar"
        class:busy={p == null}
        role="progressbar"
        aria-label="{j.name} build progress"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={p == null ? undefined : Math.round(p * 100)}>
        <i style="width:{((p ?? 0.3) * 100).toFixed(1)}%"></i>
      </div>
    </div>
  {/if}
</div>

<style>
  .job {
    display: grid;
    grid-template-columns: 34px minmax(0, 1fr) auto;
    gap: 6px 12px;
    align-items: center;
    padding: 12px 0;
    border-top: 1px solid var(--ln);
  }
  .ico {
    width: 34px;
    height: 34px;
    border-radius: 9px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
  }
  .run .ico { color: var(--pr); }
  .mid { min-width: 0; }
  .nm { font-weight: 600; color: var(--ink); }
  .meta { display: flex; gap: 6px; align-items: center; margin-top: 3px; flex-wrap: wrap; min-width: 0; }
  .ref { color: var(--mu); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .right {
    text-align: right;
    font-size: 12.5px;
    color: var(--mu);
    font-variant-numeric: tabular-nums;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
  }
  .right small { font-size: 11.5px; color: var(--fa); }
  .prog { grid-column: 2 / 4; display: flex; flex-direction: column; gap: 6px; }
  .prog .row { display: flex; justify-content: space-between; gap: 10px; font-size: 12.5px; color: var(--mu); }
  .prog .row b { color: var(--ink); font-weight: 600; }
  @media (max-width: 1100px) {
    .prog { grid-column: 1 / 4; }
  }
</style>
