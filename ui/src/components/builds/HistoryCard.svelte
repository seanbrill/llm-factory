<script lang="ts">
  // Finished builds, newest first (sheet frame f10 "Earlier"): built, failed or
  // canceled, with when and how long. Open one to read its log and, for a
  // failure, why it failed and how to fix it.
  import Icon from "../Icon.svelte";
  import { build } from "../../lib/build.svelte";
  import { outcomeText } from "../../lib/builds";
  import { href } from "../../lib/router";
  import { ago, computeName, engineName, plural } from "../../lib/format";
  import type { BuildJob } from "../../lib/types";

  let { jobs, selected = "", compact = false }: { jobs: BuildJob[]; selected?: string; compact?: boolean } = $props();

  const SHOW = 5; // before "Show older"
  const selIndex = $derived(jobs.findIndex((j) => j.id === selected));
  const all = $derived(build.showHistory || selIndex >= SHOW);
  const shown = $derived(all ? jobs : jobs.slice(0, SHOW));

  const ICON: Record<string, string> = { done: "check", error: "xmark", canceled: "minus" };
  const SAY: Record<string, string> = { done: "Built", error: "Failed", canceled: "Canceled" };
</script>

<section class="card hist" class:compact aria-label="Earlier builds">
  <div class="lh"><b>Earlier</b>{#if jobs.length}<span class="r">{plural(jobs.length, "build")}</span>{/if}</div>
  {#if !jobs.length}
    <p class="empty">Finished builds show up here, with their logs.</p>
  {:else}
    <ol class="jobs">
      {#each shown as j (j.id)}
        <li class="jr" class:sel={selected === j.id} class:er={j.status === "error"} id="job-{j.id}">
          <span class="st {j.status}" aria-hidden="true"><Icon name={ICON[j.status] ?? "minus"} size={11} stroke={3} /></span>
          <div class="jm">
            <a class="nm" href={href("builds", j.id)} aria-current={selected === j.id ? "true" : undefined}>
              <span class="sr">{SAY[j.status] ?? j.status}: </span>{j.name || j.ref}
            </a>
            <div class="m">
              {#if j.kind === "ensemble"}<span class="chip">Ensemble</span>{/if}
              <span class="mt">{engineName(j.engine)} · {computeName(j.compute)} · {outcomeText(j)}</span>
            </div>
          </div>
          <span class="r" title={j.finished_at ? new Date(j.finished_at).toLocaleString() : undefined}>{ago(j.finished_at)}</span>
        </li>
      {/each}
    </ol>
    {#if jobs.length > SHOW && selIndex < SHOW}
      <button class="ghost small more" onclick={() => (build.showHistory = !build.showHistory)}>
        {build.showHistory ? "Show fewer" : `Show ${jobs.length - SHOW} older`}
      </button>
    {/if}
  {/if}
</section>

<style>
  .hist { padding: 16px; min-width: 0; }
  .lh { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; }
  .lh b { font-size: 14px; }
  .lh .r { margin-left: auto; font-size: 12px; color: var(--mu); font-family: var(--font-mono); }
  .empty { padding: 6px 0 2px; }
  .jobs { list-style: none; margin: 0; padding: 0; }
  .jr {
    position: relative;
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr) auto;
    gap: 10px;
    align-items: center;
    padding: 10px 0;
    border-top: 1px solid var(--ln);
  }
  .jr:first-child { border-top-color: transparent; }
  .jm { min-width: 0; }
  .nm {
    display: block;
    font-weight: 600;
    font-size: 13.5px;
    color: var(--ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .nm:hover { text-decoration: none; }
  .nm::after { content: ""; position: absolute; inset: 0; border-radius: 10px; }
  .nm:focus-visible { outline: none; }
  .nm:focus-visible::after { outline: 2px solid var(--pr); outline-offset: -2px; }
  .jr:hover:not(.sel) { background: color-mix(in srgb, var(--p2) 60%, transparent); }
  .m { font-size: 12.5px; color: var(--mu); display: flex; gap: 6px; align-items: center; margin-top: 2px; min-width: 0; }
  .mt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .r { font-size: 12px; color: var(--mu); font-family: var(--font-mono); white-space: nowrap; }
  .st {
    width: 16px;
    height: 16px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--p3);
    color: var(--mu);
  }
  .st.done { background: var(--oks); color: var(--ok); }
  .st.error { background: var(--ers); color: var(--er); }
  .st.canceled { background: var(--was); color: var(--wa); }
  .jr.sel {
    background: var(--p2);
    border-radius: 10px;
    border-top-color: transparent;
    padding: 10px;
    margin: 0 -10px;
    box-shadow: inset 3px 0 0 var(--pr);
  }
  .jr.sel.er { box-shadow: inset 3px 0 0 var(--er); }
  .jr.sel + .jr { border-top-color: transparent; }
  .more { margin-top: 6px; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
  .compact { border-radius: 16px; padding: 14px; }
  .compact .nm { font-size: 14px; }
  .compact .m { font-size: 12px; }
</style>
