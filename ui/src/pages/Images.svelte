<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import Modal from "../components/Modal.svelte";
  import ModalityBadge from "../components/ModalityBadge.svelte";
  import { api, post, labelVal, type ApiError } from "../lib/api";
  import { imageRef, containerRef, containerPort, isRunning, computeBadge, engineBadge } from "../lib/util";
  import { catalog, personas, buildTemplate, toast, loadResources } from "../lib/stores";
  import { modMeta } from "../lib/icons";
  import type { ImageInfo, ContainerInfo } from "../lib/types";

  let imgs = $state<ImageInfo[]>([]);
  let conts = $state<ContainerInfo[]>([]);
  let loading = $state(true);
  let error = $state("");
  let gen = 0;

  async function load() {
    const my = ++gen;
    try {
      const [i, c] = await Promise.all([
        api<ImageInfo[]>("/api/images"),
        api<ContainerInfo[]>("/api/containers").catch(() => [] as ContainerInfo[]),
      ]);
      if (my !== gen) return;
      imgs = i ?? [];
      conts = c ?? [];
      error = "";
    } catch (e) {
      if (my === gen) error = (e as Error).message;
    } finally {
      if (my === gen) loading = false;
    }
  }

  function modalityOf(im: ImageInfo): string {
    const baked = labelVal(im.Labels, "local-llm.modality");
    if (baked) return baked;
    const model = $catalog.find((m) => m.id === labelVal(im.Labels, "local-llm.model"));
    return model?.modality ?? "text";
  }
  function runningFor(ref: string): ContainerInfo | undefined {
    return conts.find((c) => containerRef(c) === ref && isRunning(c));
  }

  function useAsTemplate(ref: string, engine: string) {
    buildTemplate.set({ ref, engine });
    location.hash = "build";
    toast("Loaded into the Build form.");
  }
  function download(ref: string, engine: string) {
    window.location.href = `/api/image/download?ref=${encodeURIComponent(ref)}&engine=${encodeURIComponent(engine)}`;
  }
  async function del(ref: string, engine: string) {
    if (!confirm(`Delete image ${ref} and its exported .tar?`)) return;
    try {
      await post("/api/image/delete", { ref, engine });
      load();
    } catch (e) { alert("Delete failed: " + (e as Error).message); }
  }

  // ---- run modal ----
  let runOpen = $state(false);
  let runRef = $state("");
  let runEngine = $state("docker");
  let runPort = $state(8080);
  let runPersona = $state("");
  let runPrompt = $state("");
  let runAlways = $state(false);

  function openRun(ref: string, engine: string, port: number) {
    runRef = ref; runEngine = engine; runPort = port;
    runPersona = ""; runPrompt = ""; runAlways = false;
    runOpen = true;
  }
  function onPersona() {
    const p = $personas.find((x) => x.id === runPersona);
    if (p) runPrompt = p.prompt;
  }
  async function startRun() {
    if (!runPort) { alert("Enter a port."); return; }
    const body: Record<string, unknown> = { ref: runRef, port: runPort, engine: runEngine };
    if (runPrompt.trim()) body.system_prompt = runPrompt.trim();
    if (runAlways) body.inject_mode = "always";
    runOpen = false;
    await doRun(body);
  }
  // Runs, and on the resource guardrail (409 + needs_force) offers to start anyway.
  async function doRun(body: Record<string, unknown>) {
    try {
      await post("/api/run", body);
      if ((body.system_prompt as string)?.trim()) toast(`Running ${runRef} on :${runPort} with a custom prompt.`);
      loadResources();
      setTimeout(load, 600);
    } catch (e) {
      const err = e as ApiError;
      if (err.status === 409 && err.data?.needs_force) {
        if (confirm(`⚠ ${err.message}\n\nStart it anyway? It may crash if the system runs out of memory.`)) {
          await doRun({ ...body, force: true });
        }
        return;
      }
      alert("Run failed: " + err.message);
    }
  }

  $effect(() => { load(); });
</script>

<div class="card">
  <div class="row-between">
    <h2>Built images</h2>
    <button class="ghost" onclick={load} title="Refresh"><Icon name="refresh" /></button>
  </div>
  <p class="hint">Click an image name to load its settings into the Build form. Click <b>Run</b> to choose a port and optionally apply a persona or one-off prompt.</p>

  {#if loading && !imgs.length}
    <div class="empty"><span class="spinner"></span> Loading…</div>
  {:else if error}
    <div class="empty">{error}</div>
  {:else if !imgs.length}
    <div class="empty">No images built yet.</div>
  {:else}
    <table>
      <thead><tr><th>Image</th><th>Tag</th><th>Engine</th><th>Compute</th><th>Size</th><th>Status</th><th>Port</th><th class="r">Actions</th></tr></thead>
      <tbody>
        {#each imgs as im, i (im.ID)}
          {@const r = imageRef(im)}
          {@const up = runningFor(r.ref)}
          {@const eng = im.Engine || "docker"}
          <tr>
            <td>
              <a href="#build" onclick={(e) => { e.preventDefault(); useAsTemplate(r.ref, eng); }} class="link">{r.name}</a>
              <br /><ModalityBadge mod={modalityOf(im)} />
            </td>
            <td>{r.tag}</td>
            <td>{engineBadge(eng)}</td>
            <td>{computeBadge(im.Compute)}</td>
            <td>{im.Size || ""}</td>
            <td>{#if up}<span class="run">● running</span>{:else}—{/if}</td>
            <td><input class="port" type="number" value={up ? containerPort(up) : 8080 + i} /></td>
            <td class="r">
              <div class="actions">
                <button class="small primary" onclick={(e) => openRun(r.ref, eng, parseInt((e.currentTarget.closest('tr')!.querySelector('.port') as HTMLInputElement).value, 10))}><Icon name="play" size={14} /> Run</button>
                <button class="small" onclick={() => download(r.ref, eng)} title="Download as .tar"><Icon name="download" /></button>
                <button class="small danger" onclick={() => del(r.ref, eng)} title="Delete image + .tar"><Icon name="trash" /></button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<Modal bind:open={runOpen} title={`Run <span style="color:var(--accent)">${runRef}</span>`}>
  <p class="hint">Start a container from this image. Optionally apply a persona or a one-off system prompt for this instance only.</p>
  <div class="grid">
    <label>Port <input type="number" min="1" max="65535" bind:value={runPort} /></label>
    <label>Persona
      <select bind:value={runPersona} onchange={onPersona}>
        <option value="">— None (use baked prompt) —</option>
        {#each $personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>
  </div>
  <label class="lbl">System prompt override (optional)
    <textarea rows="5" bind:value={runPrompt} placeholder="Leave blank to use the image's baked prompt."></textarea>
  </label>
  <label class="check"><input type="checkbox" bind:checked={runAlways} /> <span>Always enforce — override any system message the client sends</span></label>
  <div class="modal-actions">
    <button class="ghost" onclick={() => (runOpen = false)}>Cancel</button>
    <button class="primary" onclick={startRun}>Run</button>
  </div>
</Modal>

<style>
  table { width: 100%; border-collapse: collapse; font-size: 13.5px; }
  th, td { text-align: left; padding: 8px 10px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  th { color: var(--muted); font-weight: 600; }
  th.r, td.r { text-align: right; }
  .run { color: var(--ok); font-weight: 600; }
  .port { width: 84px; height: 34px; padding: 5px 8px; }
  .actions { display: inline-flex; gap: 6px; justify-content: flex-end; }
  .actions button { height: 34px; display: inline-flex; align-items: center; gap: 5px; }
  .link { cursor: pointer; }
  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  .grid label, .lbl { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--muted); margin-top: 10px; }
  .check { display: flex; align-items: center; gap: 8px; margin-top: 10px; font-size: 13px; }
  .check input { width: auto; }
  .modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
  .spinner { display: inline-block; width: 14px; height: 14px; vertical-align: -2px; margin-right: 7px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.7s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
