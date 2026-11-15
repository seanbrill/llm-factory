<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import ResourceBar from "../components/ResourceBar.svelte";
  import { post } from "../lib/api";
  import { loadResources, loadSysInfo, sysinfo, toast } from "../lib/stores";
  import { engineBadge } from "../lib/util";
  import type { GPUProbe } from "../lib/types";

  let reclaiming = $state(false);
  let lastFreed = $state<number | null>(null);

  // Engine machines + the GPU access check (POST /api/gpu/probe starts a tiny
  // throwaway container per engine, so it only runs when asked).
  const engines = $derived($sysinfo?.engines ?? []);
  let probing = $state(false);
  let probes = $state<GPUProbe[] | null>(null);
  let probeErr = $state("");
  async function checkGPU() {
    probing = true;
    probeErr = "";
    try {
      probes = (await post<{ engines: GPUProbe[] }>("/api/gpu/probe", {})).engines ?? [];
      loadSysInfo();
    } catch (e) {
      probeErr = (e as Error).message;
    } finally {
      probing = false;
    }
  }

  async function reclaim() {
    reclaiming = true;
    try {
      const r = await post<{ freed_gb: number }>("/api/system/reclaim", {});
      lastFreed = r.freed_gb;
      toast(r.freed_gb > 0.1 ? `Freed ~${r.freed_gb.toFixed(1)} GB back to the host.` : "Cache dropped (little to reclaim right now).");
      loadResources();
    } catch (e) {
      toast("Reclaim failed: " + (e as Error).message);
    } finally {
      reclaiming = false;
    }
  }
</script>

<div class="card">
  <h2>System resources</h2>
  <ResourceBar />
</div>

<div class="card">
  <h2>Engines &amp; GPU access</h2>
  <p class="hint">
    Docker Desktop and the Podman machine are separate Linux VMs, each with its own memory, CPUs and GPU access.
    On a Mac, GPU (Vulkan) models run on a Podman <b>libkrun</b> machine; Docker Desktop under Apple Virtualization
    has no GPU device.
  </p>
  {#if engines.length}
    <div class="engines">
      {#each engines as e (e.engine)}
        <div class="eng" class:low={e.low_memory}>
          <div class="eng-head">
            <b>{engineBadge(e.engine)}</b>
            <span class="eng-spec">
              {#if e.reachable}{e.mem_gb.toFixed(1)} GB · {e.cpus} CPUs · {e.gpu ? `${e.gpu} GPU` : "no GPU"}{:else}not reachable{/if}
            </span>
          </div>
          {#if e.low_memory}
            <p class="note warn">
              Too little memory to compile GPU images (they need {$sysinfo?.compile_min_gb?.[e.gpu] ?? 6} GB): GPU builds
              compile on the other engine and are transferred here, and only models that fit in {e.mem_gb.toFixed(1)} GB
              can run on its GPU. To give it more memory:
            </p>
            <pre class="cfg">{e.fix}</pre>
            {#if e.engine === "podman"}<p class="note">Then restart the factory (<code>scripts/&lt;os&gt;/start.sh</code>) so it reconnects.</p>{/if}
          {/if}
        </div>
      {/each}
    </div>
  {/if}
  <div class="row">
    <button class="primary" onclick={checkGPU} disabled={probing}>
      {#if probing}<span class="spin"></span> Checking…{:else}<Icon name="gpu" size={15} /> Check GPU access{/if}
    </button>
    <span class="note inline">Starts a tiny throwaway container on each engine with <code>--device /dev/dri</code> (the first check pulls alpine, a few MB).</span>
  </div>
  {#if probeErr}<p class="note warn">GPU check failed: {probeErr}</p>{/if}
  {#if probes}
    <div class="probes">
      {#each probes as p (p.engine)}
        <div class="probe" class:ok={p.dri}>
          <div class="eng-head">
            <b>{engineBadge(p.engine)}</b>
            {#if p.system}<span class="eng-spec">{p.system}</span>{/if}
            <span class="verdict-badge">{!p.reachable ? "not reachable" : p.dri ? "GPU device found" : "no GPU device"}</span>
          </div>
          <p class="note">{p.verdict}</p>
          {#if p.reachable}
            <ul class="facts">
              <li><code>/dev/dri</code>: {#if p.dri_devices?.length}{p.dri_devices.join(", ")}{:else}{p.dri_error || "none"}{/if}</li>
              {#if p.cdi_devices?.length}<li>CDI devices: {p.cdi_devices.join(", ")}{#if p.webgpu} (WebGPU remoting — experimental){/if}</li>{/if}
            </ul>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>

<div class="card">
  <h2>Free memory</h2>
  <p class="hint">
    Docker runs inside a Linux VM (WSL2 on Windows) that holds memory as page cache and is slow to hand it back
    to the host. This drops that cache to reclaim memory — <b>without stopping any containers</b>.
  </p>
  <div class="row">
    <button class="primary" onclick={reclaim} disabled={reclaiming}>
      {#if reclaiming}<span class="spin"></span> Reclaiming…{:else}<Icon name="refresh" size={15} /> Free memory now{/if}
    </button>
    {#if lastFreed !== null}<span class="freed">{lastFreed > 0.1 ? `↓ freed ~${lastFreed.toFixed(1)} GB` : "little to reclaim"}</span>{/if}
  </div>
  <p class="note">
    For WSL2 to return memory <b>automatically</b>, add this once to <code>%UserProfile%\.wslconfig</code> and run
    <code>wsl --shutdown</code> (stops all containers):
  </p>
  <pre class="cfg">[experimental]
autoMemoryReclaim=gradual</pre>
  <p class="note">The same reclaim is available as a standalone script: <code>scripts/windows/free-memory.ps1</code> (or <code>scripts/&lt;os&gt;/free-memory.sh</code>).</p>
</div>

<style>
  h2 { font-size: 15px; margin: 0 0 8px; }
  .card + .card { margin-top: 16px; }
  .row { display: flex; align-items: center; gap: 12px; margin: 12px 0 4px; }
  .primary { display: inline-flex; align-items: center; gap: 6px; }
  .freed { color: var(--ok); font-size: 13px; font-variant-numeric: tabular-nums; }
  .note { font-size: 12.5px; color: var(--muted); line-height: 1.6; margin: 12px 0 6px; }
  code { background: #0c0e13; padding: 1px 5px; border-radius: 4px; font-size: 12px; }
  .cfg { background: #06080c; border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; font: 12.5px/1.5 ui-monospace, monospace; color: #cbd3e1; margin: 0 0 4px; }
  .note.inline { margin: 0; }
  .warn { color: #e0b341; }
  .engines, .probes { display: flex; flex-direction: column; gap: 8px; margin: 12px 0; }
  .eng, .probe { border: 1px solid var(--border); border-radius: 10px; background: #12151c; padding: 10px 12px; }
  .eng.low { border-color: #4a3c15; }
  .probe.ok { border-color: #1d4427; }
  .eng-head { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; font-size: 13.5px; }
  .eng-spec { color: var(--muted); font-size: 12.5px; }
  .eng .note, .probe .note { margin: 6px 0 0; }
  .eng .cfg { margin: 6px 0 0; white-space: pre-wrap; }
  .verdict-badge { margin-left: auto; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.3px; color: var(--muted); border: 1px solid var(--border); border-radius: 999px; padding: 1px 8px; }
  .probe.ok .verdict-badge { color: #46c969; border-color: #1d4427; background: #11261a; }
  .facts { margin: 6px 0 0; padding-left: 18px; font-size: 12.5px; color: var(--muted); }
  .spin { display: inline-block; width: 13px; height: 13px; border: 2px solid rgba(255,255,255,0.4); border-top-color: #fff; border-radius: 50%; animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
