<script lang="ts">
  // The system prompt for a tab: a saved persona, or your own text. Sent with
  // every message to both sides.
  import { personas } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { save, visiblePanes, type Tab } from "../../lib/chat.svelte";

  let { tab }: { tab: Tab } = $props();

  // An ensemble puts its own persona's prompt first; this one is added after it.
  const ensemble = $derived(visiblePanes(tab).some((p) => p.modality === "ensemble"));

  // "" = none, "~" = your own text, else a persona id.
  const current = $derived(
    !tab.system.trim() ? "" : ($personas.find((p) => p.name === tab.persona && p.prompt === tab.system)?.id ?? "~"),
  );

  function pick(id: string) {
    const p = $personas.find((x) => x.id === id);
    if (id === "") {
      tab.system = "";
      tab.persona = "";
    } else if (p) {
      tab.system = p.prompt;
      tab.persona = p.name;
    } else {
      tab.persona = "";
      document.getElementById("pg-system")?.focus();
    }
    save();
  }
  function edit(v: string) {
    tab.system = v;
    const p = $personas.find((x) => x.name === tab.persona);
    if (p && p.prompt !== v) tab.persona = "";
    save();
  }
</script>

<div class="f">
  <label for="pg-persona">Persona</label>
  <select id="pg-persona" value={current} onchange={(e) => pick(e.currentTarget.value)}>
    <option value="">None</option>
    {#each $personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
    <option value="~">Your own prompt</option>
  </select>
</div>
<div class="f">
  <label for="pg-system">System prompt</label>
  <textarea id="pg-system" rows="5" value={tab.system} oninput={(e) => edit(e.currentTarget.value)} placeholder="You are a helpful assistant."></textarea>
  <p class="hint">
    Goes first in every request{tab.compare ? ", to both sides" : ""}. A model built with its own prompt may add that one too.
    <a href={href("personas")}>Manage personas</a>
  </p>
  {#if ensemble}
    <p class="hint ens">
      An ensemble applies its own persona first, and speaks in that persona's voice; the prompt here is added after
      it. Change the ensemble's persona on the <a href={href("ensembles")}>Ensembles</a> page.
    </p>
  {/if}
</div>

<style>
  .f { display: flex; flex-direction: column; gap: 6px; }
  textarea { min-height: 96px; }
  .hint { color: var(--fa); font-size: 12px; }
  .hint.ens { padding: 8px 10px; border-radius: var(--r); background: var(--prs); color: var(--ink); }
</style>
