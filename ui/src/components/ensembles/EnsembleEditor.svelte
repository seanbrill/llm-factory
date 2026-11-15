<script lang="ts">
  // New / edit an ensemble: its name, the built images it can use, how it routes,
  // and what it runs on. Saves to /api/ensembles.
  import { untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import { post } from "../../lib/api";
  import { ui } from "../../lib/ui.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { gb, computeName, computeChip, engineName } from "../../lib/format";
  import { live } from "../../lib/live.svelte";
  import { catalog, sysinfo } from "../../lib/stores";
  import type { Ensemble, EnsembleMember } from "../../lib/types";
  import { candidates, slug, TOOL_TEXT, type Candidate } from "./util";

  let {
    ens,
    existing,
    onclose,
    onsaved,
  }: {
    ens: Ensemble | null;
    existing: Ensemble[];
    onclose: () => void;
    onsaved: (e: Ensemble) => void;
  } = $props();

  const uid = $props.id();
  const isNew = $derived(!ens);

  // ---- form state, seeded once from the ensemble being edited -------------------
  const dockerGpu = untrack(() => $sysinfo?.engines?.find((e) => e.engine === "docker")?.gpu || "");
  const seed = untrack(() => ens);
  let name = $state(seed?.name ?? "");
  let chosen = $state<string[]>((seed?.members ?? []).map((m) => m.image));
  let routing = $state(seed?.routing || "heuristic");
  let conductor = $state(seed?.conductor ?? "");
  let budget = $state(seed?.vram_budget_gb || 12);
  let compute = $state(seed?.compute || dockerGpu || "cpu");
  let mode = $state(seed?.package_mode || "orchestrated");
  let saving = $state(false);
  let error = $state("");
  let nameInput: HTMLInputElement | undefined = $state();

  $effect(() => {
    nameInput?.focus();
  });

  const cands = $derived(candidates(live.images, $catalog));
  const usable = (c: Candidate) => c.engine !== "podman";
  // Members whose image is gone stay listed so they can be removed.
  const missing = $derived(
    (seed?.members ?? []).filter((m) => !cands.some((c) => c.ref === m.image)),
  );
  const chatImages = $derived(cands.filter((c) => c.tool === "chat" && usable(c)));

  function memberFor(ref: string): EnsembleMember | null {
    const c = cands.find((x) => x.ref === ref);
    const old = seed?.members?.find((m) => m.image === ref);
    if (c) return { tool: c.tool, modality: c.modality, image: c.ref, vram_gb: old?.vram_gb || c.vram || 6 };
    return old ?? null;
  }
  const members = $derived(chosen.map(memberFor).filter((m): m is EnsembleMember => !!m));

  function toggle(ref: string, on: boolean) {
    chosen = on ? [...chosen, ref] : chosen.filter((r) => r !== ref);
  }

  // ---- what to tell them before saving --------------------------------------------
  const totalGB = $derived(members.reduce((s, m) => s + (m.vram_gb || 0), 0));
  const unknownVRAM = $derived(chosen.some((r) => {
    const c = cands.find((x) => x.ref === r);
    return c ? c.vram == null && !seed?.members?.find((m) => m.image === r)?.vram_gb : false;
  }));
  const warnings = $derived.by(() => {
    const w: string[] = [];
    if (!members.length) return w;
    const tools = members.map((m) => m.tool);
    if (!tools.includes("chat")) w.push("No chat model: a request that isn't about an image, a picture to draw, a video or speech has nowhere to go.");
    const dup = [...new Set(tools.filter((t, i) => tools.indexOf(t) !== i))];
    for (const t of dup) w.push(`More than one member does “${TOOL_TEXT[t] ?? t}”: only the first one in the list is used.`);
    if (tools.includes("transcribe")) w.push("The Conductor doesn't route to speech-to-text yet, so that member isn't used.");
    if (routing === "tool-calling" && !conductor) w.push("Tool-calling needs a conductor model; without one it routes with the heuristic.");
    const big = Math.max(0, ...members.map((m) => m.vram_gb || 0));
    if (big > budget) w.push(`The budget (${gb(budget, 0)}) is smaller than one member (${gb(big, 0)}): that member can never start within it.`);
    if (mode === "embedded") w.push("Embedded isn't built yet: an embedded ensemble doesn't start its members, so only ones already running would answer.");
    return w;
  });

  async function save() {
    error = "";
    const n = name.trim();
    if (!n) {
      error = "Name the ensemble.";
      nameInput?.focus();
      return;
    }
    if (!slug(n)) {
      error = "Use at least one letter or digit in the name.";
      nameInput?.focus();
      return;
    }
    if (isNew && existing.some((e) => e.id === slug(n))) {
      error = `There's already an ensemble called “${existing.find((e) => e.id === slug(n))?.name}”. Pick another name, or edit that one.`;
      nameInput?.focus();
      return;
    }
    if (!members.length) {
      error = "Pick at least one member.";
      return;
    }
    const b = Number(budget);
    if (!Number.isFinite(b) || b <= 0) {
      error = "Give it a VRAM budget above 0 GB.";
      return;
    }
    const body: Partial<Ensemble> = {
      id: ens?.id,
      name: n,
      package_mode: mode,
      routing,
      conductor,
      vram_budget_gb: b,
      engine: ens?.engine || "docker",
      compute,
      members,
    };
    saving = true;
    try {
      const saved = await post<Ensemble>("/api/ensembles", body);
      onsaved(saved);
    } catch (e) {
      error = `It wasn't saved: ${(e as Error).message || "the factory didn't answer"}.`;
    } finally {
      saving = false;
    }
  }

  // Escape closes the editor while focus is in it (not when the palette is open).
  let sectionEl: HTMLElement | undefined = $state();
  function onKey(e: KeyboardEvent) {
    if (e.key !== "Escape" || ui.paletteOpen || !sectionEl?.contains(document.activeElement)) return;
    e.preventDefault();
    onclose();
  }
</script>

<svelte:window onkeydown={onKey} />

<section class="card editor" aria-labelledby="{uid}-title" bind:this={sectionEl}>
  <div class="hd">
    <h2 id="{uid}-title">{isNew ? "New ensemble" : `Edit “${ens?.name}”`}</h2>
    <div class="sp">
      <button class="small ghost icon" onclick={onclose} aria-label="Close without saving" title="Close (Esc)">
        <Icon name="xmark" size={15} />
      </button>
    </div>
  </div>

  <div class="fl">
    <label for="{uid}-name">Name</label>
    <input
      id="{uid}-name"
      type="text"
      bind:this={nameInput}
      bind:value={name}
      placeholder="Everyday helper"
      autocomplete="off" />
    {#if isNew && slug(name)}
      <p class="hint">Builds into <span class="mono">local-llm/ens-{slug(name)}</span></p>
    {/if}
  </div>

  <!-- Members -->
  <fieldset class="grp">
    <legend>Members</legend>
    <p class="hint">The built images it can use. Each one backs a tool; the Conductor starts it the first time it's needed.</p>
    {#if !live.loaded.images}
      <p class="empty"><span class="spin" aria-hidden="true"></span> Reading your images…</p>
    {:else if !cands.length && !missing.length}
      <p class="empty">
        No built images yet. <a href={href("catalog")}>Build a model</a> first: a chat model, plus any of vision, image,
        video or speech.
      </p>
    {:else}
      <ul class="mem">
        {#each cands as c (c.ref)}
          {@const on = chosen.includes(c.ref)}
          <li class:off={!usable(c)}>
            <label>
              <input
                type="checkbox"
                checked={on}
                disabled={!usable(c) && !on}
                onchange={(e) => toggle(c.ref, (e.currentTarget as HTMLInputElement).checked)} />
              <span class="mi" title={modMeta(c.modality).label}><Icon name={modMeta(c.modality).icon} size={15} /></span>
              <span class="mt">
                <span class="mono nm">{c.name}</span>
                <small>
                  {TOOL_TEXT[c.tool] ?? c.tool}
                  {#if !usable(c)} · built for Podman; an ensemble starts its members with Docker{/if}
                </small>
              </span>
              <span class="tags">
                {#if c.compute}<span class={computeChip(c.compute)}>{computeName(c.compute)}</span>{/if}
                {#if c.engine === "podman"}<span class="chip">{engineName(c.engine)}</span>{/if}
              </span>
              <span class="vr mono" title={c.vram == null ? "The catalog gives no VRAM figure for it: it counts as 6 GB toward the budget" : "VRAM it needs (from the catalog)"}>
                {c.vram == null ? "—" : `~${gb(c.vram, 0)}`}
              </span>
            </label>
          </li>
        {/each}
        {#each missing as m (m.image)}
          {@const on = chosen.includes(m.image)}
          <li class="gone">
            <label>
              <input type="checkbox" checked={on} onchange={(e) => toggle(m.image, (e.currentTarget as HTMLInputElement).checked)} />
              <span class="mi"><Icon name={modMeta(m.modality).icon} size={15} /></span>
              <span class="mt">
                <span class="mono nm">{m.image}</span>
                <small>{TOOL_TEXT[m.tool] ?? m.tool} · this image isn't on Docker any more</small>
              </span>
              <span class="vr mono">{m.vram_gb ? `~${gb(m.vram_gb, 0)}` : "—"}</span>
            </label>
          </li>
        {/each}
      </ul>
    {/if}
  </fieldset>

  <!-- Routing -->
  <fieldset class="grp">
    <legend>Routing</legend>
    <div class="segc" role="radiogroup" aria-label="Routing">
      <label class:on={routing === "heuristic"}>
        <input type="radio" name="{uid}-routing" value="heuristic" bind:group={routing} />
        <b>Heuristic</b><span>Fast, no extra model</span>
      </label>
      <label class:on={routing === "tool-calling"}>
        <input type="radio" name="{uid}-routing" value="tool-calling" bind:group={routing} />
        <b>Tool-calling</b><span>A small model picks the tool</span>
      </label>
    </div>
    {#if routing === "tool-calling"}
      <div class="fl">
        <label for="{uid}-cond">Conductor model</label>
        <select id="{uid}-cond" bind:value={conductor}>
          <option value="">None: route with the heuristic</option>
          {#each chatImages as c (c.ref)}<option value={c.ref}>{c.name}</option>{/each}
          {#if conductor && !chatImages.some((c) => c.ref === conductor)}<option value={conductor}>{conductor}</option>{/if}
        </select>
        <p class="hint">A chat image; small ones answer fastest. If it can't decide, the heuristic choice stands.</p>
      </div>
    {/if}
  </fieldset>

  <!-- Runs on -->
  <fieldset class="grp">
    <legend>Runs on</legend>
    <div class="three">
      <div class="fl">
        <label for="{uid}-comp">Compute for its members</label>
        <select id="{uid}-comp" bind:value={compute}>
          <option value="cpu">CPU</option>
          <option value="cuda">NVIDIA GPU (CUDA)</option>
          <option value="vulkan">GPU (Vulkan)</option>
        </select>
      </div>
      <div class="fl">
        <label for="{uid}-budget">VRAM budget (GB)</label>
        <input id="{uid}-budget" type="number" min="1" max="80" step="1" bind:value={budget} />
      </div>
      <div class="fl">
        <label for="{uid}-mode">Package</label>
        <select id="{uid}-mode" bind:value={mode}>
          <option value="orchestrated">Orchestrated: starts members as containers</option>
          <option value="embedded">Embedded: everything in one image (not built yet)</option>
        </select>
      </div>
    </div>
    {#if members.length}
      <p class="hint meter">
        Members need {unknownVRAM ? "at least " : ""}~{gb(totalGB, 0)} in all, against a {gb(Number(budget) || 0, 0)} budget.
        {#if totalGB > budget}The Conductor stops the least recently used member to start another.{:else}They all fit at once.{/if}
      </p>
    {/if}
  </fieldset>

  {#each warnings as w (w)}
    <div class="callout wa"><Icon name="warn" size={16} /><span><p>{w}</p></span></div>
  {/each}

  {#if error}
    <div class="callout er" role="alert"><Icon name="errc" size={16} /><span><b>{error}</b></span></div>
  {/if}

  <div class="acts">
    <button class="primary" onclick={save} disabled={saving}>
      {#if saving}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="check" size={15} />{/if}Save ensemble
    </button>
    <button class="ghost" onclick={onclose}>Cancel</button>
  </div>
</section>

<style>
  .editor { display: flex; flex-direction: column; gap: 4px; border-color: color-mix(in srgb, var(--pr) 40%, var(--ln)); }
  .hd { margin-bottom: 4px; }
  .fl { display: flex; flex-direction: column; gap: 5px; margin-top: 8px; min-width: 0; }
  .fl label { font-size: 12px; color: var(--fa); font-weight: 500; }
  .fl input, .fl select { font-size: 13px; padding: 7px 10px; }
  .hint { font-size: 12px; line-height: 1.45; }
  .hint .mono { font-size: 11.5px; color: var(--ink); }

  .grp { border: 0; padding: 0; margin: 14px 0 0; min-width: 0; }
  .grp legend {
    padding: 0;
    margin-bottom: 4px;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    font-weight: 500;
  }
  .empty { padding: 8px 0; font-size: 12.5px; display: flex; align-items: center; gap: 8px; }
  .empty .spin { width: 14px; height: 14px; }

  .mem { list-style: none; margin: 8px 0 0; padding: 0; border: 1px solid var(--ln); border-radius: 11px; overflow: hidden; }
  .mem li + li { border-top: 1px solid var(--ln); }
  .mem label {
    display: grid;
    grid-template-columns: 16px 30px minmax(0, 1fr) auto 52px;
    gap: 10px;
    align-items: center;
    padding: 8px 12px;
    cursor: pointer;
  }
  .mem label:hover { background: var(--p2); }
  .mem input { margin: 0; width: 15px; height: 15px; }
  .mem li.off label { cursor: not-allowed; }
  .mem li.off .mt, .mem li.off .mi { opacity: 0.6; }
  .mem li.gone .nm { color: var(--wa); }
  .mi { width: 30px; height: 30px; border-radius: 8px; background: var(--p3); display: grid; place-items: center; color: var(--mu); }
  .mt { min-width: 0; display: flex; flex-direction: column; }
  .nm { color: var(--ink); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mt small { color: var(--mu); font-size: 12px; }
  .tags { display: flex; gap: 4px; }
  .vr { color: var(--mu); font-size: 12px; text-align: right; }

  .segc { display: flex; padding: 3px; border: 1px solid var(--ln2); border-radius: 10px; gap: 2px; max-width: 520px; }
  .segc label {
    flex: 1;
    padding: 6px 10px;
    border-radius: 7px;
    font-size: 12.5px;
    color: var(--mu);
    line-height: 1.3;
    cursor: pointer;
    position: relative;
  }
  .segc label b { display: block; color: inherit; font-weight: 600; }
  .segc label.on { background: var(--p3); color: var(--ink); }
  .segc input { position: absolute; opacity: 0; width: 1px; height: 1px; pointer-events: none; }
  .segc label:has(input:focus-visible) { outline: 2px solid var(--pr); outline-offset: 1px; }

  .three { display: grid; grid-template-columns: minmax(0, 1fr) 140px minmax(0, 1.4fr); gap: 12px; }
  .three .fl { margin-top: 4px; }
  .meter { margin-top: 8px; }

  .callout { margin-top: 10px; }
  .callout p { margin: 0; color: var(--ink); }
  .acts { display: flex; gap: 8px; margin-top: 16px; flex-wrap: wrap; }
  .spin.dark { width: 14px; height: 14px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }

  @media (max-width: 760px) {
    .three { grid-template-columns: minmax(0, 1fr); }
    .mem label { grid-template-columns: 16px 30px minmax(0, 1fr) auto; }
    .tags { display: none; }
  }
</style>
