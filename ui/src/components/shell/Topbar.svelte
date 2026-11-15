<script lang="ts">
  // Top bar (sheet frame f1): page title, the ⌘K trigger, what's building, and
  // one pill per engine with its memory (warning when it's too small to compile
  // for its GPU). On a phone (frame f16) it becomes the brand header plus a row
  // of engine pills.
  import Icon from "../Icon.svelte";
  import Mark from "../Mark.svelte";
  import { route, pageDef, href } from "../../lib/router";
  import { sysinfo } from "../../lib/stores";
  import { build } from "../../lib/build.svelte";
  import { counts, progress } from "../../lib/jobs";
  import { gb, engineName } from "../../lib/format";
  import { openPalette } from "../../lib/ui.svelte";
  import type { EngineResources } from "../../lib/types";

  const def = $derived(pageDef($route));
  const c = $derived(counts(build.jobs));

  // Average progress of running builds, for the ring (null parts count as half).
  const ring = $derived.by(() => {
    const running = build.jobs.filter((j) => j.status === "running");
    if (!running.length) return 0;
    const sum = running.reduce((a, j) => a + (progress(j) ?? 0.5), 0);
    return sum / running.length;
  });
  const CIRC = 2 * Math.PI * 7.5;

  // Engines worth showing: reachable ones, and a GPU engine even when it's down
  // (that it's down is the news).
  const engines = $derived(($sysinfo?.engines ?? []).filter((e) => e.reachable || e.gpu));

  function engTitle(e: EngineResources): string {
    const name = e.engine === "podman" ? "Podman machine" : "Docker";
    if (!e.reachable) return `${name} isn't running. Start it, then restart the factory so it reconnects.`;
    let t = `${name}: ${gb(e.mem_gb)} memory, ${e.cpus} CPUs, ${e.gpu ? e.gpu + " GPU" : "no GPU"}.`;
    if (e.low_memory) {
      const need = $sysinfo?.compile_min_gb?.[e.gpu] ?? 6;
      t += ` Too little memory to compile GPU images (needs ${need} GB): GPU builds compile on the other engine and move here when done. Fix: ${e.fix}`;
    }
    return t;
  }

  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
</script>

<header class="top">
  <!-- Phone: brand header -->
  <a class="mbrand" href={href("overview")}><Mark size={28} /><b>LLM Factory</b></a>

  <h1>{def.title}</h1>

  <button class="cmd" onclick={() => openPalette()} aria-keyshortcuts={isMac ? "Meta+K" : "Control+K"}>
    <Icon name="find" size={15} />
    <span class="ph">Search models or run a command</span>
    <span class="kbd">{isMac ? "⌘K" : "Ctrl K"}</span>
  </button>

  {#if c.active}
    <a class="act" href={href("builds")} title="Open the build queue">
      <svg class="ring" viewBox="0 0 20 20" aria-hidden="true">
        <circle cx="10" cy="10" r="7.5" />
        <circle class="v" cx="10" cy="10" r="7.5" stroke-dasharray="{(ring * CIRC).toFixed(1)} {CIRC.toFixed(1)}" />
      </svg>
      <span>{c.running ? `Building ${c.running}` : "Queued"}{c.queued ? ` · ${c.queued} queued` : ""}</span>
    </a>
  {:else if c.failed}
    <a class="act er" href={href("builds")} title="See why">
      <Icon name="errc" size={16} /><span>{c.failed} failed</span>
    </a>
  {/if}

  <div class="engs">
    {#each engines as e (e.engine)}
      <a class="eng" class:warn={e.low_memory} class:down={!e.reachable} href={href("system")} title={engTitle(e)}>
        {#if !e.reachable}
          <span class="dot off"></span>{engineName(e.engine)} <b>off</b>
        {:else if e.low_memory}
          <Icon name="warn" size={15} />{engineName(e.engine)} <b>{gb(e.mem_gb, 0)}</b>
        {:else}
          <span class="dot"></span>{engineName(e.engine)} <b>{gb(e.mem_gb, 0)}</b>
        {/if}
      </a>
    {/each}
  </div>

  <button class="msearch btn icon" onclick={() => openPalette()} aria-label="Search">
    <Icon name="find" size={18} />
  </button>
</header>

<style>
  .top {
    height: 62px;
    flex: none;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 28px;
    border-bottom: 1px solid var(--ln);
    background: var(--cv);
    min-width: 0;
  }
  h1 {
    font-size: 21px;
    margin-right: auto;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .cmd {
    width: 330px;
    flex: 0 1 330px;
    min-width: 0;
    height: 36px;
    min-height: 36px;
    justify-content: flex-start;
    gap: 8px;
    padding: 0 10px 0 12px;
    background: var(--p1);
    color: var(--fa);
    font-weight: 400;
  }
  .cmd:hover { color: var(--mu); background: var(--p2); }
  .ph { overflow: hidden; text-overflow: ellipsis; }
  .cmd .kbd { margin-left: auto; }
  .act {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 36px;
    padding: 0 12px;
    border-radius: var(--r);
    background: var(--prs);
    color: var(--ink);
    font-size: 13px;
    font-weight: 600;
    white-space: nowrap;
    text-decoration: none;
  }
  .act.er { background: var(--ers); }
  .act.er :global(.ico) { color: var(--er); }
  .ring { width: 18px; height: 18px; transform: rotate(-90deg); flex: none; }
  .ring circle { fill: none; stroke: var(--ln2); stroke-width: 3; }
  .ring .v { stroke: var(--pr); stroke-linecap: round; transition: stroke-dasharray 0.6s ease; }
  .engs { display: flex; gap: 8px; }
  .eng { height: 36px; text-decoration: none; }
  .eng:hover { border-color: var(--ln2); color: var(--ink); }
  .mbrand, .msearch { display: none; }

  @media (max-width: 1100px) {
    .top { padding: 0 20px; }
    .cmd { width: 44px; flex: none; justify-content: center; padding: 0; }
    .cmd .ph, .cmd .kbd { display: none; }
  }

  /* Phone (f16): brand header, then the engines on their own row. */
  @media (max-width: 760px) {
    .top {
      height: auto;
      flex-wrap: wrap;
      padding: 14px 16px 12px;
      gap: 10px;
      border-bottom: 0;
    }
    .mbrand {
      display: flex;
      align-items: center;
      gap: 10px;
      color: var(--ink);
      text-decoration: none;
      margin-right: auto;
    }
    .mbrand b { font-family: var(--font-display); font-size: 18px; font-weight: 700; letter-spacing: -0.01em; }
    h1, .cmd { display: none; }
    .msearch { display: inline-flex; width: 38px; min-height: 38px; height: 38px; border-radius: 12px; order: 2; }
    .act { display: none; } /* the Builds tab carries the count on a phone */
    .engs { order: 4; flex-basis: 100%; }
    .eng { flex: 1; justify-content: flex-start; border-radius: 11px; }
    .eng b { margin-left: auto; }
  }
</style>
