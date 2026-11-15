<script lang="ts">
  // One engine on the Overview (sheet frame f1): what it is, what it does for
  // this machine, and — when it's too small or down — the fix to copy.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { catalog, resources, sysinfo } from "../../lib/stores";
  import { build } from "../../lib/build.svelte";
  import { href } from "../../lib/router";
  import { computeName, engineName } from "../../lib/format";
  import { DOCKER_MEMORY_PATH, PODMAN_MEMORY_CMD } from "../../lib/explain";
  import { fixIsCommand, lowMemoryText } from "../../lib/overview";
  import {
    engineList,
    engineSystem,
    engineTitle,
    fallbackFor,
    hostMem,
    PODMAN_FIX_GB,
    PODMAN_START_CMD,
    podmanFixNoRoom,
    startScript,
    vmGB,
  } from "../../lib/system.svelte";
  import type { EngineResources } from "../../lib/types";

  let { e }: { e: EngineResources } = $props();

  const si = $derived($sysinfo);
  const down = $derived(!e.reachable);
  const low = $derived(e.reachable && !!e.low_memory);
  const podman = $derived(e.engine === "podman");

  // The factory runs in Docker, so /api/resources measures Docker's machine.
  const usage = $derived(
    e.engine === "docker" && e.reachable && $resources?.global_ram && $resources.total_ram_gb > 0 ? $resources : null,
  );
  const pct = $derived(usage ? Math.min(100, (usage.used_ram_gb / usage.total_ram_gb) * 100) : 0);

  const sub = $derived.by(() => {
    const sys = engineSystem(e.engine, si);
    if (sys) return sys;
    if (down) return podman ? "Podman machine · not running" : "Not answering";
    if (e.gpu === "vulkan" && podman && e.gpu_from === "host") return "libkrun machine · Vulkan GPU";
    return `${e.cpus} CPUs · ${e.gpu ? computeName(e.gpu) + " GPU" : "no GPU"}`;
  });
  // A GPU the GPU check found (Docker VMM, a krunkit VM) rather than the start script.
  const gpuNote = $derived(e.gpu_experimental ? "Found by the GPU check; experimental with the factory's images." : "");

  // Another engine this one compiles GPU images for (the low-memory fallback).
  const compilesFor = $derived(
    engineList(si).find((o) => o.engine !== e.engine && o.reachable && o.low_memory && fallbackFor(si, o, o.gpu || "vulkan")?.engine === e.engine),
  );

  const fixCmd = $derived(fixIsCommand(e.fix) ? e.fix : podman ? PODMAN_MEMORY_CMD : "");
  // The 12 GB fix doesn't fit beside Docker on this Mac: plan both instead.
  const noRoom = $derived(podman && podmanFixNoRoom(si));
</script>

<div class="card mc" class:warn={low} class:down>
  <div class="t">
    <span class="ico"><Icon name={podman ? "chip" : "library"} size={18} /></span>
    <div class="tt">
      <b>{engineTitle(e.engine, si)}</b>
      <div class="sub">{sub}</div>
    </div>
    {#if down}
      <span class="chip er">Off</span>
    {:else if low}
      <span class="chip wa">{vmGB(e.mem_gb)} GB</span>
    {:else}
      <span class="chip ok">Ready</span>
    {/if}
  </div>

  {#if usage}
    <div class="meter">
      <span>Memory in use</span>
      <span class="v">{usage.used_ram_gb.toFixed(1)} / {usage.total_ram_gb.toFixed(1)} GB</span>
      <div class="bar" class:ok={pct < 70} class:wa={pct >= 70 && pct < 90} class:er={pct >= 90}>
        <i style="width:{pct.toFixed(1)}%"></i>
      </div>
    </div>
  {/if}

  {#if down}
    <div class="fix">
      <Icon name="errc" size={15} cls="er" />
      <span>
        {#if podman}
          The Podman machine isn't running{e.gpu ? ", so GPU models can't run" : ""}. Start it, then restart the factory
          (<code>{startScript(si)}</code>) so it reconnects.
        {:else}
          Docker isn't answering. Start Docker Desktop, then start the factory again.
        {/if}
      </span>
    </div>
    {#if podman}<CodeLine cmd={PODMAN_START_CMD} />{/if}
  {:else if low}
    <div class="fix">
      <Icon name="warn" size={15} cls="wa" />
      <span>{lowMemoryText(si, e, build.jobs, $catalog)}</span>
    </div>
    {#if noRoom}
      <p class="hint">Giving it {PODMAN_FIX_GB} GB means shrinking Docker first: both have to fit in this Mac's {hostMem.gb} GB.</p>
    {:else if fixCmd}
      <CodeLine cmd={fixCmd} />
    {:else}
      <p class="hint">Give it more memory: {e.fix || DOCKER_MEMORY_PATH}.</p>
    {/if}
    <div class="acts">
      <a class="btn sm" class:pri={noRoom} href={href("system", "memory")}>Memory plan</a>
      {#if fixCmd && !noRoom}<span class="hint">Run it, then restart the factory so it reconnects.</span>{/if}
    </div>
  {:else}
    <div class="acts">
      {#if !usage}<span class="chip">{vmGB(e.mem_gb)} GB</span>{/if}
      <span class="chip">{e.cpus} CPUs</span>
      <span class="chip">{e.gpu ? "Runs GPU models" : "Builds for CPU"}</span>
      {#if compilesFor}<span class="chip pr">Compiles for {engineName(compilesFor.engine)}</span>{/if}
      <span class={e.gpu ? "chip gpu" : "chip"} title={gpuNote || undefined}>{e.gpu ? computeName(e.gpu) : "No GPU"}{e.gpu_experimental ? " · experimental" : ""}</span>
    </div>
  {/if}
</div>

<style>
  .mc { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .mc.warn { border-color: color-mix(in srgb, var(--wa) 40%, var(--ln)); }
  .mc.down { border-color: color-mix(in srgb, var(--er) 35%, var(--ln)); }
  .t { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .tt { min-width: 0; }
  .tt .sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .t b { font-size: 15px; }
  .t .chip { margin-left: auto; }
  .ico {
    width: 34px;
    height: 34px;
    border-radius: 9px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .meter {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 6px 10px;
    align-items: center;
    font-size: 12.5px;
    color: var(--mu);
  }
  .meter .v { justify-self: end; font-family: var(--font-mono); color: var(--ink); font-size: 12px; }
  .meter .bar { grid-column: 1 / 3; }
  .fix { display: flex; gap: 10px; align-items: flex-start; font-size: 13px; color: var(--ink); }
  .fix :global(.ico) { margin-top: 2px; }
  .fix :global(.ico.wa) { color: var(--wa); }
  .fix :global(.ico.er) { color: var(--er); }
  .fix code { font-size: 12px; color: var(--mu); }
  .hint { font-size: 12.5px; }
  .acts { display: flex; gap: 8px 10px; margin-top: 2px; flex-wrap: wrap; align-items: center; }
</style>
