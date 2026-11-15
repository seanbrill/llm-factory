<script lang="ts">
  // One saved ensemble: its members and their live state while it runs (from
  // /api/ensembles/status, polled by the page), its persona and voice, Run and
  // Stop, warming or stopping one member, and its image build (the shared build
  // queue runs it; this card follows the job and its log).
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { gb, ago, computeName, computeChip, plural, engineName } from "../../lib/format";
  import { build, views, openLog, closeLog, stopJob } from "../../lib/build.svelte";
  import { phase, progress, timeText } from "../../lib/jobs";
  import { live } from "../../lib/live.svelte";
  import { post, type ApiError } from "../../lib/api";
  import { personas, voices, toast } from "../../lib/stores";
  import type { BuildJob, Ensemble, EnsembleLive, GateMember } from "../../lib/types";
  import { TOOL_TEXT, TOOL_WHEN, ensembleRef, latestJob, builtImage, voiceText } from "./util";

  let {
    ens,
    status,
    starting = false,
    startError = "",
    onbuild,
    onedit,
    ondelete,
    onchanged,
  }: {
    ens: Ensemble;
    status?: EnsembleLive; // undefined until the first status answer
    starting?: boolean;
    startError?: string;
    onbuild: () => void;
    onedit: () => void;
    ondelete: () => void;
    onchanged: () => void; // something changed: poll the status now
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
  const built = $derived(status ? status.built : !!image);
  const members = $derived(ens.members ?? []);
  const active = $derived(!!job?.active);
  const pct = $derived(job ? progress(job) : null);
  const conductorName = $derived((ens.conductor || "").replace(/^local-llm\//, "").replace(/:latest$/, ""));
  const running = $derived(status?.running ?? null);
  const gate = $derived(status?.gate);
  const gateMember = (tool: string): GateMember | undefined => gate?.members.find((m) => m.tool === tool);

  // ---- persona and voice ------------------------------------------------------------
  const persona = $derived(ens.persona ? $personas.find((p) => p.id === ens.persona) : undefined);
  const clipName = $derived(persona?.voice?.kind === "clone" ? $voices.find((c) => c.id === persona.voice?.clip)?.name : undefined);

  // ---- run, stop, warm a member ------------------------------------------------------
  let busy = $state<"" | "run" | "stop">("");
  let runError = $state("");
  async function run() {
    busy = "run";
    runError = "";
    try {
      const r = await post<{ port: number; url?: string; warning?: string }>("/api/ensembles/run", { id: ens.id });
      if (r.warning) toast(r.warning, 9000);
      else toast(`“${ens.name}” is starting on :${r.port}. Its members start when they're first needed.`, 6000);
    } catch (e) {
      runError = (e as ApiError).message || "The factory didn't answer.";
    } finally {
      busy = "";
      onchanged();
    }
  }
  async function stop() {
    busy = "stop";
    runError = "";
    try {
      await post("/api/ensembles/stop", { id: ens.id });
      toast(`“${ens.name}” and its members stopped.`);
    } catch (e) {
      runError = `It didn't stop: ${(e as ApiError).message || "the factory didn't answer"}.`;
    } finally {
      busy = "";
      onchanged();
    }
  }

  let memberBusy = $state<Record<string, "start" | "stop" | "">>({});
  let memberErr = $state<Record<string, string>>({});
  async function member(tool: string, action: "start" | "stop") {
    memberBusy[tool] = action;
    memberErr[tool] = "";
    try {
      await post("/api/ensembles/member", { id: ens.id, tool, action });
    } catch (e) {
      memberErr[tool] = (e as ApiError).message || "The factory didn't answer.";
    } finally {
      memberBusy[tool] = "";
      onchanged();
    }
  }

  const STATE: Record<string, { text: string; cls: string }> = {
    ready: { text: "Ready", cls: "chip ok" },
    starting: { text: "Starting…", cls: "chip pr" },
    failed: { text: "Failed", cls: "chip er" },
    stopped: { text: "Stopped", cls: "chip" },
  };
  const usedPct = $derived(gate && gate.budget_gb > 0 ? Math.min(100, Math.round((gate.used_gb / gate.budget_gb) * 100)) : 0);
</script>

<article class="card ens" aria-labelledby="{uid}-name">
  <div class="top">
    <div class="ttl">
      <h3 id="{uid}-name">{ens.name}</h3>
      <div class="chips">
        {#if running}<span class="chip ok"><span class="dot"></span>Running on :{running.port}</span>{:else if built}<span class="chip">Stopped</span>{/if}
        <span class="chip">{ens.routing === "tool-calling" ? "Tool-calling" : "Heuristic"}</span>
        <span class={computeChip(ens.compute)} title="Compute for member images that don't say">{computeName(ens.compute)}</span>
        <span class="chip" title="What its members may use at once">{gb(ens.vram_budget_gb, 0)} budget</span>
      </div>
    </div>
    <div class="acts">
      {#if running}
        <a class="btn sm pri" href={href("playground", String(running.port))}><Icon name="playground" size={14} />Open in Playground</a>
        <button class="small" onclick={stop} disabled={!!busy}>
          {#if busy === "stop"}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="stopsq" size={14} />{/if}Stop
        </button>
      {:else if built}
        <button class="small primary" onclick={run} disabled={!!busy || active} title={active ? "Wait for the build to finish" : "Start the ensemble"}>
          {#if busy === "run"}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="play" size={14} />{/if}Run
        </button>
      {/if}
      <button class="small" class:primary={!built && !running} onclick={onbuild} disabled={starting || active} title={active ? "It's already in the build queue" : "Build its image"}>
        {#if starting}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="builds" size={14} />{/if}
        {built ? "Build again" : "Build image"}
      </button>
      <button class="small" onclick={onedit}><Icon name="pen" size={14} />Edit</button>
      <button class="small ghost icon danger" onclick={ondelete} aria-label="Delete {ens.name}" title="Delete">
        <Icon name="trash" size={15} />
      </button>
    </div>
  </div>

  {#if runError}
    <div class="callout er" role="alert"><Icon name="errc" size={16} /><span><b>{runError}</b>{#if !built}<p>Build its image first, then run it.</p>{/if}</span></div>
  {/if}
  {#if status?.gate_error}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>It's running, but its Conductor didn't answer.</b>
        <p>{status.gate_error}. It may still be starting; if this stays, stop it and run it again, or read its log in the <a href={href("library")}>Library</a>.</p>
      </span>
    </div>
  {/if}
  {#if status?.voice_error}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span><b>Its voice won't work as set.</b><p>{status.voice_error}</p></span>
    </div>
  {/if}

  <ul class="mem">
    {#each members as m (m.image + m.tool)}
      {@const g = gateMember(m.tool)}
      {@const st = g ? STATE[g.state] ?? STATE.stopped : null}
      {@const mb = memberBusy[m.tool] ?? ""}
      <li>
        <span class="mi" title={modMeta(m.modality).label}><Icon name={modMeta(m.modality).icon} size={14} /></span>
        <span class="tool">
          {TOOL_TEXT[m.tool] ?? m.tool}
          {#if (ens.routing !== "tool-calling" || m.tool === "transcribe" || m.tool === "speak") && TOOL_WHEN[m.tool]}<small>{TOOL_WHEN[m.tool]}</small>{/if}
        </span>
        <span class="img mono" title={m.image}>{m.image.replace(/^local-llm\//, "")}{#if m.pinned}<span class="pin" title="Kept warm: started with the ensemble, never stopped to make room"> · warm</span>{/if}</span>
        <span class="vr mono" title="Its share of the memory budget">{(g?.mem_gb ?? m.vram_gb) ? `~${gb(g?.mem_gb ?? m.vram_gb, 0)}` : "—"}</span>
        <span class="state">
          {#if st}
            <span class={st.cls}>{#if g?.state === "starting"}<span class="spin sm" aria-hidden="true"></span>{/if}{st.text}</span>
          {:else if running}
            <span class="chip">—</span>
          {/if}
        </span>
        <span class="ma">
          {#if running && g}
            {#if mb}
              <button class="small ghost" disabled><span class="spin sm" aria-hidden="true"></span>{mb === "start" ? "Starting" : "Stopping"}</button>
            {:else if g.state === "ready" || g.state === "starting"}
              <button class="small ghost" onclick={() => member(m.tool, "stop")} title="Stop this member to free its memory">Stop</button>
            {:else}
              <button class="small ghost" onclick={() => member(m.tool, "start")} title="Start it now, so the first request doesn't wait">Start</button>
            {/if}
          {/if}
        </span>
        {#if g?.state === "failed" && g.error}
          <p class="merr">{g.error}</p>
        {/if}
        {#if memberErr[m.tool]}
          <p class="merr">{memberErr[m.tool]}</p>
        {/if}
      </li>
    {:else}
      <li class="none">No members.</li>
    {/each}
  </ul>

  {#if gate}
    <div class="meter">
      <div class="mh"><span>Members in memory</span><span class="mono">{gb(gate.used_gb, 1)} of {gb(gate.budget_gb, 0)}</span></div>
      <div class="bar" class:ok={usedPct < 90} class:wa={usedPct >= 90}><i style="width:{usedPct}%"></i></div>
    </div>
  {/if}

  <p class="per">
    <Icon name="persona" size={13} />
    {#if ens.persona && !persona}
      Its persona was deleted: <button class="lnkb" onclick={onedit}>pick another</button>.
    {:else if persona}
      Plays <a href={href("personas", persona.id)}>{persona.name}</a> and speaks with {voiceText(persona.voice, clipName)}.
      {#if gate?.persona && gate.persona.synced === false}<span class="fa">The running ensemble gets it with its next request.</span>{/if}
    {:else}
      No persona: no system prompt, and the speech member's default voice.
    {/if}
  </p>
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
  {:else if job && job.status === "canceled" && !built}
    <div class="st"><span class="chip">Canceled</span><span class="sx">The last build was canceled {ago(job.finished_at)}.</span></div>
  {/if}

  {#if built && !active}
    <div class="st ok">
      <span class="chip ok"><Icon name="check" size={12} stroke={2.6} />Image ready</span>
      <span class="sx mono">{status?.image || ensembleRef(ens)}</span>
      {#if job?.status === "done" && job.finished_at}<span class="tt">built {ago(job.finished_at)}</span>{/if}
      {#if !running}<span class="lnk sx">Run it here or from the <a href={href("library")}>Library</a>.</span>{/if}
    </div>
  {:else if !job && !built}
    <p class="fine">Not built yet. Build its image, then run it: it answers like one model, in the Playground and through the factory's /v1 gateway.</p>
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
  .chips .dot { width: 6px; height: 6px; box-shadow: none; }
  .acts { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
  .spin.sm { width: 11px; height: 11px; }
  .acts .spin { width: 12px; height: 12px; }

  .mem { list-style: none; margin: 0; padding: 0; }
  .mem li {
    display: grid;
    grid-template-columns: 28px minmax(120px, 0.9fr) minmax(0, 1.3fr) 48px 92px 64px;
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
  .pin { color: var(--pr); }
  .vr { color: var(--fa); font-size: 12px; text-align: right; }
  .state { display: flex; justify-content: flex-end; }
  .ma { display: flex; justify-content: flex-end; }
  .ma button { height: 28px; padding: 0 9px; font-size: 12px; }
  .merr { grid-column: 2 / -1; margin: -2px 0 2px; color: var(--er); font-size: 12px; line-height: 1.45; overflow-wrap: anywhere; }

  .meter { display: flex; flex-direction: column; gap: 6px; }
  .mh { display: flex; justify-content: space-between; font-size: 12px; color: var(--mu); }
  .mh .mono { font-size: 11.5px; }
  .per { display: flex; align-items: baseline; gap: 6px; flex-wrap: wrap; font-size: 12.5px; color: var(--mu); }
  .per :global(.ico) { align-self: center; flex: none; }
  .per .fa { color: var(--fa); }
  .lnkb { all: unset; color: var(--pr); cursor: pointer; text-decoration: underline; }
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

  @media (max-width: 1100px) {
    .mem li { grid-template-columns: 28px minmax(0, 1fr) 48px 92px 64px; }
    .img { grid-column: 2 / -1; grid-row: 2; }
  }
  @media (max-width: 760px) {
    .mem li { grid-template-columns: 28px minmax(0, 1fr) auto auto; }
    .vr { display: none; }
    .acts { width: 100%; }
  }
</style>
