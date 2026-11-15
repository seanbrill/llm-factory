<script lang="ts">
  // The resource budget the run guardrail checks (GET /api/resources): memory,
  // GPU memory and CPU in use, and how much of it the factory's running models
  // account for. Plus how to bring in an image built on another machine.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { resources } from "../../lib/stores";
  import { live } from "../../lib/live.svelte";
  import { gb, plural } from "../../lib/format";
  import { num } from "../../lib/library";

  const r = $derived($resources);
  const pct = (used: number, total: number) => (total > 0 ? Math.max(0, Math.min(100, Math.round((used / total) * 100))) : 0);
  const tone = (p: number) => (p >= 90 ? "er" : p >= 70 ? "wa" : "ok");
  const models = $derived(r?.running?.length ?? 0);
</script>

<section class="card" aria-labelledby="budget-h">
  <div class="hd">
    <h2 id="budget-h">Memory now</h2>
    {#if r}<span class="chip" title={r.global_ram ? "Measured just now" : "The factory couldn't measure it, so this adds up its own models' estimates"}>{r.global_ram ? "Live" : "Estimated"}</span>{/if}
  </div>
  {#if live.errors.resources && !r}
    <div class="callout er"><Icon name="errc" /><span><b>Couldn't read memory use.</b><p>{live.errors.resources}. It refreshes every few seconds.</p></span></div>
  {:else if !r}
    <p class="empty"><span class="spin"></span> Reading memory…</p>
  {:else}
    <div class="g">
      <div class="gh"><span>Memory</span><span class="mono">{num(r.used_ram_gb)} of {gb(r.total_ram_gb, 0)} · {gb(r.free_ram_gb)} free</span></div>
      <div class="bar {tone(pct(r.used_ram_gb, r.total_ram_gb))}"><i style="width:{pct(r.used_ram_gb, r.total_ram_gb)}%"></i></div>
    </div>
    {#if r.gpu && r.total_vram_gb > 0}
      <div class="g">
        <div class="gh"><span>GPU memory {#if !r.global_vram}<small>est.</small>{/if}</span><span class="mono">{num(r.used_vram_gb)} of {gb(r.total_vram_gb, 0)} · {gb(r.free_vram_gb)} free</span></div>
        <div class="bar gpu"><i style="width:{pct(r.used_vram_gb, r.total_vram_gb)}%"></i></div>
      </div>
    {/if}
    {#if r.cpus > 0}
      <div class="g">
        <div class="gh"><span>CPU</span><span class="mono">{Math.round(r.cpu_used_pct)}% · load {r.cpu_load1.toFixed(1)} on {r.cpus} cores</span></div>
        <div class="bar {tone(r.cpu_used_pct)}"><i style="width:{Math.min(100, Math.round(r.cpu_used_pct))}%"></i></div>
      </div>
    {/if}
    <p class="note">
      {#if r.global_ram}Counts everything on the factory's machine, not just models.{:else}The factory couldn't measure memory, so this adds up its own models' estimates.{/if}
      {#if models}{`Its ${plural(models, "running model")} ${models === 1 ? "accounts" : "account"} for about ${gb(r.committed_ram_gb)}` +
          (r.committed_vram_gb > 0 ? ` and ${gb(r.committed_vram_gb)} of GPU memory` : "") +
          " (estimated from their sizes)."}{/if}
    </p>
  {/if}

  <div class="bring">
    <span class="bi"><Icon name="upload" size={18} /></span>
    <div class="bt">
      <b>Bring in an image</b>
      <p>Load a .tar saved on another machine (Download .tar on its row there). It shows up here once loaded; on Podman use <span class="mono">podman load</span>.</p>
      <CodeLine cmd="docker load -i <file>.tar" />
    </div>
  </div>
</section>

<style>
  .card { padding: 16px 18px; display: flex; flex-direction: column; gap: 12px; }
  .hd { margin-bottom: 0; }
  .g { display: flex; flex-direction: column; gap: 6px; }
  .gh { display: flex; justify-content: space-between; gap: 10px; font-size: 13px; }
  .gh small { color: var(--wa); font-size: 11px; margin-left: 4px; }
  .gh .mono { color: var(--mu); font-size: 12px; text-align: right; }
  .note { color: var(--mu); font-size: 12px; }
  .empty { display: flex; align-items: center; gap: 8px; }
  .bring {
    display: flex;
    gap: 14px;
    padding: 14px;
    border: 1.5px dashed var(--ln2);
    border-radius: 12px;
    margin-top: 2px;
  }
  .bi {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    display: grid;
    place-items: center;
    background: var(--prs);
    color: var(--pr);
    flex: none;
  }
  .bt { display: flex; flex-direction: column; gap: 6px; min-width: 0; flex: 1; }
  .bt b { font-size: 14px; }
  .bt p { color: var(--mu); font-size: 12.5px; }
  .bt .mono { color: var(--ink); font-size: 12px; }
</style>
