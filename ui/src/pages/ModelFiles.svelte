<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import { api, post } from "../lib/api";
  import type { ModelFile } from "../lib/types";

  let models = $state<ModelFile[]>([]);
  let loading = $state(true);
  let error = $state("");

  async function load() {
    loading = true;
    try {
      const all = await api<ModelFile[]>("/api/models");
      models = (all ?? []).filter((m) => m.downloaded);
      error = "";
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function del(m: ModelFile) {
    if (!confirm(`Delete weights for ${m.name} (${m.on_disk_gb.toFixed(2)} GB)? A future build will re-download it.`)) return;
    try {
      await post("/api/model/delete", { model_id: m.id });
      load();
    } catch (e) {
      alert("Delete failed: " + (e as Error).message);
    }
  }

  $effect(() => { load(); });
</script>

<div class="card">
  <div class="row-between">
    <h2>Downloaded model files</h2>
    <button class="ghost" onclick={load} title="Refresh"><Icon name="refresh" /></button>
  </div>
  {#if loading}
    <div class="empty">Loading…</div>
  {:else if error}
    <div class="empty">{error}</div>
  {:else if !models.length}
    <div class="empty">No model files downloaded yet (they download on first build).</div>
  {:else}
    <table>
      <thead><tr><th>Model</th><th>File</th><th>Size on disk</th><th class="r">Actions</th></tr></thead>
      <tbody>
        {#each models as m (m.id)}
          <tr>
            <td>{m.name}</td>
            <td><code>{m.file}</code></td>
            <td>{m.on_disk_gb.toFixed(2)} GB</td>
            <td class="r">
              <button class="small danger" onclick={() => del(m)} title="Delete weights"><Icon name="trash" /></button>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  table { width: 100%; border-collapse: collapse; font-size: 13.5px; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  th { color: var(--muted); font-weight: 600; }
  th.r, td.r { text-align: right; }
  code { background: #0c0e13; padding: 1px 5px; border-radius: 4px; }
</style>
