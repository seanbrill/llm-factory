<script lang="ts">
  // A failed build that still matters (sheet frame f1): what happened, why,
  // and the fix — or that it's already building again.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { post } from "../../lib/api";
  import { sysinfo, toast } from "../../lib/stores";
  import { watchQueue } from "../../lib/build.svelte";
  import { href } from "../../lib/router";
  import { dur, engineName } from "../../lib/format";
  import { compiledOn } from "../../lib/jobs";
  import { explainFailure } from "../../lib/explain";
  import { firstFix, whenText, type FailureItem } from "../../lib/overview";

  let { item, log }: { item: FailureItem; log: string } = $props();

  const j = $derived(item.job);
  const x = $derived(explainFailure(j, log));
  const fix = $derived(x && !item.retry ? firstFix(x, j, $sysinfo) : undefined);
  const retryOn = $derived(item.retry ? compiledOn(item.retry) : "");
  const took = $derived(j.elapsed_ms > 0 ? ` after ${dur(j.elapsed_ms / 1000)}` : "");
  let busy = $state(false);

  async function retry() {
    busy = true;
    try {
      const r = await post<{ position: number; engine: string }>("/api/builds/retry", { id: j.id });
      toast(r.position > 0 ? `${j.name} queued again: ${r.position} ahead on ${engineName(r.engine)}.` : `Building ${j.name} again.`);
      watchQueue();
    } catch (e) {
      toast(`Couldn't queue ${j.name} again: ${(e as Error).message}`, 7000);
    } finally {
      busy = false;
    }
  }
</script>

<div class="failed">
  <Icon name="xmark" size={15} />
  <div class="tx">
    <p>
      <b>{whenText(j.finished_at)}: {j.name} failed{took}.</b>
      {x?.title}
      {#if x?.why}{x.why}{/if}
      {#if item.retry}
        It's building again above{#if retryOn !== item.retry.engine}, compiling on {engineName(retryOn)}{/if}.
      {/if}
    </p>
    {#if fix}
      <div class="fix">
        {#if fix.cmd}
          <span class="sub">{fix.label}:</span>
          <CodeLine cmd={fix.cmd} />
        {:else if fix.to}
          <span class="sub">{fix.label}.</span>
          <a class="btn sm" href={fix.to}>Memory plan</a>
        {:else if fix.action === "retry"}
          {#if fix.label !== "Build it again"}<span class="sub">{fix.label}.</span>{/if}
          <button class="small" onclick={retry} disabled={busy}>
            {#if busy}<span class="spin"></span>{:else}<Icon name="retry" size={14} />{/if}Build it again
          </button>
        {:else if fix.action === "system"}
          <a class="btn sm" href={href("system")}>{fix.label}</a>
        {:else if fix.action === "cleanup" || fix.action === "library"}
          <a class="btn sm" href={href("library")}>{fix.label}</a>
        {:else}
          <span class="sub">{fix.label}.</span>
        {/if}
      </div>
    {/if}
  </div>
  <a class="log" href={href("builds", j.id)}>See the log</a>
</div>

<style>
  .failed {
    margin-top: 10px;
    padding: 12px;
    border-radius: var(--r);
    background: var(--ers);
    display: grid;
    grid-template-columns: 15px minmax(0, 1fr) auto;
    gap: 10px;
    font-size: 13px;
    align-items: start;
  }
  .failed > :global(.ico) { color: var(--er); margin-top: 3px; }
  .tx { min-width: 0; }
  .tx p { color: var(--mu); }
  .tx b { color: var(--ink); font-weight: 600; }
  .fix { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; margin-top: 8px; }
  .fix :global(.code) { align-self: stretch; }
  .log { font-size: 12.5px; white-space: nowrap; }
  .spin { width: 13px; height: 13px; }
</style>
