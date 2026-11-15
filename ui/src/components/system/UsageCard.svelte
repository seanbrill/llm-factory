<script lang="ts">
  // Memory in use (System, beside sheet frame f13): the live budget from
  // /api/resources — memory, VRAM when there is a GPU to measure, CPU load —
  // and how much of it the factory's own models account for. Then Free memory:
  // Docker's VM keeps memory as page cache, and POST /api/system/reclaim drops
  // it without stopping containers. On Windows WSL2 can do that on its own.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { post } from "../../lib/api";
  import { resources, toast } from "../../lib/stores";
  import { live, refresh } from "../../lib/live.svelte";
  import { gb } from "../../lib/format";

  const r = $derived($resources);
  const WSL = "[experimental]\nautoMemoryReclaim=gradual";

  const pct = (used: number, total: number) => (total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0);
  const tone = (p: number) => (p >= 90 ? "er" : p >= 70 ? "wa" : "ok");

  const gauges = $derived.by(() => {
    if (!r) return [];
    const out: { key: string; icon: string; label: string; value: string; p: number }[] = [];
    out.push({
      key: "ram",
      icon: "layers",
      label: "Memory",
      value: `${gb(r.used_ram_gb)} of ${gb(r.total_ram_gb, 0)} · ${gb(r.free_ram_gb)} free`,
      p: pct(r.used_ram_gb, r.total_ram_gb),
    });
    if (r.gpu && r.total_vram_gb > 0) {
      out.push({
        key: "vram",
        icon: "gpu",
        label: "GPU memory",
        value: `${gb(r.used_vram_gb)} of ${gb(r.total_vram_gb, 0)} · ${gb(r.free_vram_gb)} free`,
        p: pct(r.used_vram_gb, r.total_vram_gb),
      });
    }
    if (r.cpus > 0) {
      out.push({
        key: "cpu",
        icon: "cpu",
        label: "CPU",
        value: `${Math.round(r.cpu_used_pct)}% · load ${r.cpu_load1.toFixed(1)} of ${r.cpus}`,
        p: Math.min(100, Math.round(r.cpu_used_pct)),
      });
    }
    return out;
  });
  const estimate = $derived(!!r && !r.global_ram && !r.global_vram);

  let reclaiming = $state(false);
  let freed = $state<number | null>(null);
  async function reclaim() {
    reclaiming = true;
    try {
      const res = await post<{ freed_gb: number }>("/api/system/reclaim", {});
      freed = res.freed_gb;
      toast(res.freed_gb > 0.1 ? `Freed about ${gb(res.freed_gb)} back to the host.` : "Cache dropped. There was little to free right now.");
      refresh("resources");
    } catch (e) {
      toast(`Couldn't free memory: ${(e as Error).message}. Check that Docker is running, then try again.`, 7000);
    } finally {
      reclaiming = false;
    }
  }
</script>

<div class="hd">
  <h2>Memory in use</h2>
  {#if estimate}<span class="chip wa" title="Live usage couldn't be read, so this counts only the factory's own models">Estimate</span>{/if}
</div>

{#if !r && !live.loaded.resources}
  <p class="sub st"><span class="spin"></span>Reading memory use…</p>
{:else if !r}
  <p class="sub">Couldn't read memory use{live.errors.resources ? ` (${live.errors.resources})` : ""}. This card tries again on its own.</p>
{:else}
  <div class="gauges">
    {#each gauges as g (g.key)}
      <div class="g">
        <div class="gh">
          <span class="gl"><Icon name={g.icon} size={14} />{g.label}</span>
          <span class="mono gv">{g.value}</span>
        </div>
        <div
          class="bar {tone(g.p)}"
          role="progressbar"
          aria-label="{g.label} in use"
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={g.p}>
          <i style="width:{g.p}%"></i>
        </div>
      </div>
    {/each}
  </div>
  <p class="note">
    {estimate
      ? "Estimated from the factory's own models: live usage couldn't be read, so treat it as a floor."
      : "Live, from the machine the factory runs on (Docker's VM). It counts everything, not just the factory."}
    {#if r.committed_ram_gb > 0 || r.committed_vram_gb > 0}
      {` The factory's models account for about ${gb(r.committed_ram_gb)}${r.committed_vram_gb > 0 ? ` and ${gb(r.committed_vram_gb)} of GPU memory` : ""}.`}
    {/if}
  </p>
{/if}

<div class="free">
  <h3>Free memory</h3>
  <p>
    Docker's VM holds on to memory as page cache and is slow to give it back. This drops that cache, <b>without stopping any containers</b>.
  </p>
  <div class="frow">
    <button class="small" onclick={reclaim} disabled={reclaiming}>
      {#if reclaiming}<span class="spin"></span>Freeing…{:else}<Icon name="refresh" size={14} />Free memory now{/if}
    </button>
    {#if freed !== null}
      <span class="freed" class:ok={freed > 0.1} role="status">{freed > 0.1 ? `Freed about ${gb(freed)}` : "Little to free right now"}</span>
    {/if}
  </div>
  <details>
    <summary>Windows (WSL2) and scripts</summary>
    <div class="more">
      <p>To have WSL2 give memory back on its own, add this once to <code>%UserProfile%\.wslconfig</code>, then run <code>wsl --shutdown</code> (it stops all containers):</p>
      <div class="cfg">
        <pre>{WSL}</pre>
        <CopyButton text={WSL} title="Copy the .wslconfig lines" />
      </div>
      <p>
        The same cache drop as a script: <code>scripts/windows/free-memory.ps1</code>, <code>scripts/macos/free-memory.sh</code> or
        <code>scripts/linux/free-memory.sh</code>.
      </p>
    </div>
  </details>
</div>

<style>
  .st { display: flex; align-items: center; gap: 8px; }
  .gauges { display: flex; flex-direction: column; gap: 12px; }
  .gh { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-bottom: 6px; font-size: 13px; min-width: 0; }
  .gl { display: inline-flex; align-items: center; gap: 7px; color: var(--mu); flex: none; }
  .gv { color: var(--ink); font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .note { margin-top: 10px; font-size: 12px; color: var(--fa); }
  .free { margin-top: 16px; padding-top: 14px; border-top: 1px solid var(--ln); }
  .free h3 { font-family: var(--font-body); font-size: 14px; font-weight: 600; margin: 0 0 4px; letter-spacing: 0; }
  .free > p { font-size: 12.5px; color: var(--mu); }
  .free b { color: var(--ink); font-weight: 600; }
  .frow { display: flex; align-items: center; gap: 12px; margin-top: 10px; flex-wrap: wrap; }
  .freed { font-size: 12.5px; color: var(--mu); font-variant-numeric: tabular-nums; }
  .freed.ok { color: var(--ok); }
  details { margin-top: 12px; font-size: 12.5px; color: var(--mu); }
  summary { cursor: pointer; color: var(--mu); font-weight: 500; width: fit-content; border-radius: 4px; }
  summary:hover { color: var(--ink); }
  .more { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; }
  .more code { font-size: 11.5px; }
  .cfg {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 8px 8px 8px 12px;
    border-radius: var(--r);
    background: color-mix(in srgb, var(--cv) 70%, black);
    border: 1px solid var(--ln);
  }
  .cfg pre { flex: 1; margin: 0; font: 12px/1.5 var(--font-mono); color: var(--ink); overflow-x: auto; }
  .spin { width: 13px; height: 13px; }
</style>
