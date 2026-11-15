<script lang="ts">
  // Overview on a phone (sheet frame f16): a glance at what's building, the
  // one thing that needs you (with its fix), and what's running.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { post } from "../../lib/api";
  import { catalog, resources, sysinfo, toast } from "../../lib/stores";
  import { build, watchQueue } from "../../lib/build.svelte";
  import { live, refresh } from "../../lib/live.svelte";
  import { href } from "../../lib/router";
  import { computeChip, computeName, dur, engineName, gb, plural } from "../../lib/format";
  import { compiledOn, phase, progress, timeText } from "../../lib/jobs";
  import { allDoneIn, problems, runRows, type FailureItem, type Problem, type RunRow } from "../../lib/overview";

  let {
    failures,
    logs,
    queue,
    queueErr,
  }: { failures: FailureItem[]; logs: Record<string, string>; queue: "loading" | "ok" | "error"; queueErr: string } = $props();

  const runningJobs = $derived(build.jobs.filter((j) => j.status === "running"));
  const queued = $derived(
    build.jobs.filter((j) => j.status === "queued").sort((a, b) => a.position - b.position || a.created_at - b.created_at),
  );
  const doneIn = $derived(allDoneIn(build.jobs));
  const probs = $derived(problems($sysinfo, build.jobs, $catalog, failures, logs, live.storage));
  let showAll = $state(false);
  const rows = $derived(runRows(live.containers, $catalog, $resources));
  let stopping = $state<Record<string, boolean>>({});
  let retrying = $state(false);

  function names(list: string[]): string {
    if (list.length <= 2) return list.join(" and ");
    return `${list.slice(0, 2).join(", ")} and ${plural(list.length - 2, "more")}`;
  }

  async function retry(p: Problem) {
    if (!p.retryId) return;
    retrying = true;
    try {
      await post("/api/builds/retry", { id: p.retryId });
      toast("Queued again.");
      watchQueue();
    } catch (e) {
      toast(`Couldn't queue it again: ${(e as Error).message}`, 7000);
    } finally {
      retrying = false;
    }
  }

  async function stop(r: RunRow) {
    if (!confirm(`Stop ${r.name}? Its container is removed; the image stays in your Library.`)) return;
    stopping[r.key] = true;
    try {
      await post("/api/stop", { id: r.id, engine: r.engine });
      toast(`Stopped ${r.name}.`);
      refresh("containers", "resources", "harness");
    } catch (e) {
      toast(`Couldn't stop ${r.name}: ${(e as Error).message}`, 7000);
    } finally {
      delete stopping[r.key];
    }
  }
</script>

<div class="ph">
  <section class="card">
    <p class="lbl">{runningJobs.length ? "Building now" : "Builds"}<a class="r" href={href("builds")}>Queue</a></p>
    {#if !build.jobs.length && queue === "loading"}
      <p class="sub st"><span class="spin"></span>Reading the build queue…</p>
    {:else if !build.jobs.length && queue === "error"}
      <p class="sub">Couldn't read the build queue ({queueErr}). This page tries again on its own.</p>
    {:else}
      {#each runningJobs as j (j.id)}
        {@const p = progress(j)}
        {@const on = compiledOn(j)}
        <div class="bj">
          <a class="nm" href={href("builds", j.id)}>{j.name}</a>
          <div class="meta">
            <span class={computeChip(j.compute)}>{computeName(j.compute)}</span>
            {on !== j.engine ? `${engineName(on)}, then ${engineName(j.engine)}` : engineName(j.engine)}
          </div>
          <div class="bar" class:busy={p == null}><i style="width:{((p ?? 0.3) * 100).toFixed(1)}%"></i></div>
          <div class="row"><span><b>{phase(j)}</b></span><span>{timeText(j)}</span></div>
        </div>
      {:else}
        <p class="sub">Nothing is building. <a href={href("catalog")}>Pick models to build</a>.</p>
      {/each}
      {#if queued.length}
        <div class="next">
          {runningJobs.length ? "Then" : "Waiting:"}
          {#each queued.slice(0, 2) as q, i (q.id)}{#if i > 0}{queued.length === 2 ? " and " : ", "}{/if}<b>{q.name}</b>{/each}{#if queued.length > 2}{` and ${plural(queued.length - 2, "more")}`}{/if}{#if doneIn > 0}{` · all done in about ${dur(doneIn)}`}{/if}
        </div>
      {/if}
    {/if}
  </section>

  {#each showAll ? probs : probs.slice(0, 1) as p (p.key)}
    <section class="card alert {p.tone}">
      <Icon name={p.tone === "er" ? "errc" : "warn"} size={18} />
      <div class="at">
        <b>{p.title}</b>
        <p>{p.text}</p>
        <div class="acts">
          {#if p.cmd}<CopyButton text={p.cmd} label="Copy the command" cls="" title="Copy the command" />{/if}
          {#if p.retryId}
            <button onclick={() => retry(p)} disabled={retrying}><Icon name="retry" size={15} />Build it again</button>
          {/if}
          {#if p.href}<a class="btn" class:gh={!!(p.cmd || p.retryId)} href={p.href}>{p.hrefLabel}</a>{/if}
        </div>
      </div>
    </section>
  {/each}
  {#if probs.length > 1}
    <button class="ghost more" onclick={() => (showAll = !showAll)} aria-expanded={showAll}>
      {showAll ? "Show only the first" : `${plural(probs.length - 1, "more thing needs", "more things need")} you`}
      <Icon name={showAll ? "chevronUp" : "chevronDown"} size={15} />
    </button>
  {/if}

  <section class="card">
    <p class="lbl">Running · {rows.length}<a class="r" href={href("library")}>Library</a></p>
    {#if !live.loaded.containers}
      <p class="sub st"><span class="spin"></span>Looking for running models…</p>
    {:else if live.errors.containers && !rows.length}
      <p class="sub">Couldn't list the running models ({live.errors.containers}).</p>
    {:else if !rows.length}
      <p class="sub">Nothing is running.</p>
    {:else}
      {#each rows as r (r.key)}
        <div class="run">
          <span class="dot"></span>
          <span class="rn">
            {#if r.port}<a href={href("playground", r.port)}>{r.name}</a>{:else}<b>{r.name}</b>{/if}
            <small>{r.port ? `:${r.port}` : engineName(r.engine)}{r.memGB != null ? ` · ${gb(r.memGB)}` : ""}</small>
          </span>
          <button onclick={() => stop(r)} disabled={stopping[r.key]} aria-label="Stop {r.name}">
            {#if stopping[r.key]}<span class="spin"></span>{/if}Stop
          </button>
        </div>
      {/each}
    {/if}
  </section>
</div>

<style>
  .ph { display: flex; flex-direction: column; gap: 12px; }
  .card { border-radius: 16px; padding: 14px; }
  .lbl {
    margin-bottom: 8px;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .lbl .r { margin-left: auto; letter-spacing: 0; text-transform: none; font-size: 12.5px; color: var(--pr); font-weight: 600; }
  .bj + .bj { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--ln); }
  .nm { font-weight: 600; font-size: 15px; color: var(--ink); }
  .meta { display: flex; gap: 6px; align-items: center; margin-top: 4px; font-size: 12.5px; color: var(--mu); }
  .bar { height: 7px; margin: 12px 0 6px; }
  .row { display: flex; justify-content: space-between; gap: 10px; font-size: 12.5px; color: var(--mu); }
  .row b { color: var(--ink); font-weight: 600; }
  .next { margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--ln); font-size: 12.5px; color: var(--mu); }
  .next b { color: var(--ink); font-weight: 500; }
  .alert { display: grid; grid-template-columns: 20px minmax(0, 1fr); gap: 10px; }
  .alert.wa { background: var(--was); border-color: color-mix(in srgb, var(--wa) 35%, transparent); }
  .alert.wa > :global(.ico) { color: var(--wa); }
  .alert.er { background: var(--ers); border-color: color-mix(in srgb, var(--er) 35%, transparent); }
  .alert.er > :global(.ico) { color: var(--er); }
  .at b { display: block; font-size: 14px; }
  .at p { font-size: 12.5px; color: var(--mu); margin-top: 2px; }
  .acts { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 10px; }
  .more { align-self: center; font-size: 12.5px; }
  .run { display: flex; align-items: center; gap: 10px; padding: 10px 0; border-top: 1px solid var(--ln); }
  .run:first-of-type { border-top: 0; padding-top: 0; }
  .rn { display: flex; flex-direction: column; min-width: 0; }
  .rn a, .rn b { font-weight: 600; font-size: 14px; color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .rn small { color: var(--mu); font-size: 12px; font-family: var(--font-mono); }
  .run button { margin-left: auto; min-height: 32px; font-weight: 600; }
  .st { display: flex; align-items: center; gap: 8px; }
  .spin { width: 13px; height: 13px; }
</style>
