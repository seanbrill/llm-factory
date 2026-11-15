<script lang="ts">
  // Builds on the Overview (sheet frame f1): each engine's lane — the running
  // build and what waits behind it — and the failures that still need you.
  import JobRow from "./JobRow.svelte";
  import FailureNote from "./FailureNote.svelte";
  import { build } from "../../lib/build.svelte";
  import { counts, lanes } from "../../lib/jobs";
  import { sysinfo } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { engineName, plural } from "../../lib/format";
  import { engineOf } from "../../lib/system.svelte";
  import type { FailureItem } from "../../lib/overview";

  let {
    failures,
    logs,
    queue,
    queueErr,
  }: { failures: FailureItem[]; logs: Record<string, string>; queue: "loading" | "ok" | "error"; queueErr: string } = $props();

  const c = $derived(counts(build.jobs));
  // Lanes with something in them; the one with a running build first.
  const laneList = $derived(
    Object.entries(lanes(build.jobs))
      .filter(([, js]) => js.length)
      .sort((a, b) => +(b[1][0].status === "running") - +(a[1][0].status === "running")),
  );
  const chip = $derived([c.running ? `${c.running} running` : "", c.queued ? `${c.queued} queued` : ""].filter(Boolean).join(" · "));
  const shown = $derived(failures.slice(0, 2));
</script>

<div class="card builds">
  <div class="hd">
    <h2>Builds</h2>
    {#if c.active}<span class="chip pr">{chip}</span>{/if}
    <div class="sp"><a class="btn sm" href={href("builds")}>Open queue</a></div>
  </div>

  {#if !build.jobs.length && queue === "loading"}
    <p class="sub state"><span class="spin"></span>Reading the build queue…</p>
  {:else if !build.jobs.length && queue === "error"}
    <p class="sub state">Couldn't read the build queue ({queueErr}). The factory may be restarting — this page tries again on its own.</p>
  {:else}
    {#each laneList as [engine, js] (engine)}
      {@const e = engineOf($sysinfo, engine)}
      <p class="lane">
        {engineName(engine)} lane
        {#if e && !e.reachable}<span class="down">· {engineName(engine)} isn't running, so these wait</span>{/if}
      </p>
      {#each js as j (j.id)}<JobRow {j} lane={js} />{/each}
    {/each}
    {#if !c.active}
      <p class="idle">
        Nothing is building. <a href={href("catalog")}>Pick models in the Catalog</a> to build one or several at once.
      </p>
    {/if}
  {/if}

  {#each shown as f (f.job.id)}<FailureNote item={f} log={logs[f.job.id] ?? ""} />{/each}
  {#if failures.length > shown.length}
    <a class="more" href={href("builds")}>{plural(failures.length - shown.length, "more failed build")} on Builds</a>
  {/if}
</div>

<style>
  .builds { min-width: 0; }
  .lane {
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    margin: 6px 0 8px;
  }
  .lane + :global(.job) { padding-top: 12px; }
  .lane ~ .lane { margin-top: 14px; }
  .down { color: var(--er); text-transform: none; letter-spacing: 0; font-size: 12px; }
  .idle { color: var(--mu); font-size: 13px; padding: 4px 0 2px; }
  .state { display: flex; align-items: center; gap: 8px; }
  .more { display: inline-block; margin-top: 10px; font-size: 12.5px; }
</style>
