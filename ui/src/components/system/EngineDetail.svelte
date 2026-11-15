<script lang="ts">
  // One engine on System (sheet frame f13): its machine (memory, CPUs, how many
  // compile jobs it runs), whether containers get a GPU there and why — from
  // /api/sysinfo, including the factory's saved GPU check (when it ran, and
  // whether the engine's machine changed since) — and how to change its
  // memory, which is done on the host.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { sysinfo } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { ago, computeName, engineName } from "../../lib/format";
  import { DOCKER_MEMORY_PATH } from "../../lib/explain";
  import {
    checkFor,
    checkGPU,
    compileFloor,
    compileJobs,
    engineTitle,
    engineVer,
    everChecked,
    fallbackFor,
    gpuCheck,
    hostMem,
    isMac,
    JOB_GIB,
    PODMAN_FIX_GB,
    PODMAN_START_CMD,
    podmanFixNoRoom,
    podmanMemoryCmd,
    renderAccessText,
    startScript,
    vmGB,
  } from "../../lib/system.svelte";
  import type { EngineResources } from "../../lib/types";

  let { e, checkHere = false }: { e: EngineResources; checkHere?: boolean } = $props();

  const si = $derived($sysinfo);
  const mac = $derived(isMac(si));
  const podman = $derived(e.engine === "podman");
  const down = $derived(!e.reachable);
  const low = $derived(e.reachable && !!e.low_memory);
  const check = $derived(checkFor(si, e.engine));
  const stale = $derived(!!check?.stale);
  // "Its kernel changed (7.0.14-linuxkit → 7.1.2-linuxkit)"
  const staleWhy = $derived.by(() => {
    const r = check?.stale_reason || "its machine changed";
    return r.charAt(0).toUpperCase() + r.slice(1);
  });
  const ver = $derived(engineVer.map?.[e.engine] || "");

  const title = $derived(engineTitle(e.engine, si));
  const sub = $derived(
    [
      ver ? (podman ? ver : `Engine ${ver}`) : "",
      podman ? (e.gpu === "vulkan" && e.gpu_from === "host" ? "libkrun machine" : "Podman machine") : mac ? "Linux VM" : "",
    ]
      .filter(Boolean)
      .join(" · ") || "Container engine",
  );

  // Compile jobs: what the Dockerfiles' BUILD_JOBS sizing allows on this machine.
  const cpuJobs = $derived(compileJobs(si, e, false));
  const gpuJobs = $derived(compileJobs(si, e, true));
  const jobsText = $derived.by(() => {
    if (down) return "—";
    if (!cpuJobs && !gpuJobs) return "too small";
    // A GPU engine compiles GPU images; say what those get.
    if (e.gpu) return gpuJobs ? `up to ${gpuJobs} at once` : `up to ${cpuJobs}, CPU only`;
    return `up to ${cpuJobs} at once`;
  });
  const jobsTitle = $derived(
    !down && !cpuJobs && !gpuJobs
      ? `Compiling needs ${compileFloor(si, "cpu")} GB here (${compileFloor(si, e.gpu || "vulkan")} GB for GPU), so builds compile on the other engine.`
      : `One job per ${JOB_GIB.cpu} GB of free memory (${JOB_GIB.gpu} GB for GPU images), at most one per CPU` +
        (e.gpu && gpuJobs && cpuJobs !== gpuJobs ? `: up to ${gpuJobs} for GPU images, ${cpuJobs} for CPU ones` : "") +
        ". A busy engine runs fewer.",
  );

  // GPU: what reaches containers here, and why. The server decides (e.gpu): the
  // start script's GPU, or one the saved GPU check found while the engine's
  // machine is still the one it checked.
  const gpuYes = $derived(!!e.gpu);
  const gpuTitle = $derived.by(() => {
    if (check && !check.reachable) return "Couldn't check: it isn't running";
    if (e.gpu) {
      return `GPU: ${computeName(e.gpu)}${mac && e.gpu === "vulkan" ? " on this Mac's GPU" : ""}${e.gpu_experimental ? " (experimental)" : ""}`;
    }
    if (stale && check?.dri) return "GPU unknown until it's checked again";
    if (check?.source === "run") return "No GPU device: a Vulkan run found none";
    if (check?.dri) return "A GPU device reaches containers, but models can't use it";
    return "No GPU for models";
  });
  const gpuText = $derived.by(() => {
    if (stale && check?.dri && !e.gpu) {
      return `The GPU check found a GPU device here ${ago(check.checked_at)}, but ${check.stale_reason || "its machine changed"}, so GPU builds don't come here until it's checked again.`;
    }
    if (check) return check.verdict;
    const when = down ? " while the machine runs" : "";
    if (e.gpu === "vulkan") return mac ? `libkrun passes the Mac's GPU into containers as Vulkan${when}.` : `Containers here get the GPU through Vulkan${when}.`;
    if (e.gpu === "cuda") return `Containers here get the NVIDIA GPU through CUDA${when}.`;
    if (podman) return "This Podman machine doesn't pass a GPU through, so containers run on the CPU.";
    return mac
      ? "Docker Desktop's default VM (Apple Virtualization) gives containers no GPU, so models here run on the CPU. Its Docker VMM may (experimental): switch to it, then check GPU access."
      : "Containers on this engine run on the CPU.";
  });
  // When the check ran, and what it means for the GPU here.
  const when = $derived.by(() => {
    if (!check?.checked_at) return "";
    const t = ago(check.checked_at);
    if (check.source === "run") return `A Vulkan run found no GPU device here ${t}.`;
    if (e.gpu_from === "probe") return `${engineName(e.engine)}'s GPU comes from the GPU check, ${t}.`;
    return `Checked ${t}.`;
  });
  const access = $derived(check?.dri ? renderAccessText(check) : "");
  const floorGPU = $derived(compileFloor(si, e.gpu || "vulkan"));
  const fb = $derived(low ? fallbackFor(si, e, e.gpu || "vulkan") : undefined);

  // Raising Podman to the fix's 12 GB only works when Docker leaves room for it.
  const noRoom = $derived(podman && podmanFixNoRoom(si));

  let showMem = $state(false);
</script>

<div class="card ecard">
  <div class="eh">
    <span class="ico"><Icon name={podman ? "chip" : "library"} size={18} /></span>
    <span class="nm"><h3>{title}</h3><small>{sub}</small></span>
    {#if down}<span class="chip er">Not running</span>{:else}<span class="chip ok">Running</span>{/if}
  </div>

  <div class="kv">
    <span>{podman ? "Memory" : "Memory limit"}</span>
    <span class:wa={low}>{down ? "—" : `${vmGB(e.mem_gb)} GB`}</span>
    <span>CPUs</span>
    <span>{down ? "—" : e.cpus}</span>
    <span title={jobsTitle}>Compile jobs</span>
    <span class:wa={!down && !cpuJobs && !gpuJobs} title={jobsTitle}>{jobsText}</span>
  </div>

  <div class="gpu" class:yes={gpuYes} class:no={!gpuYes}>
    <Icon name={gpuYes ? "bolt" : "info"} size={16} />
    <span class="gt">
      <b>{gpuTitle}</b>
      {gpuText}
      {#if low && e.gpu}{" Needs more memory before it's useful."}{/if}
      {#if check?.reachable}
        <span class="facts">
          {#if check.system}<span>{check.system}</span>{/if}
          <span><code>/dev/dri</code>: {check.dri_devices?.length ? check.dri_devices.join(", ") : check.dri_error || "none"}</span>
          {#if access}<span>Render node: {access}</span>{/if}
          {#if check.cdi_devices?.length}
            <span>CDI devices: {check.cdi_devices.join(", ")}{#if check.webgpu}{" (WebGPU remoting, experimental)"}{/if}</span>
          {/if}
        </span>
      {/if}
      {#if when}<small class="when">{when}</small>{/if}
    </span>
  </div>

  {#if stale && check}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span>
        <b>{title} changed since the GPU check.</b>
        <p>
          {staleWhy}.
          {check.dri ? "Check GPU access again so the factory knows whether its GPU still works." : "Check GPU access again to see whether it gives containers a GPU now."}
        </p>
        <span class="fixes">
          <button class="small" onclick={checkGPU} disabled={gpuCheck.running}>
            {#if gpuCheck.running}<span class="spin"></span>Checking…{:else}<Icon name="retry" size={14} />Check GPU access again{/if}
          </button>
        </span>
      </span>
    </div>
  {/if}

  {#if down}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>{podman ? "The Podman machine isn't running." : "Docker isn't answering."}</b>
        <p>
          {#if podman}Start it, then restart the factory (<code>{startScript(si)}</code>) so it reconnects:{:else}Start Docker Desktop, then start the factory again.{/if}
        </p>
      </span>
    </div>
    {#if podman}<CodeLine cmd={PODMAN_START_CMD} />{/if}
  {:else if low}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span>
        <b>Too little memory to compile for GPU (it needs {floorGPU} GB).</b>
        <p>
          {fb ? `GPU builds compile on ${engineName(fb.engine)} and move here when done.` : "Neither engine can compile GPU images now."}
          {` Only models that fit in ${vmGB(e.mem_gb)} GB run on its GPU.`}
          {#if !podman}
            {` Raise it: ${e.fix || DOCKER_MEMORY_PATH}.`}
          {:else if noRoom}
            {` Giving it ${PODMAN_FIX_GB} GB means shrinking Docker first: both have to fit in this Mac's ${hostMem.gb} GB.`}
          {:else}
            {` Give it ${PODMAN_FIX_GB} GB, then restart the factory so it reconnects:`}
          {/if}
        </p>
      </span>
    </div>
    {#if podman && !noRoom}<CodeLine cmd={podmanMemoryCmd(PODMAN_FIX_GB)} />{/if}
  {/if}

  <div class="eacts">
    {#if checkHere && !stale}
      <button class="small" onclick={checkGPU} disabled={gpuCheck.running}>
        {#if gpuCheck.running}<span class="spin"></span>Checking…{:else}<Icon name="chip" size={14} />{everChecked(si) ? "Check GPU access again" : "Check GPU access"}{/if}
      </button>
    {/if}
    {#if podman}
      {#if !down}<a class="btn sm" href={href("system", "memory")}>{low && noRoom ? "Plan the memory" : "Change memory"}</a>{/if}
    {:else if !down}
      <button class="small" onclick={() => (showMem = !showMem)} aria-expanded={showMem}>
        {mac ? "Change memory in Docker Desktop" : "Change Docker's memory"}
        <Icon name={showMem ? "chevronUp" : "chevronDown"} size={14} />
      </button>
    {/if}
  </div>
  {#if checkHere && !everChecked(si) && !gpuCheck.running}
    <p class="note">Starts a throwaway container on each engine with <code>--device /dev/dri</code>. The first check pulls alpine, a few MB.</p>
  {/if}
  {#if showMem}
    <p class="note">
      {DOCKER_MEMORY_PATH}, then <b>Apply &amp; restart</b>. Restarting Docker stops the factory: start it again with
      <code>{startScript(si)}</code>.
    </p>
  {/if}
  {#if (checkHere || stale) && gpuCheck.error}
    <p class="err">The GPU check didn't finish: {gpuCheck.error}. Check that the engines are running, then try again.</p>
  {/if}
</div>

<style>
  .ecard { display: flex; flex-direction: column; min-width: 0; }
  .eh { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .eh .ico {
    width: 36px;
    height: 36px;
    border-radius: 10px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .nm { display: flex; flex-direction: column; min-width: 0; }
  .nm h3 { font-family: var(--font-body); font-size: 15px; font-weight: 600; margin: 0; letter-spacing: 0; }
  .nm small { color: var(--mu); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .eh .chip { margin-left: auto; }
  .kv { display: grid; grid-template-columns: auto 1fr; gap: 7px 14px; margin-top: 14px; font-size: 13px; }
  .kv > span:nth-child(odd) { color: var(--mu); }
  .kv > span:nth-child(even) { text-align: right; font-family: var(--font-mono); font-size: 12.5px; }
  .kv .wa { color: var(--wa); }
  .gpu {
    margin-top: 14px;
    padding: 12px;
    border-radius: 11px;
    font-size: 12.5px;
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: 9px;
  }
  .gpu > :global(.ico) { margin-top: 1px; }
  .gt { min-width: 0; overflow-wrap: anywhere; }
  .gt b { display: block; font-size: 13px; margin-bottom: 1px; }
  .gpu.no { background: var(--p2); color: var(--mu); }
  .gpu.no b { color: var(--ink); }
  .gpu.yes { background: var(--gpus); color: color-mix(in srgb, var(--ink) 80%, var(--cv)); }
  .gpu.yes b,
  .gpu.yes > :global(.ico) { color: var(--gpu); }
  .facts { display: flex; flex-direction: column; gap: 2px; margin-top: 6px; font-size: 12px; }
  .facts code { font-size: 11.5px; }
  .when { display: block; margin-top: 6px; color: var(--fa); font-size: 11.5px; }
  .callout { margin-top: 12px; }
  .callout code { font-size: 11.5px; }
  .fixes { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 8px; }
  .ecard > :global(.code) { margin-top: 8px; }
  .eacts { display: flex; gap: 8px; margin-top: 12px; flex-wrap: wrap; }
  .note { margin-top: 8px; font-size: 12px; color: var(--fa); }
  .note b { color: var(--mu); font-weight: 600; }
  .note code { font-size: 11.5px; white-space: nowrap; }
  .err { margin-top: 10px; font-size: 12.5px; color: var(--er); }
  .spin { width: 13px; height: 13px; }
</style>
