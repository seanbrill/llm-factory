<script lang="ts">
  // Live VRAM/RAM budget bars, shared by Containers + Images. Self-loads the
  // /api/resources store on mount; pages that mutate containers also refresh it.
  import { onMount } from "svelte";
  import Icon from "./Icon.svelte";
  import { resources, loadResources } from "../lib/stores";

  onMount(() => loadResources());

  const pct = (used: number, total: number) => (total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0);
  const tone = (p: number) => (p >= 90 ? "hot" : p >= 70 ? "warm" : "ok");
</script>

{#if $resources}
  {@const r = $resources}
  {@const vp = pct(r.used_vram_gb, r.total_vram_gb)}
  {@const mp = pct(r.used_ram_gb, r.total_ram_gb)}
  <div class="budget">
    {#if r.gpu && r.total_vram_gb > 0}
      <div class="gauge">
        <div class="ghead"><span><Icon name="gpu" size={14} /> VRAM {#if !r.global_vram}<span class="est">est.</span>{/if}</span><span class="gval">{r.used_vram_gb.toFixed(1)} / {r.total_vram_gb.toFixed(0)} GB · {r.free_vram_gb.toFixed(1)} free</span></div>
        <div class="track"><div class="fill {tone(vp)}" style="width:{vp}%"></div></div>
      </div>
    {/if}
    <div class="gauge">
      <div class="ghead"><span><Icon name="cpu" size={14} /> RAM {#if !r.global_ram}<span class="est">est.</span>{/if}</span><span class="gval">{r.used_ram_gb.toFixed(1)} / {r.total_ram_gb.toFixed(0)} GB · {r.free_ram_gb.toFixed(1)} free</span></div>
      <div class="track"><div class="fill {tone(mp)}" style="width:{mp}%"></div></div>
    </div>
  </div>
  <p class="bnote">
    {#if r.global_vram || r.global_ram}Live whole-machine usage (counts everything, not just the factory).{:else}Estimated from the factory's own models — live usage couldn't be read, so this is an upper bound.{/if}
    {#if r.committed_vram_gb > 0 || r.committed_ram_gb > 0} This factory's models account for ~{r.committed_vram_gb.toFixed(1)} GB VRAM / {r.committed_ram_gb.toFixed(1)} GB RAM of it.{/if}
  </p>
{/if}

<style>
  .budget { display: flex; gap: 18px; flex-wrap: wrap; margin: 14px 0 6px; }
  .gauge { flex: 1; min-width: 220px; }
  .ghead { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12.5px; color: var(--muted); margin-bottom: 5px; }
  .ghead :global(.ico) { vertical-align: -2px; margin-right: 4px; }
  .gval { font-variant-numeric: tabular-nums; }
  .est { font-size: 10px; color: #e0832f; border: 1px solid #4a3115; border-radius: 4px; padding: 0 4px; margin-left: 2px; }
  .track { height: 9px; background: #0c0e13; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .fill { height: 100%; border-radius: 999px; transition: width 0.3s; }
  .fill.ok { background: #46c969; }
  .fill.warm { background: #e3b341; }
  .fill.hot { background: #f0683c; }
  .bnote { font-size: 11px; color: var(--muted); margin: 8px 0 0; line-height: 1.5; }
</style>
