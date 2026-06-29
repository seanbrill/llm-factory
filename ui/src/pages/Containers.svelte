<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import { api, post } from "../lib/api";
  import { resources, loadResources } from "../lib/stores";
  import type { ContainerInfo } from "../lib/types";

  let rows = $state<ContainerInfo[]>([]);
  let loading = $state(true);
  let error = $state("");

  const pct = (used: number, total: number) => (total > 0 ? Math.min(100, Math.round((used / total) * 100)) : 0);
  const tone = (p: number) => (p >= 90 ? "hot" : p >= 70 ? "warm" : "ok");

  // A generation token guards against overlapping reloads painting stale rows.
  let gen = 0;
  async function load() {
    const my = ++gen;
    loading = true;
    loadResources();
    try {
      const cs = await api<ContainerInfo[]>("/api/containers");
      if (my !== gen) return;
      rows = cs ?? [];
      error = "";
    } catch (e) {
      if (my === gen) error = (e as Error).message;
    } finally {
      if (my === gen) loading = false;
    }
  }

  const running = (c: ContainerInfo) => /up|running|healthy/i.test(c.Status || c.State || "");

  async function stop(c: ContainerInfo) {
    if (!confirm(`Stop and remove container ${c.Names || c.ID}?`)) return;
    try {
      await post("/api/stop", { id: c.ID || c.Names, engine: c.Engine || "docker" });
      load();
    } catch (e) {
      alert("Remove failed: " + (e as Error).message);
    }
  }

  async function logs(c: ContainerInfo) {
    try {
      const q = `id=${encodeURIComponent(c.ID || c.Names || "")}&engine=${encodeURIComponent(c.Engine || "docker")}&tail=400`;
      const out = await api<{ logs: string }>("/api/container/logs?" + q);
      const w = window.open("", "_blank", "width=900,height=600");
      if (!w) return alert("Allow popups to view logs.");
      w.document.body.style.cssText = "margin:0;background:#0b0b0b;color:#ddd";
      w.document.body.innerHTML = `<pre style="white-space:pre-wrap;font:12px ui-monospace,monospace;padding:1rem;margin:0"></pre>`;
      w.document.querySelector("pre")!.textContent = out.logs || "(no output)";
    } catch (e) {
      alert("Couldn't fetch logs: " + (e as Error).message);
    }
  }

  $effect(() => {
    load();
  });
</script>

<div class="card">
  <div class="row-between">
    <h2>Running containers</h2>
    <button class="ghost icon-btn" onclick={load} title="Refresh"><Icon name="refresh" /></button>
  </div>

  {#if $resources}
    {@const r = $resources}
    {@const vp = pct(r.committed_vram_gb, r.total_vram_gb)}
    {@const mp = pct(r.committed_ram_gb, r.total_ram_gb)}
    <div class="budget">
      {#if r.gpu && r.total_vram_gb > 0}
        <div class="gauge">
          <div class="ghead"><span><Icon name="gpu" size={14} /> VRAM</span><span class="gval">{r.committed_vram_gb.toFixed(1)} / {r.total_vram_gb.toFixed(0)} GB · {r.free_vram_gb.toFixed(1)} free</span></div>
          <div class="track"><div class="fill {tone(vp)}" style="width:{vp}%"></div></div>
        </div>
      {/if}
      <div class="gauge">
        <div class="ghead"><span><Icon name="cpu" size={14} /> RAM</span><span class="gval">{r.committed_ram_gb.toFixed(1)} / {r.total_ram_gb.toFixed(0)} GB · {r.free_ram_gb.toFixed(1)} free</span></div>
        <div class="track"><div class="fill {tone(mp)}" style="width:{mp}%"></div></div>
      </div>
    </div>
    <p class="bnote">Estimated from the models this factory runs. Anything using the GPU/RAM outside the factory (e.g. a model you started by hand) isn't counted, so "free" is an upper bound.</p>
  {/if}

  {#if loading && !rows.length}
    <div class="empty"><span class="spinner"></span> Loading…</div>
  {:else if error}
    <div class="empty">{error}</div>
  {:else if !rows.length}
    <div class="empty">No containers.</div>
  {:else}
    <table>
      <thead>
        <tr><th>Name</th><th>Image</th><th>Engine</th><th>Status</th><th>Ports</th><th class="r">Actions</th></tr>
      </thead>
      <tbody>
        {#each rows as c (c.ID || c.Names)}
          <tr>
            <td>{c.Names || ""}</td>
            <td>{c.Image || ""}</td>
            <td>{c.Engine || "docker"}</td>
            <td class:run={running(c)}>{c.Status || c.State || ""}</td>
            <td>{c.Ports || ""}</td>
            <td class="r">
              <div class="actions">
                <button class="small icon-btn" onclick={() => logs(c)} title="View logs"><Icon name="doc" /></button>
                <button class="small danger" onclick={() => stop(c)}><Icon name="stop" size={14} /> Stop</button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .budget { display: flex; gap: 18px; flex-wrap: wrap; margin: 14px 0 6px; }
  .gauge { flex: 1; min-width: 220px; }
  .ghead { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12.5px; color: var(--muted); margin-bottom: 5px; }
  .ghead :global(.ico) { vertical-align: -2px; margin-right: 4px; }
  .gval { font-variant-numeric: tabular-nums; }
  .track { height: 9px; background: #0c0e13; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .fill { height: 100%; border-radius: 999px; transition: width 0.3s; }
  .fill.ok { background: #46c969; }
  .fill.warm { background: #e3b341; }
  .fill.hot { background: #f0683c; }
  .bnote { font-size: 11px; color: var(--muted); margin: 8px 0 0; line-height: 1.5; }
  table { width: 100%; border-collapse: collapse; font-size: 13.5px; margin-top: 14px; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  th { color: var(--muted); font-weight: 600; }
  th.r, td.r { text-align: right; }
  .run { color: var(--ok); font-weight: 600; }
  .actions { display: inline-flex; gap: 6px; justify-content: flex-end; }
  .actions button { height: 32px; display: inline-flex; align-items: center; gap: 5px; }
  .icon-btn { padding: 6px 8px; justify-content: center; }
  .spinner {
    display: inline-block; width: 14px; height: 14px; vertical-align: -2px; margin-right: 7px;
    border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%;
    animation: spin 0.7s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
