<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import ResourceBar from "../components/ResourceBar.svelte";
  import { post } from "../lib/api";
  import { loadResources, toast } from "../lib/stores";

  let reclaiming = $state(false);
  let lastFreed = $state<number | null>(null);

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
  .spin { display: inline-block; width: 13px; height: 13px; border: 2px solid rgba(255,255,255,0.4); border-top-color: #fff; border-radius: 50%; animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
