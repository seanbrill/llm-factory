<script lang="ts">
  // The rarely changed settings of one planned build (a row's expander on the
  // Build plan): where it runs, what it builds for, the image name, a baked-in
  // system prompt, context and memory, a local URL and restart behaviour, and
  // for chat models the tools baked into the image (ToolsSetting).
  // Edits write straight into the plan item (saved by lib/plan.svelte.ts).
  import { tick } from "svelte";
  import Icon from "../Icon.svelte";
  import ToolsSetting from "./ToolsSetting.svelte";
  import { canHarness } from "../../lib/baked";
  import { ctxDefaultFor, fmtTokens } from "../../lib/util";
  import { contextFor } from "../../lib/meta";
  import { computeName } from "../../lib/format";
  import { effectiveCompute, type PlanItem } from "../../lib/plan.svelte";
  import type { Model, Persona, SysInfo } from "../../lib/types";

  let {
    it,
    m,
    si,
    personas,
    index,
    count,
    onmove,
    oncompute,
  }: {
    it: PlanItem;
    m: Model | undefined;
    si: SysInfo | null;
    personas: Persona[];
    index: number;
    count: number;
    onmove: (to: number) => void;
    oncompute: (c: string) => void;
  } = $props();

  const uid = $derived("set-" + it.model_id.replace(/[^a-zA-Z0-9_-]/g, "_"));
  const compute = $derived(m ? effectiveCompute(m, it.compute) : it.compute);
  const canBuild = (c: string) => !m?.computes?.length || m.computes.includes(c);
  const COMPUTES: [string, string][] = [
    ["cpu", "CPU"],
    ["vulkan", "Vulkan GPU"],
    ["cuda", "CUDA GPU"],
  ];
  // "Docker (18 GB, Vulkan GPU)": both engines can have the GPU, so say which do.
  const engineNote = (e: string) => {
    const x = si?.engines?.find((y) => y.engine === e);
    if (!x) return "";
    const gpu = x.gpu ? `${computeName(x.gpu)} GPU` : "";
    if (!x.reachable) return ` (not running${gpu ? ", " + gpu : ""})`;
    const parts = [x.mem_gb ? `${Math.max(1, Math.floor(x.mem_gb))} GB` : "", gpu].filter(Boolean);
    return parts.length ? ` (${parts.join(", ")})` : "";
  };
  const ctxAuto = $derived(`Auto (${fmtTokens(ctxDefaultFor(compute))})`);
  const ctxMax = $derived(m && contextFor(m.id) ? `Max (${contextFor(m.id)})` : "Max for this model");
  const MEM = [0, 4, 8, 12, 16];
  const memOptions = $derived(MEM.includes(it.memory_gb) ? MEM : [...MEM, it.memory_gb].sort((a, b) => a - b));

  // Moving drops focus (the row's nodes move): keep it on the button, or on the
  // other one when this end of the list disables it.
  let upBtn: HTMLButtonElement | undefined = $state();
  let downBtn: HTMLButtonElement | undefined = $state();
  async function move(to: number, from: HTMLButtonElement | undefined) {
    onmove(to);
    await tick();
    (from && !from.disabled ? from : from === upBtn ? downBtn : upBtn)?.focus();
  }

  function fillPersona(e: Event) {
    const sel = e.currentTarget as HTMLSelectElement;
    const p = personas.find((x) => x.id === sel.value);
    if (p) it.system_prompt = p.prompt;
    sel.value = "";
  }
</script>

<div class="set">
  <div class="grid g4">
    <label class="f">
      <span>Runs on</span>
      <select bind:value={it.engine}>
        <option value="docker">Docker{engineNote("docker")}</option>
        <option value="podman">Podman{engineNote("podman")}</option>
      </select>
    </label>
    <label class="f">
      <span>Builds for</span>
      <select value={it.compute} onchange={(e) => oncompute(e.currentTarget.value)}>
        {#each COMPUTES as [c, label] (c)}
          <option value={c} disabled={!canBuild(c)}>{label}{canBuild(c) ? "" : " — none for this model"}</option>
        {/each}
      </select>
    </label>
    <label class="f">
      <span>Image name</span>
      <input bind:value={it.image_name} placeholder={"local-llm/" + it.model_id} spellcheck="false" autocomplete="off" />
    </label>
    <label class="f">
      <span>Tag</span>
      <input bind:value={it.tag} placeholder="latest" spellcheck="false" autocomplete="off" />
    </label>
  </div>

  <label class="f">
    <span>System prompt <small>optional — baked into the image to specialise it</small></span>
    <textarea rows="3" bind:value={it.system_prompt} placeholder="You are FinBot, a terse financial-data analyst. Always answer with strict JSON."></textarea>
  </label>
  <div class="grid g2">
    <label class="f">
      <span>When to apply it</span>
      <select bind:value={it.inject_mode}>
        <option value="missing">Only if the client sends none</option>
        <option value="always">Always, replacing the client's</option>
      </select>
    </label>
    <label class="f">
      <span>Fill from a persona</span>
      <select onchange={fillPersona} disabled={!personas.length}>
        <option value="">{personas.length ? "Choose a persona…" : "No personas yet"}</option>
        {#each personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>
  </div>

  <div class="grid g4">
    <label class="f">
      <span>Context window</span>
      <select bind:value={it.ctx_tier}>
        <option value="auto">{ctxAuto}</option>
        <option value="2048">2K</option>
        <option value="4096">4K</option>
        <option value="8192">8K</option>
        <option value="16384">16K</option>
        <option value="32768">32K</option>
        <option value="max">{ctxMax}</option>
      </select>
    </label>
    <label class="f">
      <span>Memory limit</span>
      <select value={String(it.memory_gb)} onchange={(e) => (it.memory_gb = parseFloat(e.currentTarget.value) || 0)}>
        {#each memOptions as g (g)}<option value={String(g)}>{g ? `${g} GB` : "No limit"}</option>{/each}
      </select>
    </label>
    <label class="f">
      <span>Local URL <small>optional</small></span>
      <span class="url"><input bind:value={it.route} placeholder="ai" spellcheck="false" autocomplete="off" aria-describedby="{uid}-url" /><span class="sfx" id="{uid}-url">.localhost</span></span>
    </label>
    <label class="f chk">
      <span>Restart with the engine</span>
      <span class="tgl"><input type="checkbox" class="tg" bind:checked={it.autostart} /><small>{it.autostart ? "On: once running, it comes back after a restart" : "Off"}</small></span>
    </label>
  </div>

  {#if canHarness(m)}
    <ToolsSetting {it} {m} />
  {/if}

  <div class="foot">
    <span class="hint">Builds run in plan order{count > 1 ? ` — this is #${index + 1} of ${count}` : ""}.</span>
    {#if count > 1}
      <button type="button" class="small" bind:this={upBtn} onclick={() => move(index - 1, upBtn)} disabled={index === 0}><Icon name="chevronUp" size={14} />Move up</button>
      <button type="button" class="small" bind:this={downBtn} onclick={() => move(index + 1, downBtn)} disabled={index === count - 1}><Icon name="chevronDown" size={14} />Move down</button>
    {/if}
  </div>
</div>

<style>
  .set { display: flex; flex-direction: column; gap: 12px; padding: 14px; border-radius: var(--r); background: var(--p2); border: 1px solid var(--ln); }
  .grid { display: grid; gap: 12px; }
  .g4 { grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .g2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .f { display: flex; flex-direction: column; gap: 5px; font-size: 12.5px; color: var(--mu); min-width: 0; }
  .f > span:first-child { font-weight: 500; }
  .f small { color: var(--fa); font-weight: 400; }
  .f select, .f input, .f textarea { font-size: 13px; background: var(--p1); }
  .url { display: flex; align-items: stretch; }
  .url input { border-top-right-radius: 0; border-bottom-right-radius: 0; min-width: 0; }
  .sfx {
    display: flex;
    align-items: center;
    padding: 0 9px;
    border: 1px solid var(--ln2);
    border-left: 0;
    border-radius: 0 var(--r) var(--r) 0;
    background: var(--p3);
    color: var(--mu);
    font: 12px var(--font-mono);
  }
  .tgl { display: flex; align-items: center; gap: 8px; min-height: 36px; }
  .tgl small { color: var(--mu); font-size: 12px; }
  .foot { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .foot .hint { margin-right: auto; font-size: 12.5px; }
  @media (max-width: 1100px) {
    .g4 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  @media (max-width: 560px) {
    .g4, .g2 { grid-template-columns: minmax(0, 1fr); }
  }
</style>
