<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import ModalityBadge from "../components/ModalityBadge.svelte";
  import { api, post, labelVal } from "../lib/api";
  import { imageRef } from "../lib/util";
  import { ensembles, catalog, loadEnsembles, toast } from "../lib/stores";
  import type { Ensemble, EnsembleMember, ImageInfo } from "../lib/types";

  const TOOL_FOR: Record<string, string> = {
    text: "chat", code: "chat", reasoning: "chat", vision: "see_image",
    image: "generate_image", video: "generate_video", "audio-stt": "transcribe", tts: "speak",
  };

  interface Candidate { ref: string; name: string; modality: string; tool: string; vram: number; }

  let editing = $state<Ensemble | null | undefined>(undefined);
  let candidates = $state<Candidate[]>([]);
  let chosen = $state<Set<string>>(new Set());
  let buildLog = $state("");
  let buildTimer: ReturnType<typeof setInterval> | undefined;

  // form fields
  let name = $state("");
  let mode = $state("orchestrated");
  let routing = $state("heuristic");
  let budget = $state(12);
  let compute = $state("cuda");
  let conductor = $state("");

  async function imageList(): Promise<Candidate[]> {
    let imgs: ImageInfo[] = [];
    try { imgs = await api<ImageInfo[]>("/api/images"); } catch { /* none */ }
    const out: Candidate[] = [];
    for (const im of imgs) {
      if (labelVal(im.Labels, "local-llm.kind") === "ensemble") continue;
      const { ref, name } = imageRef(im);
      const mod = labelVal(im.Labels, "local-llm.modality") ||
        ($catalog.find((m) => m.id === labelVal(im.Labels, "local-llm.model"))?.modality ?? "text");
      const tool = TOOL_FOR[mod];
      if (!tool) continue;
      const cat = $catalog.find((m) => m.id === labelVal(im.Labels, "local-llm.model"));
      out.push({ ref, name, modality: mod, tool, vram: cat?.min_vram_gb ?? 6 });
    }
    return out;
  }

  async function open(e: Ensemble | null) {
    editing = e;
    name = e?.name ?? ""; mode = e?.package_mode ?? "orchestrated";
    routing = e?.routing ?? "heuristic"; budget = e?.vram_budget_gb ?? 12;
    compute = e?.compute ?? "cuda"; conductor = e?.conductor ?? "";
    candidates = await imageList();
    chosen = new Set((e?.members ?? []).map((m) => m.image));
  }
  function close() { editing = undefined; }
  function toggle(ref: string) {
    const s = new Set(chosen);
    s.has(ref) ? s.delete(ref) : s.add(ref);
    chosen = s;
  }

  async function save() {
    if (!name.trim()) { alert("Name your ensemble."); return; }
    const members: EnsembleMember[] = candidates
      .filter((c) => chosen.has(c.ref))
      .map((c) => ({ tool: c.tool, modality: c.modality, image: c.ref, vram_gb: c.vram }));
    if (!members.length) { alert("Pick at least one member image."); return; }
    const body: Partial<Ensemble> = {
      id: editing?.id, name: name.trim(), package_mode: mode, routing,
      conductor, vram_budget_gb: budget, engine: "docker", compute, members,
    };
    try { await post("/api/ensembles", body); }
    catch (e) { alert("Save failed: " + (e as Error).message); return; }
    close();
    await loadEnsembles();
    toast(`Ensemble “${name.trim()}” saved.`);
  }
  async function del(e: Ensemble) {
    if (!confirm(`Delete ensemble “${e.name}”?`)) return;
    try { await post("/api/ensembles/delete", { id: e.id }); } catch (err) { alert("Delete failed: " + (err as Error).message); return; }
    await loadEnsembles();
  }

  async function build(e: Ensemble) {
    buildLog = `Building “${e.name}” …\n`;
    try { await post("/api/ensemble/build", { id: e.id, compute: e.compute }); }
    catch (err) { buildLog += "ERROR: " + (err as Error).message + "\n"; return; }
    clearInterval(buildTimer);
    let off = 0;
    buildTimer = setInterval(async () => {
      let st: { lines?: string[]; next_offset?: number; status?: string };
      try { st = await api("/api/build/state?offset=" + off); } catch { return; }
      for (const l of st.lines ?? []) buildLog += l + "\n";
      off = st.next_offset ?? off;
      if (st.status && st.status !== "running") {
        clearInterval(buildTimer);
        toast(st.status === "done" ? "Ensemble image built." : "Ensemble build failed — see log.");
      }
    }, 1000);
  }

  const chatImages = $derived(candidates.filter((c) => c.tool === "chat"));
</script>

<div class="card">
  <div class="row-between">
    <h2>Ensembles</h2>
    <button class="ghost small" onclick={() => open(null)}>+ New ensemble</button>
  </div>
  <p class="hint">Combine your built images into one multimodal super-model: a tiny <b>Conductor</b> routes each request to the right specialist, starting/stopping them to fit your VRAM. Builds into a single runnable image. (See <code>docs/ENSEMBLE.md</code>.)</p>

  {#if editing !== undefined}
    <div class="editor">
      <div class="grid">
        <label>Name <input type="text" bind:value={name} placeholder="My Ensemble" /></label>
        <label>Package mode
          <select bind:value={mode}>
            <option value="orchestrated">Orchestrated — runs sibling containers</option>
            <option value="embedded">Embedded / mega — bake everything (staged)</option>
          </select>
        </label>
        <label>Routing
          <select bind:value={routing}>
            <option value="heuristic">Heuristic — fast, no extra model</option>
            <option value="tool-calling">Tool-calling — uses a conductor model</option>
          </select>
        </label>
        <label>VRAM budget (GB) <input type="number" min="1" max="80" bind:value={budget} /></label>
        <label>Compute
          <select bind:value={compute}>
            <option value="cuda">NVIDIA GPU (CUDA)</option>
            <option value="cpu">CPU</option>
            <option value="vulkan">GPU — Vulkan / Metal</option>
          </select>
        </label>
        <label>Conductor model (tool-calling)
          <select bind:value={conductor}>
            <option value="">— none —</option>
            {#each chatImages as c (c.ref)}<option value={c.ref}>{c.name}</option>{/each}
          </select>
        </label>
      </div>
      <span class="field-label">Members — pick which built images to include</span>
      <div class="members">
        {#if !candidates.length}
          <div class="empty">No built images yet — build some models first.</div>
        {:else}
          {#each candidates as c (c.ref)}
            <label class="member">
              <input type="checkbox" checked={chosen.has(c.ref)} onchange={() => toggle(c.ref)} />
              <ModalityBadge mod={c.modality} /> <b>{c.name}</b> <span class="meta">{c.tool} · ~{c.vram} GB</span>
            </label>
          {/each}
        {/if}
      </div>
      <div class="actions">
        <button class="ghost" onclick={close}>Cancel</button>
        <button class="primary" onclick={save}>Save ensemble</button>
      </div>
    </div>
  {/if}

  {#if buildLog}<pre class="log">{buildLog}</pre>{/if}

  <div class="list">
    {#if !$ensembles.length}
      <div class="empty">No ensembles yet. Create one from your built images.</div>
    {:else}
      {#each $ensembles as e (e.id)}
        <div class="ecard">
          <div class="ehead">
            <span class="ename">{e.name} <span class="meta">{e.package_mode} · {e.routing} · {(e.members || []).length} members · {e.vram_budget_gb} GB</span></span>
            <span class="eactions">
              <button class="small primary" onclick={() => build(e)}>Build image</button>
              <button class="small" onclick={() => open(e)}>Edit</button>
              <button class="small danger" onclick={() => del(e)} title="Delete"><Icon name="trash" /></button>
            </span>
          </div>
          <div class="eprompt">{(e.members || []).map((m) => `${m.tool} → ${m.image}`).join("\n")}</div>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .editor { border: 1px solid var(--border); border-radius: 12px; padding: 14px 16px; margin: 8px 0 14px; background: #12151c; display: flex; flex-direction: column; gap: 10px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
  .editor label { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--muted); }
  .field-label { font-size: 13px; color: var(--muted); margin-top: 6px; }
  .members { display: grid; gap: 6px; max-height: 300px; overflow: auto; }
  .member { display: flex; align-items: center; gap: 8px; padding: 7px 10px; border: 1px solid var(--border); border-radius: 8px; background: #12151c; cursor: pointer; font-size: 13px; flex-direction: row; }
  .member input { width: auto; }
  .member b { color: var(--text); }
  .meta { color: var(--muted); font-size: 11.5px; font-weight: 400; }
  .actions { display: flex; justify-content: flex-end; gap: 8px; }
  .log { background: #06080c; border: 1px solid var(--border); border-radius: 8px; padding: 12px; max-height: 280px; overflow: auto; white-space: pre-wrap; font: 12.5px/1.5 ui-monospace, monospace; color: #cbd3e1; margin: 0 0 14px; }
  .list { display: grid; gap: 10px; }
  .ecard { border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; background: #12151c; }
  .ehead { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 6px; }
  .ename { font-weight: 600; font-size: 14px; }
  .eactions { display: inline-flex; gap: 6px; }
  .eprompt { color: var(--muted); font-size: 12.5px; line-height: 1.5; white-space: pre-wrap; }
</style>
