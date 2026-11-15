<script lang="ts">
  // One saved ensemble: what's in it, how it routes, and its image build (the
  // shared build queue runs it; this card follows the job and its log).
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { gb, ago, computeName, computeChip, plural, engineName } from "../../lib/format";
  import { build, views, openLog, closeLog, stopJob } from "../../lib/build.svelte";
  import { phase, progress, timeText } from "../../lib/jobs";
  import { live } from "../../lib/live.svelte";
  import type { BuildJob, Ensemble } from "../../lib/types";
  import { TOOL_TEXT, TOOL_WHEN, ensembleRef, latestJob, builtImage } from "./util";

  let {
    ens,
    starting = false,
    startError = "",
    onbuild,
    onedit,
    ondelete,
  }: {
    ens: Ensemble;
    starting?: boolean;
    startError?: string;
    onbuild: () => void;
    onedit: () => void;
    ondelete: () => void;
  } = $props();

  const uid = $props.id();
  const job = $derived(latestJob(build.jobs, ens));
  const view = $derived(job ? views[job.id] : undefined);

  function toggleLog(j: BuildJob) {
    if (views[j.id]?.open) closeLog(j.id);
    else openLog(j.id);
  }
  async function cancelJob(j: BuildJob) {
    if (j.status === "running" && !confirm(`Cancel the running build of ${j.ref}?`)) return;
    await stopJob(j);
  }
  const image = $derived(builtImage(live.images, ens));
  const members = $derived(ens.members ?? []);
  const active = $derived(!!job?.active);
  const pct = $derived(job ? progress(job) : null);
  const conductorName = $derived((ens.conductor || "").replace(/^local-llm\//, "").replace(/:latest$/, ""));
</script>

<article class="card ens" aria-labelledby="{uid}-name">
  <div class="top">
    <div class="ttl">
      <h3 id="{uid}-name">{ens.name}</h3>
      <div class="chips">
        <span class="chip">{ens.routing === "tool-calling" ? "Tool-calling" : "Heuristic"}</span>
        <span class={computeChip(ens.compute)}>{computeName(ens.compute)}</span>
        <span class="chip">{gb(ens.vram_budget_gb, 0)} budget</span>
        {#if ens.package_mode === "embedded"}<span class="chip wa" title="Embedded isn't built yet: its members don't start">Embedded</span>{/if}
      </div>
    </div>
    <div class="acts">
      <button class="small primary" onclick={onbuild} disabled={starting || active} title={active ? "It's already in the build queue" : "Build its image"}>
        {#if starting}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="builds" size={14} />{/if}
        {image ? "Build again" : "Build image"}
      </button>
      <button class="small" onclick={onedit}><Icon name="pen" size={14} />Edit</button>
      <button class="small ghost icon danger" onclick={ondelete} aria-label="Delete {ens.name}" title="Delete">
        <Icon name="trash" size={15} />
      </button>
    </div>
  </div>

  <ul class="mem">
    {#each members as m (m.image + m.tool)}
      <li>
        <span class="mi" title={modMeta(m.modality).label}><Icon name={modMeta(m.modality).icon} size={14} /></span>
        <span class="tool">
          {TOOL_TEXT[m.tool] ?? m.tool}
          {#if (ens.routing !== "tool-calling" || m.tool === "transcribe") && TOOL_WHEN[m.tool]}<small>{TOOL_WHEN[m.tool]}</small>{/if}
        </span>
        <span class="img mono" title={m.image}>{m.image.replace(/^local-llm\//, "")}</span>
        <span class="vr mono">{m.vram_gb ? `~${gb(m.vram_gb, 0)}` : "—"}</span>
      </li>
    {:else}
      <li class="none">No members.</li>
    {/each}
  </ul>
  {#if ens.routing === "tool-calling"}
    <p class="cond">
      {#if conductorName}Conductor model: <span class="mono">{conductorName}</span>{:else}No conductor model, so it routes with the heuristic.{/if}
    </p>
  {/if}

  <!-- The image and its build -->
  {#if startError}
    <div class="callout er" role="alert"><Icon name="errc" size={16} /><span><b>It wasn't queued.</b><p>{startError}</p></span></div>
  {/if}

  {#if job && job.status === "queued"}
    <div class="st">
      <span class="chip pr">Queued</span>
      <span class="sx">
        {job.position ? `${plural(job.position, "build")} ahead on ${engineName(job.engine)}` : `Next on ${engineName(job.engine)}`}{job.position && job.wait_seconds > 0 ? ` · ${timeText(job)}` : ""}
      </span>
      <a class="lnk" href={href("builds", job.id)}>Open the queue</a>
      <button class="small ghost" onclick={() => cancelJob(job)}>Cancel</button>
    </div>
  {:else if job && job.status === "running"}
    <div class="st run">
      <div class="rl">
        <span class="spin" aria-hidden="true"></span>
        <span class="sx">{phase(job)}</span>
        <span class="tt mono">{timeText(job)}</span>
      </div>
      <div class="bar" class:busy={pct == null} aria-hidden="true"><i style="width:{pct == null ? 30 : Math.round(pct * 100)}%"></i></div>
      <div class="rl">
        <button class="small ghost" onclick={() => toggleLog(job)} aria-expanded={!!view?.open}>
          <Icon name="doc" size={14} />{view?.open ? "Hide log" : "Show log"}
        </button>
        <button class="small ghost" onclick={() => cancelJob(job)}>Cancel</button>
      </div>
    </div>
  {:else if job && job.status === "error"}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>The image didn't build.</b>
        <p>{job.error || "The log says why."} Fix what it says, then build it again.</p>
        <button class="small ghost" onclick={() => toggleLog(job)} aria-expanded={!!view?.open}>
          <Icon name="doc" size={14} />{view?.open ? "Hide log" : "Show log"}
        </button>
      </span>
    </div>
  {:else if job && job.status === "canceled" && !image}
    <div class="st"><span class="chip">Canceled</span><span class="sx">The last build was canceled {ago(job.finished_at)}.</span></div>
  {/if}

  {#if image && !active}
    <div class="st ok">
      <span class="chip ok"><Icon name="check" size={12} stroke={2.6} />Image ready</span>
      <span class="sx mono">{ensembleRef(ens)}</span>
      {#if job?.status === "done" && job.finished_at}<span class="tt">built {ago(job.finished_at)}</span>{/if}
      <a class="lnk" href={href("library")}>Run it from the Library</a>
    </div>
  {:else if !job && !image}
    <p class="fine">Not built yet. Build its image, then run it from the Library: it answers like one model.</p>
  {/if}

  {#if job && view?.open}
    <pre class="log">{view.log || "No output yet."}</pre>
  {/if}
</article>

<style>
  .ens { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .top { display: flex; align-items: flex-start; gap: 12px; flex-wrap: wrap; }
  .ttl { flex: 1; min-width: 200px; }
  h3 { font: 700 16px/1.25 var(--font-display); margin: 0; letter-spacing: -0.005em; }
  .chips { display: flex; gap: 6px; flex-wrap: wrap; margin-top: 6px; }
  .acts { display: flex; gap: 6px; align-items: center; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }

  .mem { list-style: none; margin: 0; padding: 0; }
  .mem li {
    display: grid;
    grid-template-columns: 28px minmax(120px, 0.9fr) minmax(0, 1.4fr) 48px;
    gap: 10px;
    align-items: center;
    padding: 7px 0;
    border-top: 1px solid var(--ln);
    font-size: 13px;
  }
  .mem li:first-child { border-top: 0; }
  .mem li.none { display: block; color: var(--mu); }
  .mi { width: 28px; height: 28px; border-radius: 8px; background: var(--p3); display: grid; place-items: center; color: var(--mu); }
  .tool { color: var(--ink); line-height: 1.3; }
  .tool small { display: block; color: var(--fa); font-size: 11.5px; }
  .img { color: var(--mu); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .vr { color: var(--fa); font-size: 12px; text-align: right; }
  .cond { font-size: 12.5px; color: var(--mu); }
  .cond .mono { color: var(--ink); font-size: 12px; }

  .st {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
    padding: 9px 12px;
    border-radius: 10px;
    background: var(--p2);
    font-size: 12.5px;
  }
  .st.ok { background: color-mix(in srgb, var(--ok) 7%, var(--p1)); }
  .st.run { flex-direction: column; align-items: stretch; gap: 8px; background: var(--prs); }
  .rl { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; }
  .sx { color: var(--mu); min-width: 0; }
  .st.run .sx { color: var(--ink); flex: 1; }
  .sx.mono { font-size: 11.5px; }
  .tt { color: var(--fa); font-size: 11.5px; }
  .lnk { font-size: 12.5px; margin-left: auto; }
  .chip :global(.ico) { margin-left: -2px; }
  .callout button { margin: 6px 0 0 -10px; }
  .fine { color: var(--fa); font-size: 12px; }
  .log {
    margin: 0;
    padding: 9px 11px;
    border-radius: 9px;
    background: color-mix(in srgb, var(--cv) 70%, black);
    border: 1px solid var(--ln);
    font: 11.5px/1.5 var(--font-mono);
    color: var(--mu);
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    max-height: 260px;
    overflow: auto;
  }

  @media (max-width: 760px) {
    .mem li { grid-template-columns: 28px minmax(0, 1fr) 44px; }
    .img { grid-column: 2 / 4; grid-row: 2; }
    .acts { width: 100%; }
    .acts .primary { flex: 1; }
  }
</style>
