<script lang="ts">
  // New / edit an ensemble: its name, one member per slot (chat, vision, speech
  // in and out, images, video) from the built images, the persona it plays and
  // that persona's voice, how it routes, and its memory budget. Saves the
  // persona's voice (when changed) to /api/personas, then the ensemble to
  // /api/ensembles.
  import { untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import VoiceEditor from "../personas/VoiceEditor.svelte";
  import { post, type ApiError } from "../../lib/api";
  import { ui } from "../../lib/ui.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { gb, computeName } from "../../lib/format";
  import { live } from "../../lib/live.svelte";
  import { catalog, personas, sysinfo, loadPersonas } from "../../lib/stores";
  import type { Ensemble, EnsembleMember, Persona, PersonaVoice } from "../../lib/types";
  import { candidates, memberOf, slug, SLOTS, type Candidate, type Slot } from "./util";

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
  const seedMembers = seed?.members ?? [];
  /** The first saved member per tool (an older ensemble could list two). */
  const oldMember = (tool: string): EnsembleMember | undefined => seedMembers.find((m) => m.tool === tool);

  let name = $state(seed?.name ?? "");
  let pick = $state<Record<string, string>>(Object.fromEntries(SLOTS.map((s) => [s.tool, oldMember(s.tool)?.image ?? ""])));
  let pin = $state<Record<string, boolean>>(Object.fromEntries(SLOTS.map((s) => [s.tool, !!oldMember(s.tool)?.pinned])));
  let routing = $state(seed?.routing || "agent");
  /** Built-in tools the brain may call besides the members (agent routing). */
  let agentTools = $state<string[]>(seed?.harness?.tools ?? []);
  let conductor = $state(seed?.conductor ?? "");
  let budget = $state(seed?.vram_budget_gb || 12);
  let compute = $state(seed?.compute || dockerGpu || "cpu");
  let personaId = $state(seed?.persona ?? "");
  let saving = $state(false);
  let error = $state("");
  let nameInput: HTMLInputElement | undefined = $state();

  $effect(() => {
    nameInput?.focus();
  });

  const cands = $derived(candidates(live.images, $catalog));
  const usable = (c: Candidate) => c.engine !== "podman";
  const options = (s: Slot) => cands.filter((c) => s.mods.includes(c.modality));
  const chatImages = $derived(cands.filter((c) => c.tool === "chat" && usable(c)));
  const candFor = (ref: string) => cands.find((c) => c.ref === ref);

  /** The member a slot holds now: its picked image as a candidate, else the saved member (image gone). */
  function slotMember(s: Slot): EnsembleMember | null {
    const ref = pick[s.tool];
    if (!ref) return null;
    const c = candFor(ref);
    if (c) return memberOf(s, c, pin[s.tool]);
    const old = seedMembers.find((m) => m.tool === s.tool && m.image === ref);
    return old ? { ...old, pinned: pin[s.tool] || undefined } : null;
  }
  const members = $derived(SLOTS.map(slotMember).filter((m): m is EnsembleMember => !!m));
  const gone = (s: Slot) => !!pick[s.tool] && !candFor(pick[s.tool]);

  // ---- persona and its voice ---------------------------------------------------------
  const persona = $derived<Persona | undefined>($personas.find((p) => p.id === personaId));
  const norm = (v: PersonaVoice | undefined): PersonaVoice =>
    !v?.kind ? { kind: "" } : v.kind === "builtin" ? { kind: "builtin", name: v.name ?? "" } : { kind: "clone", clip: v.clip ?? "" };
  let voice = $state<PersonaVoice>({ kind: "" });
  let voiceFor = "";
  $effect(() => {
    const p = persona;
    const id = p?.id ?? "";
    if (id === voiceFor) return;
    voiceFor = id;
    voice = norm(p?.voice);
  });
  const voiceDirty = $derived(!!persona && JSON.stringify(norm(voice)) !== JSON.stringify(norm(persona.voice)));
  const speak = $derived(members.find((m) => m.tool === "speak"));
  const speakCand = $derived(speak ? candFor(speak.image) : undefined);
  const speakName = $derived(speakCand?.modelName ?? speak?.model ?? "");
  const canClone = $derived(speak ? (speakCand ? speakCand.voiceClone : !!speak.voice_clone) : null);
  const needsTranscript = $derived(!!(speakCand ? speakCand.needsTranscript : speak?.clone_needs_transcript));
  const cloneOnly = $derived(!!(speakCand ? speakCand.cloneOnly : speak?.clone_only));
  const chatCand = $derived(pick.chat ? candFor(pick.chat) : undefined);

  // ---- what to tell them before saving --------------------------------------------
  const totalGB = $derived(members.reduce((s, m) => s + (m.vram_gb || 0), 0));
  const pinnedGB = $derived(members.filter((m) => m.pinned).reduce((s, m) => s + (m.vram_gb || 0), 0));
  const unknownFootprint = $derived(members.some((m) => !m.vram_gb));
  const warnings = $derived.by(() => {
    const w: string[] = [];
    if (!members.length) return w;
    if (!pick.chat) w.push("Pick a chat member: it answers everything that isn't an image, a picture to draw, a video or speech.");
    if (persona && voice.kind && !pick.speak) w.push(`${persona.name} has a voice, but the ensemble has no speech member to speak with it. Pick one under Speech.`);
    if (routing === "tool-calling" && !conductor) w.push("Tool-calling needs a conductor model; without one it routes with the heuristic.");
    if (routing === "agent" && chatCand && chatCand.runtime && chatCand.runtime !== "cpp")
      w.push(`Agent routing needs a llama.cpp chat model to call the others; ${chatCand.modelName} isn't one, so the ensemble routes with the heuristic.`);
    if (persona && cloneOnly && voice.kind !== "clone" && pick.speak)
      w.push(`${speakName || "The speech member"} has no built-in voices: give ${persona.name} a cloned voice, or pick another speech model.`);
    const b = Number(budget) || 0;
    for (const m of members) {
      if ((m.vram_gb || 0) > b) w.push(`${candFor(m.image)?.modelName ?? m.image} needs ~${gb(m.vram_gb, 0)}, more than the ${gb(b, 0)} budget: it can never start. Raise the budget.`);
    }
    if (pinnedGB > b) w.push(`The members you keep warm need ~${gb(pinnedGB, 0)}, more than the ${gb(b, 0)} budget. Keep fewer warm, or raise the budget.`);
    const pod = SLOTS.filter((s) => pick[s.tool] && candFor(pick[s.tool])?.engine === "podman");
    for (const s of pod) w.push(`The ${s.label.toLowerCase()} image is built for Podman; an ensemble starts its members with Docker. Build it for Docker.`);
    return w;
  });

  function voiceProblem(): string {
    if (voice.kind === "builtin" && !voice.name?.trim()) return "Name the persona's built-in voice, or pick another kind of voice.";
    if (voice.kind === "clone" && !voice.clip) return "Pick or record a voice clip for the persona, or pick another kind of voice.";
    return "";
  }

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
    if (!pick.chat) {
      error = "Pick a chat member: every ensemble needs one.";
      return;
    }
    const b = Number(budget);
    if (!Number.isFinite(b) || b <= 0) {
      error = "Give it a memory budget above 0 GB.";
      return;
    }
    if (persona && voiceDirty) {
      const vp = voiceProblem();
      if (vp) {
        error = vp;
        return;
      }
    }
    const body: Partial<Ensemble> = {
      id: ens?.id,
      name: n,
      package_mode: "orchestrated",
      routing,
      conductor,
      vram_budget_gb: b,
      engine: ens?.engine || "docker",
      compute,
      persona: personaId || "",
      members,
      harness: routing === "agent" && agentTools.length ? { tools: [...agentTools] } : null,
    };
    saving = true;
    try {
      // The voice belongs to the persona: save it there first.
      if (persona && voiceDirty) {
        await post<Persona>("/api/personas", { id: persona.id, name: persona.name, prompt: persona.prompt, voice: norm(voice) });
        await loadPersonas();
      }
      const saved = await post<Ensemble>("/api/ensembles", body);
      onsaved(saved);
    } catch (e) {
      const err = e as ApiError;
      error = err.status === 409 && err.message ? err.message : `It wasn't saved: ${err.message || "the factory didn't answer"}.`;
    } finally {
      saving = false;
    }
  }

  // Escape closes the editor while focus is in it (not when the palette is open).
  let sectionEl: HTMLElement | undefined = $state();
  function onKey(e: KeyboardEvent) {
    if (e.key !== "Escape" || ui.paletteOpen || !sectionEl?.contains(document.activeElement)) return;
    if ((e.target as HTMLElement)?.closest?.("audio")) return;
    e.preventDefault();
    onclose();
  }

  const AGENT_TOOLS: [string, string, string][] = [
    ["web_search", "Web search", "Search the web (DuckDuckGo unless set otherwise)"],
    ["fetch_url", "Fetch pages", "Read a web page by its address"],
    ["files", "Files", "Read and write files in the ensemble's /workspace"],
    ["skills", "Skills", "Use the skills folder baked into the image"],
  ];

  const optText = (c: Candidate) =>
    `${c.name.replace(/^(localhost\/)?local-llm\//, "")} · ${computeName(c.compute)}${c.footprint != null ? ` · ~${gb(c.footprint, 0)}` : ""}${usable(c) ? "" : " · Podman, can't be used"}`;
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

  <!-- Members, one per slot -->
  <fieldset class="grp">
    <legend>Members</legend>
    <p class="hint">One built image per job. The Conductor starts each the first time it's needed; “Keep warm” starts it with the ensemble and never stops it to make room.</p>
    {#if !live.loaded.images}
      <p class="empty"><span class="spin" aria-hidden="true"></span> Reading your images…</p>
    {:else if !cands.length && !seedMembers.length}
      <p class="empty">
        No built images yet. <a href={href("catalog")}>Build a model</a> first: a chat model, plus any of vision, speech,
        image or video.
      </p>
    {:else}
      <ul class="slots">
        {#each SLOTS as s (s.tool)}
          {@const opts = options(s)}
          {@const old = seedMembers.find((m) => m.tool === s.tool && m.image === pick[s.tool])}
          <li class:set={!!pick[s.tool]}>
            <span class="mi" aria-hidden="true"><Icon name={modMeta(s.mods[0]).icon} size={15} /></span>
            <span class="sl">
              <label for="{uid}-slot-{s.tool}">{s.label}{#if s.tool === "chat"}<em> · required</em>{/if}</label>
              <small>{s.help}</small>
            </span>
            <span class="pk">
              <select id="{uid}-slot-{s.tool}" bind:value={pick[s.tool]} class:bad={gone(s)}>
                <option value="">{opts.length ? "None" : `None built yet`}</option>
                {#each opts as c (c.ref)}<option value={c.ref} disabled={!usable(c) && pick[s.tool] !== c.ref}>{optText(c)}</option>{/each}
                {#if gone(s)}<option value={pick[s.tool]}>{pick[s.tool]} · not built any more</option>{/if}
              </select>
              {#if gone(s)}
                <small class="gonet">This image isn't on Docker any more ({old?.model || "build it again"}). Pick another, or rebuild it.</small>
              {/if}
            </span>
            <label class="kw" title="Start it with the ensemble and never stop it to make room">
              <input type="checkbox" class="tg" bind:checked={pin[s.tool]} disabled={!pick[s.tool]} />
              <span>Keep warm</span>
            </label>
          </li>
        {/each}
      </ul>
    {/if}
  </fieldset>

  <!-- Persona and voice -->
  <fieldset class="grp">
    <legend>Persona and voice</legend>
    <div class="fl">
      <label for="{uid}-persona">Persona</label>
      <select id="{uid}-persona" bind:value={personaId}>
        <option value="">None: no system prompt, the speech member's default voice</option>
        {#each $personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
        {#if personaId && !persona}<option value={personaId}>A deleted persona ({personaId})</option>{/if}
      </select>
      {#if personaId && !persona}
        <p class="hint warn">That persona was deleted. Pick another, or none.</p>
      {:else if persona}
        <p class="hint">Its prompt comes first in every chat. <a href={href("personas", persona.id)}>Edit {persona.name}</a></p>
      {:else}
        <p class="hint">A persona gives the ensemble a character to play and a voice to speak with. <a href={href("personas", "new")}>Make one</a></p>
      {/if}
    </div>
    {#if persona}
      <div class="voice">
        <span class="vl">{persona.name}'s voice</span>
        <VoiceEditor bind:voice ensembleId={ens?.id ?? ""} speakModel={speak?.model ?? ""} {speakName} {canClone} {needsTranscript} {cloneOnly} defaultClipName={`${persona.name}'s voice`} />
        <p class="hint">
          The voice is saved with the persona{voiceDirty ? " when you save the ensemble" : ""}: every ensemble that uses {persona.name} speaks with it.
        </p>
      </div>
    {/if}
  </fieldset>

  <!-- Routing -->
  <fieldset class="grp">
    <legend>Routing</legend>
    <div class="segc" role="radiogroup" aria-label="Routing">
      <label class:on={routing === "agent"}>
        <input type="radio" name="{uid}-routing" value="agent" bind:group={routing} />
        <b>Agent</b><span>The chat model calls the others</span>
      </label>
      <label class:on={routing === "heuristic"}>
        <input type="radio" name="{uid}-routing" value="heuristic" bind:group={routing} />
        <b>Heuristic</b><span>Fast, no extra model</span>
      </label>
      <label class:on={routing === "tool-calling"}>
        <input type="radio" name="{uid}-routing" value="tool-calling" bind:group={routing} />
        <b>Tool-calling</b><span>A small model picks the member</span>
      </label>
    </div>
    {#if routing === "agent"}
      <p class="hint">
        The chat member is the brain: it answers, and when you ask for a picture, a video or speech it calls that member as a
        tool, then tells you what it made. If its model can't call tools, the ensemble routes with the heuristic.
      </p>
      <div class="fl">
        <span class="lbl" id="{uid}-tools">Built-in tools it may also use</span>
        <div class="tools" role="group" aria-labelledby="{uid}-tools">
          {#each AGENT_TOOLS as [id, label, help] (id)}
            <label class="kw" title={help}>
              <input type="checkbox" class="tg" value={id} bind:group={agentTools} />
              <span>{label}</span>
            </label>
          {/each}
        </div>
        <p class="hint">Off by default. Web search and fetching pages reach the internet; files stay in the ensemble's own /workspace.</p>
      </div>
    {:else if routing === "tool-calling"}
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
    <div class="two">
      <div class="fl">
        <label for="{uid}-budget">Memory budget (GB)</label>
        <input id="{uid}-budget" type="number" min="1" max="512" step="1" bind:value={budget} />
        <p class="hint">What its members may use at once: VRAM for GPU members, RAM for CPU ones.</p>
      </div>
      <div class="fl">
        <label for="{uid}-comp">Compute for older member images</label>
        <select id="{uid}-comp" bind:value={compute}>
          <option value="cpu">CPU</option>
          <option value="cuda">NVIDIA GPU (CUDA)</option>
          <option value="vulkan">GPU (Vulkan)</option>
        </select>
        <p class="hint">Each member runs on the compute its image was built for; this is only for images that don't say.</p>
      </div>
    </div>
    {#if members.length}
      <p class="hint meter">
        Members need {unknownFootprint ? "at least " : ""}~{gb(totalGB, 0)} in all, against a {gb(Number(budget) || 0, 0)} budget.
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
  .hint.warn { color: var(--wa); }

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

  .slots { list-style: none; margin: 8px 0 0; padding: 0; border: 1px solid var(--ln); border-radius: 11px; overflow: hidden; }
  .slots li {
    display: grid;
    grid-template-columns: 30px minmax(150px, 0.8fr) minmax(0, 1.4fr) auto;
    gap: 12px;
    align-items: center;
    padding: 9px 12px;
  }
  .slots li + li { border-top: 1px solid var(--ln); }
  .slots li.set .mi { background: var(--prs); color: var(--pr); }
  .mi { width: 30px; height: 30px; border-radius: 8px; background: var(--p3); display: grid; place-items: center; color: var(--mu); }
  .sl { display: flex; flex-direction: column; min-width: 0; }
  .sl label { font-size: 13px; font-weight: 600; color: var(--ink); }
  .sl em { font-style: normal; font-weight: 500; color: var(--fa); font-size: 12px; }
  .sl small { color: var(--mu); font-size: 12px; line-height: 1.35; }
  .pk { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .pk select { font-size: 12.5px; padding: 6px 9px; min-width: 0; width: 100%; }
  .pk select.bad { border-color: var(--wa); }
  .gonet { color: var(--wa); font-size: 11.5px; }
  .kw { display: inline-flex; align-items: center; gap: 7px; font-size: 12px; color: var(--mu); white-space: nowrap; cursor: pointer; }
  .kw:has(input:disabled) { opacity: 0.5; cursor: default; }

  .voice { display: flex; flex-direction: column; gap: 8px; margin-top: 12px; padding: 12px; border: 1px solid var(--ln); border-radius: 11px; }
  .vl { font-size: 12px; font-weight: 600; color: var(--ink); }

  .segc { display: flex; padding: 3px; border: 1px solid var(--ln2); border-radius: 10px; gap: 2px; max-width: 680px; }
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

  .lbl { font-size: 12px; color: var(--fa); font-weight: 500; }
  .tools { display: flex; flex-wrap: wrap; gap: 8px 18px; }

  .two { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.2fr); gap: 12px; }
  .two .fl { margin-top: 4px; }
  .meter { margin-top: 8px; }

  .callout { margin-top: 10px; }
  .callout p { margin: 0; color: var(--ink); }
  .acts { display: flex; gap: 8px; margin-top: 16px; flex-wrap: wrap; }
  .spin.dark { width: 14px; height: 14px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }

  @media (max-width: 1100px) {
    .slots li { grid-template-columns: 30px minmax(0, 1fr) auto; row-gap: 8px; }
    .pk { grid-column: 2 / 4; grid-row: 2; }
    .kw { grid-column: 3; grid-row: 1; }
  }
  @media (max-width: 760px) {
    .two { grid-template-columns: minmax(0, 1fr); }
    .slots li { grid-template-columns: 30px minmax(0, 1fr); }
    .pk { grid-column: 2; grid-row: 2; }
    .kw { grid-column: 2; grid-row: 3; justify-self: start; }
  }
</style>
