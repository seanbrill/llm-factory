<script lang="ts">
  // The ways to fix a failed build (sheet frames f10, f17): cards on a desktop,
  // the recommended one first; buttons on a phone. Commands copy, actions run
  // (build again, clean up, open a page), and a retry that's already queued
  // says so instead of offering a second one.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { build, retryJob } from "../../lib/build.svelte";
  import { fixCards, laterBuilds, placeText, waitText, type FixCard } from "../../lib/builds";
  import { lanes } from "../../lib/jobs";
  import { post, type ApiError } from "../../lib/api";
  import { go, href } from "../../lib/router";
  import { sysinfo, toast } from "../../lib/stores";
  import { live, watch, refresh } from "../../lib/live.svelte";
  import { copyText } from "../../lib/ui.svelte";
  import { addToPlan, inPlan, updatePlanItem } from "../../lib/plan.svelte";
  import { gb, plural } from "../../lib/format";
  import type { Explanation } from "../../lib/explain";
  import type { BuildJob, Model } from "../../lib/types";

  let {
    job,
    ex,
    model,
    compact = false,
  }: { job: BuildJob; ex: Explanation; model?: Model; compact?: boolean } = $props();

  const cards = $derived(fixCards(job, ex, { si: $sysinfo, model }));
  // A retry (or any later build of this image) that's queued or running now.
  const again = $derived(laterBuilds(job, build.jobs).find((o) => o.active) ?? null);
  const againText = $derived.by(() => {
    if (!again) return "";
    if (again.status === "running") return "Queued · building now";
    const lane = lanes(build.jobs)[again.engine] ?? [];
    const qi = lane.filter((x) => x.status === "queued").findIndex((x) => x.id === again.id);
    const w = waitText(again);
    return `Queued · ${qi === 0 ? "next" : placeText(again, qi) + " in line"}${w ? `, ${w}` : ""}`;
  });

  const hasCleanup = $derived(cards.some((c) => c.kind === "cleanup"));
  $effect(() => {
    if (hasCleanup) return watch("storage");
  });
  const leftovers = $derived(live.storage?.leftovers);
  const building = $derived(build.jobs.some((j) => j.status === "running"));

  let busy = $state("");
  let cleanupMsg = $state("");

  async function retry() {
    busy = "retry";
    const r = await retryJob(job);
    busy = "";
    if (r) go("builds", r.id);
  }

  async function cleanup() {
    busy = "cleanup";
    cleanupMsg = "";
    try {
      const r = await post<{ removed: string[]; freed_gb: number }>("/api/storage/cleanup", {});
      cleanupMsg = r.removed.length
        ? `Removed ${plural(r.removed.length, "leftover build folder")}, freed ${gb(r.freed_gb)}.`
        : "Nothing to clean up.";
      refresh("storage");
    } catch (e) {
      const err = e as ApiError;
      cleanupMsg = err.status === 409
        ? "A build is running and uses that folder. Clean up after it finishes."
        : `Couldn't clean up: ${err.message}`;
    } finally {
      busy = "";
    }
  }

  function planCPU() {
    if (!model) return;
    const patch = { compute: "cpu", engine: "docker" };
    if (inPlan(model.id)) updatePlanItem(model.id, patch);
    else addToPlan(model, $sysinfo, patch);
    toast(`${model.name} is in the build plan for CPU on Docker.`);
    go("plan");
  }

  async function copyCmd(c: FixCard) {
    if (!c.cmd) return;
    toast((await copyText(c.cmd)) ? "Copied. Run it in a terminal on this Mac." : c.cmd, 6000);
  }

  function run(c: FixCard) {
    if (c.kind === "retry") return retry();
    if (c.kind === "cleanup") return cleanup();
    if (c.kind === "system") return go("system");
    if (c.kind === "library") return go("library");
    if (c.kind === "cpu") return planCPU();
    if (c.kind === "cmd") return copyCmd(c);
  }

  const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);
</script>

{#if cards.length}
  {#if compact}
    <div class="fixb">
      {#each cards as c (c.kind + c.title)}
        {#if c.kind === "note"}
          <p class="note"><b>{c.title}</b>{c.desc ? ` ${c.desc}` : ""}</p>
        {:else if c.kind === "retry" && again}
          <a class="btn big" class:pri={c.rec} href={href("builds", again.id)}>
            {c.title}<small>· {again.status === "running" ? "building now" : "queued"}</small>
          </a>
        {:else}
          <button class="big" class:primary={c.rec} onclick={() => run(c)} disabled={busy !== "" || (c.kind === "cleanup" && building)}>
            {#if busy === c.kind}<span class="spin"></span>{/if}
            {c.kind === "cmd" ? `Copy: ${lower(c.title)}` : c.title}
          </button>
        {/if}
      {/each}
      {#if cleanupMsg}<p class="msg" role="status">{cleanupMsg}</p>{/if}
    </div>
  {:else}
    <div class="fixes" class:one={cards.length === 1}>
      {#each cards as c (c.kind + c.title)}
        <div class="fx" class:rec={c.rec}>
          <span class="k">{c.kicker}</span>
          <span class="t">{c.title}</span>
          {#if c.desc}<span class="d">{c.desc}</span>{/if}
          {#if c.kind === "cleanup"}
            <span class="d">
              {#if leftovers && leftovers.count}{plural(leftovers.count, "leftover build folder")} take {gb(leftovers.gb)}.{:else if building}Leftover build folders can be removed once no build is running.{:else}Removes leftover build folders.{/if}
            </span>
          {/if}
          <div class="act">
            {#if c.kind === "cmd" && c.cmd}
              <CodeLine cmd={c.cmd} />
            {:else if c.kind === "retry" && again}
              <a class="chip ok" href={href("builds", again.id)}>{againText}</a>
            {:else if c.kind === "retry"}
              <button class="small" class:primary={c.rec} onclick={retry} disabled={busy !== ""}>
                {#if busy === "retry"}<span class="spin"></span>{:else}<Icon name="retry" size={14} />{/if}Build again
              </button>
            {:else if c.kind === "cleanup"}
              <button class="small" onclick={cleanup} disabled={busy !== "" || building} title={building ? "Waits until no build is running" : undefined}>
                {#if busy === "cleanup"}<span class="spin"></span>{/if}Clean up
              </button>
            {:else if c.kind === "system"}
              <a class="btn sm" href={href("system")}>Open System</a>
            {:else if c.kind === "library"}
              <a class="btn sm" href={href("library")}>Open Library</a>
            {:else if c.kind === "cpu"}
              <button class="small" onclick={planCPU}>Plan a CPU build</button>
            {/if}
          </div>
          {#if c.kind === "cleanup" && cleanupMsg}<p class="msg" role="status">{cleanupMsg}</p>{/if}
        </div>
      {/each}
    </div>
  {/if}
{/if}

<style>
  .fixes {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
    gap: 10px;
  }
  .fixes.one { grid-template-columns: minmax(0, 1fr); }
  .fx {
    border: 1px solid var(--ln2);
    border-radius: 12px;
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .fx.rec {
    border-color: var(--pr);
    background: color-mix(in srgb, var(--pr) 7%, var(--p1));
  }
  .k {
    font-size: 11px;
    letter-spacing: 0.07em;
    text-transform: uppercase;
    color: var(--mu);
    font-weight: 700;
  }
  .rec .k { color: var(--pr); }
  .t { font-weight: 600; font-size: 14px; line-height: 1.35; }
  .d { font-size: 12.5px; color: var(--mu); }
  .act { margin-top: auto; padding-top: 4px; display: flex; min-width: 0; }
  .act :global(.code) { flex: 1; font-size: 11.5px; align-items: flex-start; padding: 8px 6px 8px 10px; }
  .act :global(.code > span) { white-space: normal; overflow-wrap: anywhere; line-height: 1.5; }
  .act .chip { text-decoration: none; }
  .act .chip:hover { text-decoration: underline; }
  .msg { font-size: 12.5px; color: var(--mu); }

  /* Phone: one button per fix, the recommended one filled. */
  .fixb { display: flex; flex-direction: column; gap: 8px; }
  .big {
    width: 100%;
    min-height: 42px;
    border-radius: 12px;
    font-size: 13.5px;
    font-weight: 600;
    white-space: normal;
    text-align: center;
  }
  .big small { font-weight: 500; opacity: 0.75; margin-left: 4px; }
  .note { font-size: 13px; color: var(--mu); padding: 2px 2px; }
  .note b { color: var(--ink); font-weight: 600; }
</style>
