<script lang="ts">
  // The build queue under the Build form: running and queued builds (one at a
  // time per engine, the engines in parallel) and recent history, each with its
  // progress, elapsed time against the ETA, Cancel, and an expandable log.
  import Icon from "./Icon.svelte";
  import { build, views, cancelJob, toggleLog } from "../lib/build.svelte";
  import { computeBadge, engineBadge } from "../lib/util";
  import type { BuildJob } from "../lib/types";

  const HISTORY = 3; // finished builds shown before "Show more"

  const order = { running: 0, queued: 1 } as Record<string, number>;
  const active = $derived(
    build.jobs.filter((j) => j.active).sort((a, b) => (order[a.status] - order[b.status]) || a.created_at - b.created_at),
  );
  const finished = $derived(
    build.jobs.filter((j) => !j.active).sort((a, b) => b.finished_at - a.finished_at),
  );
  const shown = $derived([...active, ...(build.showHistory ? finished : finished.slice(0, HISTORY))]);
  const summary = $derived.by(() => {
    const running = active.filter((j) => j.status === "running").length;
    const queued = active.length - running;
    const parts = [];
    if (running) parts.push(`${running} building`);
    if (queued) parts.push(`${queued} queued`);
    return parts.join(" · ") || "idle";
  });

  function fmtDur(ms: number): string {
    const s = Math.max(0, Math.round(ms / 1000));
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
    const mm = h ? String(m).padStart(2, "0") : String(m);
    return `${h ? h + ":" : ""}${mm}:${String(sec).padStart(2, "0")}`;
  }

  const STATUS: Record<string, string> = {
    queued: "Queued", running: "Building", done: "Done", error: "Failed", canceled: "Canceled",
  };

  function timeText(j: BuildJob): string {
    switch (j.status) {
      case "running": {
        const el = fmtDur(j.elapsed_ms);
        if (!j.eta_seconds) return el;
        return j.elapsed_ms > j.eta_seconds * 1000
          ? `${el} · past the ~${fmtDur(j.eta_seconds * 1000)} estimate`
          : `${el} of ~${fmtDur(j.eta_seconds * 1000)}`;
      }
      case "queued":
        if (j.wait_seconds) return `starts in ~${fmtDur(j.wait_seconds * 1000)}`;
        return j.position ? `${j.position} ahead on ${engineBadge(j.engine)}` : "starting…";
      default:
        return j.started_at ? `took ${fmtDur(j.elapsed_ms)}` : "never started";
    }
  }

  // Keep a streaming log pinned to its newest line unless the reader scrolled up.
  function autoscroll(node: HTMLElement, _text: string) {
    let stick = true;
    const onScroll = () => { stick = node.scrollTop + node.clientHeight >= node.scrollHeight - 24; };
    node.addEventListener("scroll", onScroll);
    node.scrollTop = node.scrollHeight;
    return {
      update() { if (stick) node.scrollTop = node.scrollHeight; },
      destroy() { node.removeEventListener("scroll", onScroll); },
    };
  }
</script>

{#if build.jobs.length}
  <div class="queue">
    <div class="qhead">
      <h3 class="subhead">Build queue</h3>
      <span class="qsum">{summary}</span>
    </div>
    {#each shown as j (j.id)}
      {@const v = views[j.id]}
      <div class="job st-{j.status}">
        <div class="jrow">
          <span class="jstat">{STATUS[j.status] ?? j.status}</span>
          <span class="jmain">
            <span class="jname">{j.name || j.ref}{#if j.kind === "ensemble"} <span class="kind">ensemble</span>{/if}</span>
            <span class="jref">{j.ref}</span>
          </span>
          <span class="pills">
            <span class="pill">{engineBadge(j.engine)}</span>
            <span class="pill">{computeBadge(j.compute)}</span>
          </span>
          <span class="jtime">{timeText(j)}</span>
          {#if j.active}
            <button class="ghost icon-btn" onclick={() => cancelJob(j)} title={j.status === "running" ? "Cancel this build" : "Remove from the queue"} aria-label="Cancel build">
              <Icon name="x" size={15} />
            </button>
          {/if}
          <button class="ghost icon-btn" onclick={() => toggleLog(j)} title={v?.open ? "Hide log" : "Show log"} aria-label="Toggle log" aria-expanded={!!v?.open}>
            <span class="caret" class:up={v?.open}><Icon name="chevron" size={15} /></span>
          </button>
        </div>
        {#if j.status === "running"}
          <div class="bar" class:active={v?.pct == null}>
            <div class="fill" class:determinate={v?.pct != null} style={v?.pct != null ? `width:${v.pct}%` : ""}></div>
          </div>
          <div class="phase">{v?.phase || j.last_line || "Starting…"}{#if v?.pct != null} ({v.pct}%){/if}</div>
        {:else if j.status === "error" && j.error}
          <div class="jerr"><Icon name="alert" size={13} /> {j.error}</div>
        {/if}
        {#if v?.open}
          <pre class="log" use:autoscroll={v.log}>{v.log || (j.status === "queued" ? "Waiting for its turn…" : "Loading…")}</pre>
        {/if}
      </div>
    {/each}
    {#if finished.length > HISTORY}
      <button class="ghost more" onclick={() => (build.showHistory = !build.showHistory)}>
        {build.showHistory ? "Show fewer" : `Show ${finished.length - HISTORY} older`}
      </button>
    {/if}
  </div>
{/if}

<style>
  .queue { margin-top: 18px; display: flex; flex-direction: column; gap: 8px; }
  .qhead { display: flex; align-items: baseline; gap: 10px; }
  .subhead { margin: 0; font-size: 13px; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.4px; }
  .qsum { font-size: 12px; color: var(--muted); }

  .job { border: 1px solid var(--border); border-radius: 10px; background: #12151c; padding: 9px 11px; display: flex; flex-direction: column; gap: 7px; }
  .job.st-running { border-color: #33508f; }
  .job.st-error { border-color: #4a2415; }
  .jrow { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .jstat { flex: none; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.3px; padding: 2px 8px; border-radius: 999px; border: 1px solid var(--border); color: var(--muted); background: #161922; }
  .st-running .jstat { color: #cdd8f2; border-color: #33508f; background: #1c2740; }
  .st-queued .jstat { color: #b9c6e0; }
  .st-done .jstat { color: #46c969; border-color: #1d4427; background: #11261a; }
  .st-error .jstat { color: #f0683c; border-color: #4a2415; background: #240f09; }
  .jmain { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .jname { font-size: 13.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .kind { font-size: 11px; color: var(--muted); }
  .jref { font-size: 11.5px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: ui-monospace, monospace; }
  .pills { display: inline-flex; gap: 4px; flex: none; }
  .pill { font-size: 11px; color: #9fb0cc; background: #1b2233; border: 1px solid var(--border); border-radius: 5px; padding: 0 6px; line-height: 18px; }
  .jtime { flex: none; font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .icon-btn { flex: none; display: inline-flex; align-items: center; padding: 4px 6px; }
  .caret { display: inline-flex; transition: transform 0.15s; }
  .caret.up { transform: rotate(180deg); }
  .phase { font-size: 12px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .jerr { font-size: 12.5px; color: #f0a08a; display: flex; gap: 6px; align-items: flex-start; }
  .jerr :global(.ico) { margin-top: 3px; }
  .more { align-self: flex-start; }

  .bar { height: 8px; background: #0c0e13; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .fill { height: 100%; width: 0; background: linear-gradient(90deg, var(--accent-2), var(--accent)); }
  .fill.determinate { transition: width 0.3s ease; }
  .bar.active .fill { width: 35%; animation: sweep 1.3s ease-in-out infinite; }
  @keyframes sweep { 0% { margin-left: -35%; } 100% { margin-left: 100%; } }
  @media (max-width: 720px) { .pills { display: none; } }
  .log { margin: 0; background: #06080c; border: 1px solid var(--border); border-radius: 8px; padding: 12px; max-height: 320px; overflow: auto; white-space: pre-wrap; font: 12.5px/1.5 ui-monospace, monospace; color: #cbd3e1; }
</style>
