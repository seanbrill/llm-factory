<script lang="ts">
  import Icon from "../components/Icon.svelte";
  import { post } from "../lib/api";
  import { personas, loadPersonas, toast } from "../lib/stores";
  import type { Persona } from "../lib/types";

  let editing = $state<Persona | null | undefined>(undefined); // undefined = closed
  let name = $state("");
  let prompt = $state("");

  function open(p: Persona | null) {
    editing = p;
    name = p?.name ?? "";
    prompt = p?.prompt ?? "";
  }
  function close() { editing = undefined; }

  async function save() {
    if (!name.trim() || !prompt.trim()) { alert("A persona needs a name and a prompt."); return; }
    const body: Partial<Persona> = { name: name.trim(), prompt: prompt.trim() };
    if (editing) body.id = editing.id;
    try {
      await post("/api/personas", body);
    } catch (e) { alert("Save failed: " + (e as Error).message); return; }
    close();
    await loadPersonas();
    toast(`Persona “${name.trim()}” saved.`);
  }

  async function del(p: Persona) {
    if (!confirm(`Delete persona “${p.name}”?`)) return;
    try {
      await post("/api/personas/delete", { id: p.id });
    } catch (e) { alert("Delete failed: " + (e as Error).message); return; }
    await loadPersonas();
  }
</script>

<div class="card">
  <div class="row-between">
    <h2>Personas</h2>
    <button class="ghost small" onclick={() => open(null)}>+ New persona</button>
  </div>
  <p class="hint">Named system prompts you can reuse — bake one into a build, or apply it when you Run an image.</p>

  {#if editing !== undefined}
    <div class="editor">
      <label>Name <input type="text" bind:value={name} placeholder="FinBot" /></label>
      <label>System prompt
        <textarea rows="6" bind:value={prompt} placeholder="You are FinBot, a terse financial-data analyst..."></textarea>
      </label>
      <div class="actions">
        <button class="ghost" onclick={close}>Cancel</button>
        <button class="primary" onclick={save}>Save persona</button>
      </div>
    </div>
  {/if}

  <div class="list">
    {#if !$personas.length}
      <div class="empty">No personas yet. Create one to reuse it across builds and runs.</div>
    {:else}
      {#each $personas as p (p.id)}
        <div class="pcard">
          <div class="phead">
            <span class="pname">{p.name}</span>
            <span class="pactions">
              <button class="small" onclick={() => open(p)}>Edit</button>
              <button class="small danger" onclick={() => del(p)} title="Delete persona"><Icon name="trash" /></button>
            </span>
          </div>
          <div class="pprompt">{p.prompt}</div>
        </div>
      {/each}
    {/if}
  </div>
</div>

<style>
  .editor { border: 1px solid var(--border); border-radius: 12px; padding: 14px 16px; margin: 8px 0 14px; background: #12151c; display: flex; flex-direction: column; gap: 10px; }
  .editor label { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--muted); }
  .actions { display: flex; justify-content: flex-end; gap: 8px; }
  .list { display: grid; gap: 10px; }
  .pcard { border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; background: #12151c; }
  .phead { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-bottom: 6px; }
  .pname { font-weight: 600; font-size: 14px; }
  .pactions { display: inline-flex; gap: 6px; }
  .pprompt { color: var(--muted); font-size: 12.5px; line-height: 1.5; white-space: pre-wrap; max-height: 120px; overflow: auto; }
</style>
