<script lang="ts">
  // The open build (sheet frames f10, f17). A failure leads with one sentence
  // on what happened and why, then the ways to fix it, where it stopped, and
  // the log opened on the lines that matter. A running build shows its
  // progress and follows its log; a queued one its place in line; a built one
  // where its image is.
  import { tick, untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import LogView from "./LogView.svelte";
  import Steps from "./Steps.svelte";
  import FixList from "./FixList.svelte";
  import { build, views, openLog, closeLog, stopJob, retryJob, moveJob, type MoveTo } from "../../lib/build.svelte";
  import { explainFailure } from "../../lib/explain";
  import { lanes, progress, routeText } from "../../lib/jobs";
  import {
    STATUS, buildSteps, fixCards, jobRoute, laterBuilds, leftText, logLines, modelOf, phaseText, placeText,
    took, waitText, whenText,
  } from "../../lib/builds";
  import { catalog, sysinfo } from "../../lib/stores";
  import { go, href } from "../../lib/router";
  import { ago, clock, computeChip, computeName, dur, engineName } from "../../lib/format";
  import type { BuildJob } from "../../lib/types";

  let { job, compact = false }: { job: BuildJob; compact?: boolean } = $props();

  // Stream this build's log while it's open.
  $effect(() => {
    const id = job.id;
    untrack(() => openLog(id));
    return () => closeLog(id);
  });

  const model = $derived(modelOf(job, $catalog));
  const v = $derived(views[job.id]);
  const log = $derived(logLines(v));
  const failed = $derived(job.status === "error" || job.status === "canceled");
  const ex = $derived(failed ? explainFailure(job, v?.log ?? "") : null);
  // A lone "Build it again" needs no card: the header has that button.
  const plainRetry = $derived(
    !!ex && fixCards(job, ex, { si: $sysinfo, model }).every((c) => c.kind === "retry" && !c.desc && /^Build (it )?again$/.test(c.title)),
  );
  const steps = $derived(compact ? [] : buildSteps(job, log.lines, model, v?.pct ?? null));
  const route = $derived(jobRoute(job, model, $sysinfo));
  const later = $derived(laterBuilds(job, build.jobs));
  const again = $derived(later.find((o) => o.active) ?? null);
  const fixedBy = $derived(job.status === "done" ? null : ([...later].reverse().find((o) => o.status === "done") ?? null));
  const st = $derived(STATUS[job.status] ?? { t: job.status, c: "chip" });
  const p = $derived(progress(job));
  const name = $derived(job.name || job.ref);

  // Place in its lane's queue.
  const queued = $derived((lanes(build.jobs)[job.engine] ?? []).filter((x) => x.status === "queued"));
  const qi = $derived(queued.findIndex((x) => x.id === job.id));
  const wait = $derived(waitText(job));

  // Where the export went, from the log ("Exported 3.49 GB: images/….tar").
  const exported = $derived.by(() => {
    for (let i = log.lines.length - 1; i >= 0; i--) {
      const m = log.lines[i].match(/^Exported ([\d.]+) GB: (images\/\S+)/);
      if (m) return `Exported ${(+m[1]).toFixed(1)} GB to ${m[2]}.`;
    }
    return "";
  });

  // Short "when" for the phone header: "11 h ago · 29 min".
  const shortWhen = $derived.by(() => {
    if (job.status === "running") return clock(job.elapsed_ms);
    if (job.status === "queued") return wait || placeText(job, Math.max(0, qi));
    return job.started_at ? `${ago(job.finished_at)} · ${took(job.elapsed_ms)}` : ago(job.finished_at);
  });

  let confirming = $state(false);
  let busy = $state("");

  async function cancel() {
    busy = "cancel";
    await stopJob(job);
    busy = "";
    confirming = false;
  }

  // The question takes the focus on its safe answer; Escape or Keep building
  // hands it back to the Cancel button.
  async function ask() {
    confirming = true;
    await tick();
    document.querySelector<HTMLElement>("#open-build [data-keep]")?.focus();
  }
  async function keep() {
    confirming = false;
    await tick();
    document.querySelector<HTMLElement>("#open-build [data-cancel]")?.focus();
  }
  function onConfirmKey(e: KeyboardEvent) {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    keep();
  }

  async function retry() {
    busy = "retry";
    const r = await retryJob(job);
    busy = "";
    if (r) go("builds", r.id);
  }

  async function move(to: MoveTo) {
    busy = "move";
    await moveJob(job, to);
    busy = "";
  }

  const ICON: Record<string, string> = { done: "check", error: "xmark", canceled: "minus", queued: "clock" };
</script>

<section class="card dt" class:compact class:fail={job.status === "error"} aria-labelledby="dt-name">
  {#if compact}
    <div class="fh">
      <span class="sx {job.status}" aria-hidden="true">
        {#if job.status === "running"}<span class="spin"></span>{:else}<Icon name={ICON[job.status] ?? "info"} size={12} stroke={2.8} />{/if}
      </span>
      <h2 id="dt-name">{name}</h2>
      <small>{shortWhen}</small>
    </div>
  {:else}
    <div class="dh">
      <div class="dhm">
        <p class="mono sub ref">{job.ref}</p>
        <h2 id="dt-name">{name}</h2>
        <div class="meta">
          <span class={st.c}>{st.t}</span>
          {#if job.kind === "ensemble"}<span class="chip">Ensemble</span>{/if}
          <span class="chip">{engineName(job.engine)}</span>
          <span class={computeChip(job.compute)}>{computeName(job.compute)}</span>
          {#if route.transfer}
            <span class="chip" title="Compiles on {engineName(route.build)}, then runs on {engineName(route.target)}">
              <Icon name="route" size={12} />{routeText(route.build, route.target)}
            </span>
          {/if}
          <span class="sub when">{whenText(job)}</span>
        </div>
      </div>
      <div class="sp">
        {#if v?.log}<CopyButton text={v.log} label="Copy log" />{/if}
        {#if job.status === "running"}
          <button class="small danger" data-cancel onclick={ask} disabled={confirming}>Cancel build</button>
        {:else if job.status === "queued"}
          <button class="small danger" onclick={cancel} disabled={busy !== ""}>Remove</button>
        {:else if again}
          <a class="btn sm" href={href("builds", again.id)}>Open the new build</a>
        {:else}
          <button class="small" onclick={retry} disabled={busy !== ""}>
            {#if busy === "retry"}<span class="spin"></span>{:else}<Icon name="retry" size={14} />{/if}Build again
          </button>
        {/if}
      </div>
    </div>
  {/if}

  {#if confirming}
    <div class="confirm callout wa" role="group" aria-label="Cancel this build?">
      <Icon name="warn" />
      <span>
        <b>Cancel the build of {name}?</b>
        <p>It stops now and no image is made. You can build it again later.</p>
        <span class="row">
          <button class="small danger" onclick={cancel} onkeydown={onConfirmKey} disabled={busy !== ""}>
            {#if busy === "cancel"}<span class="spin"></span>{/if}Yes, cancel it
          </button>
          <button class="small ghost" data-keep onclick={keep} onkeydown={onConfirmKey}>Keep building</button>
        </span>
      </span>
    </div>
  {/if}

  {#if failed && ex}
    <div class="what {ex.tone}">
      <Icon name={ex.tone === "er" ? "errc" : "warn"} size={18} />
      <div>
        <b>{ex.title}</b>
        {#if ex.why}<p>{ex.why}</p>{:else if job.status === "canceled"}<p>No image was made.</p>{/if}
      </div>
    </div>
    {#if fixedBy}
      <div class="callout ok">
        <Icon name="check" />
        <span>
          <b>Built since.</b>
          <p>A later build of this image finished {ago(fixedBy.finished_at)}. <a href={href("builds", fixedBy.id)}>Open it</a></p>
        </span>
      </div>
    {:else if compact || !plainRetry}
      <FixList {job} {ex} {model} {compact} />
    {/if}
  {:else if job.status === "running"}
    <div class="prog">
      <div class="row">
        <span class="ph">{phaseText(job)}</span>
        <span class="lf">{leftText(job) || `${clock(job.elapsed_ms)} elapsed`}</span>
      </div>
      <div class="bar" class:busy={p == null} role="progressbar" aria-label="Progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={p == null ? undefined : Math.round(p * 100)}>
        <i style="width:{p == null ? 30 : Math.round(p * 100)}%"></i>
      </div>
      {#if !compact}
        <div class="row fine">
          <span>{clock(job.elapsed_ms)} elapsed{job.eta_seconds > 0 ? ` · the last ${computeName(job.compute)} build took about ${dur(job.eta_seconds)}` : ""}</span>
          {#if route.transfer}<span>Compiles on {engineName(route.build)}, then loads into {engineName(route.target)}</span>{/if}
        </div>
      {:else}
        <button class="small danger wide" data-cancel onclick={ask} disabled={confirming}>Cancel build</button>
      {/if}
    </div>
  {:else if job.status === "queued"}
    <div class="wait">
      <Icon name="clock" size={18} />
      <div>
        <b>{qi === 0 ? "Next" : `${placeText(job, Math.max(0, qi))} in line`} on {engineName(job.engine)}</b>
        <p>{wait ? `Starts in about ${wait.slice(1)}.` : "It starts when the builds ahead of it finish."}{route.transfer ? ` It will compile on ${engineName(route.build)}, then load into ${engineName(route.target)}.` : ""}</p>
        {#if queued.length > 1}
          <div class="mv" role="group" aria-label="Move it in the queue">
            <button class="small" disabled={qi <= 0 || busy !== ""} onclick={() => move("top")}><Icon name="toTop" size={14} />To the front</button>
            <button class="small" disabled={qi <= 0 || busy !== ""} onclick={() => move("up")}><Icon name="arrowup" size={14} />Up</button>
            <button class="small" disabled={qi >= queued.length - 1 || busy !== ""} onclick={() => move("down")}><Icon name="arrowDown" size={14} />Down</button>
            <button class="small" disabled={qi >= queued.length - 1 || busy !== ""} onclick={() => move("bottom")}><Icon name="toBottom" size={14} />To the back</button>
          </div>
        {/if}
        {#if compact}<button class="small danger wide" onclick={cancel} disabled={busy !== ""}>Remove from the queue</button>{/if}
      </div>
    </div>
  {:else if job.status === "done"}
    <div class="callout ok">
      <Icon name="check" />
      <span>
        <b>Image built{route.transfer ? ` on ${engineName(route.build)} and loaded into ${engineName(route.target)}` : ""}.</b>
        <p>{exported || `It's in the Library as ${job.ref}.`}</p>
      </span>
    </div>
    <div class="acts">
      <a class="btn sm pri" href={href("library", job.ref)}><Icon name="library" size={14} />Open in Library</a>
      {#if compact && !again}
        <button class="small" onclick={retry} disabled={busy !== ""}><Icon name="retry" size={14} />Build again</button>
      {/if}
    </div>
  {/if}

  {#if compact && failed && !again && !fixedBy && ex && !ex.fixes.some((f) => f.action === "retry")}
    <button class="small wide" onclick={retry} disabled={busy !== ""}><Icon name="retry" size={14} />Build again</button>
  {/if}

  {#if steps.length}<Steps {steps} />{/if}

  {#if job.status !== "queued"}
    <LogView {job} {compact} />
  {/if}
</section>

<style>
  .dt { padding: 20px 22px; min-width: 0; display: flex; flex-direction: column; gap: 16px; }
  .dh { display: flex; align-items: flex-start; gap: 12px; min-width: 0; }
  .dhm { min-width: 0; flex: 1; }
  .ref { font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .dh h2 { font-size: 18px; margin: 2px 0 0; }
  .meta { display: flex; gap: 6px; align-items: center; margin-top: 6px; flex-wrap: wrap; }
  .meta .chip :global(.ico) { margin-right: 1px; }
  .when { margin-left: 2px; }
  .sp { margin-left: auto; display: flex; gap: 8px; flex: none; }

  .what {
    padding: 14px 16px;
    border-radius: 12px;
    background: var(--ers);
    display: grid;
    grid-template-columns: 20px minmax(0, 1fr);
    gap: 10px;
    font-size: 13.5px;
  }
  .what.wa { background: var(--was); }
  .what > :global(.ico) { color: var(--er); margin-top: 1px; }
  .what.wa > :global(.ico) { color: var(--wa); }
  .what b { display: block; font-size: 14px; margin-bottom: 2px; }
  .what p { color: color-mix(in srgb, var(--ink) 85%, var(--cv)); }

  .confirm .row { display: flex; gap: 8px; margin-top: 8px; flex-wrap: wrap; }

  .prog { display: flex; flex-direction: column; gap: 7px; }
  .prog .row { display: flex; justify-content: space-between; gap: 12px; font-size: 13px; }
  .prog .ph { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .prog .lf { color: var(--mu); white-space: nowrap; }
  .prog .fine { font-size: 12px; color: var(--mu); flex-wrap: wrap; }
  .prog .bar { height: 8px; }

  .wait {
    display: grid;
    grid-template-columns: 20px minmax(0, 1fr);
    gap: 10px;
    padding: 14px 16px;
    border-radius: 12px;
    background: var(--p2);
    font-size: 13.5px;
  }
  .wait > :global(.ico) { color: var(--mu); margin-top: 1px; }
  .wait p { color: var(--mu); margin-top: 2px; }
  .mv { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 10px; }

  .acts { display: flex; gap: 8px; flex-wrap: wrap; }
  .wide { width: 100%; }

  /* Phone (f17): a compact card; the explanation and fixes first, the log folded. */
  .compact { padding: 14px; border-radius: 16px; gap: 10px; }
  .compact.fail { border-color: color-mix(in srgb, var(--er) 40%, var(--ln)); }
  .fh { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .fh h2 { font-size: 15px; margin: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .fh small { margin-left: auto; color: var(--mu); font-size: 12px; white-space: nowrap; }
  .sx {
    width: 22px;
    height: 22px;
    flex: none;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--p3);
    color: var(--mu);
  }
  .sx.error { background: var(--er); color: var(--cv); }
  .sx.done { background: var(--oks); color: var(--ok); }
  .sx.canceled { background: var(--was); color: var(--wa); }
  .sx .spin { width: 14px; height: 14px; }
  .compact .what { padding: 11px 12px; font-size: 13px; grid-template-columns: minmax(0, 1fr); }
  .compact .what > :global(.ico) { display: none; }
  .compact .what b { font-size: 13.5px; }
  .compact .what p { color: var(--mu); }
  .compact .prog .row { font-size: 12.5px; }
  .compact .wait { padding: 12px; }
</style>
