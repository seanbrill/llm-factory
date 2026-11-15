<script lang="ts">
  // New / edit a persona: a name, a system prompt and a voice (VoiceEditor).
  // Saves to /api/personas.
  import { untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import VoiceEditor from "./VoiceEditor.svelte";
  import { post, type ApiError } from "../../lib/api";
  import { ui } from "../../lib/ui.svelte";
  import type { Persona, PersonaVoice } from "../../lib/types";
  import { slug } from "../ensembles/util";

  let {
    persona,
    existing,
    onclose,
    onsaved,
  }: {
    persona: Persona | null;
    existing: Persona[];
    onclose: () => void;
    onsaved: (p: Persona) => void;
  } = $props();

  const uid = $props.id();
  const seed = untrack(() => persona);
  const isNew = !seed;
  let name = $state(seed?.name ?? "");
  let prompt = $state(seed?.prompt ?? "");
  let voice = $state<PersonaVoice>(seed?.voice ? { ...seed.voice } : { kind: "" });
  let saving = $state(false);
  let error = $state("");
  let nameInput: HTMLInputElement | undefined = $state();
  let promptInput: HTMLTextAreaElement | undefined = $state();
  let sectionEl: HTMLElement | undefined = $state();

  $effect(() => {
    nameInput?.focus();
  });

  const words = $derived(prompt.trim() ? prompt.trim().split(/\s+/).length : 0);
  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

  async function save() {
    error = "";
    const n = name.trim();
    const p = prompt.trim();
    if (!n) {
      error = "Name the persona.";
      nameInput?.focus();
      return;
    }
    if (!p) {
      error = "Write its system prompt.";
      promptInput?.focus();
      return;
    }
    if (isNew) {
      const clash = existing.find((x) => x.id === slug(n));
      if (slug(n) && clash) {
        error = `There's already a persona called “${clash.name}”. Pick another name, or edit that one.`;
        nameInput?.focus();
        return;
      }
    }
    if (voice.kind === "builtin" && !voice.name?.trim()) {
      error = "Name the built-in voice, or pick another kind of voice.";
      return;
    }
    if (voice.kind === "clone" && !voice.clip) {
      error = "Pick or record a voice clip, or pick another kind of voice.";
      return;
    }
    const body: Partial<Persona> = { name: n, prompt: p, voice: voice.kind ? voice : { kind: "" } };
    if (seed) body.id = seed.id;
    saving = true;
    try {
      const saved = await post<Persona>("/api/personas", body);
      onsaved(saved);
    } catch (e) {
      const err = e as ApiError;
      // A name that clashes comes back as 409 with the sentence to show.
      error = err.status === 409 && err.message ? err.message : `It wasn't saved: ${err.message || "the factory didn't answer"}.`;
      if (err.status === 409) nameInput?.focus();
    } finally {
      saving = false;
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape" && !ui.paletteOpen && sectionEl?.contains(document.activeElement)) {
      e.preventDefault();
      onclose();
    } else if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && sectionEl?.contains(document.activeElement)) {
      e.preventDefault();
      save();
    }
  }
</script>

<svelte:window onkeydown={onKey} />

<section class="card editor" aria-labelledby="{uid}-title" bind:this={sectionEl}>
  <div class="hd">
    <h2 id="{uid}-title">{isNew ? "New persona" : `Edit “${seed?.name}”`}</h2>
    <div class="sp">
      <button class="small ghost icon" onclick={onclose} aria-label="Close without saving" title="Close (Esc)">
        <Icon name="xmark" size={15} />
      </button>
    </div>
  </div>

  <div class="fl">
    <label for="{uid}-name">Name</label>
    <input id="{uid}-name" type="text" bind:this={nameInput} bind:value={name} placeholder="Terse analyst" autocomplete="off" />
  </div>
  <div class="fl">
    <div class="lrow">
      <label for="{uid}-prompt">System prompt</label>
      <span class="count" aria-live="polite">{words ? `${words} word${words === 1 ? "" : "s"}` : ""}</span>
    </div>
    <textarea
      id="{uid}-prompt"
      rows="9"
      bind:this={promptInput}
      bind:value={prompt}
      placeholder="You are a terse data analyst. Answer in at most three sentences; use tables for numbers."
      aria-describedby="{uid}-tip"></textarea>
    <p class="hint" id="{uid}-tip">
      Say who the model is, how it should answer, and what it must never do. Short prompts leave more room for the
      conversation.
    </p>
  </div>

  <fieldset class="grp">
    <legend>Voice</legend>
    <p class="hint">How it sounds when an ensemble with this persona speaks. Images built with the persona only take its prompt.</p>
    <VoiceEditor bind:voice defaultClipName={name.trim() ? `${name.trim()}'s voice` : ""} />
  </fieldset>

  {#if error}
    <div class="callout er" role="alert"><Icon name="errc" size={16} /><span><b>{error}</b></span></div>
  {/if}

  <div class="acts">
    <button class="primary" onclick={save} disabled={saving}>
      {#if saving}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="check" size={15} />{/if}Save persona
    </button>
    <button class="ghost" onclick={onclose}>Cancel</button>
    <span class="kh"><span class="kbd">{isMac ? "⌘" : "Ctrl"}</span> <span class="kbd">Enter</span> saves</span>
  </div>
</section>

<style>
  .editor { display: flex; flex-direction: column; gap: 4px; border-color: color-mix(in srgb, var(--pr) 40%, var(--ln)); max-width: 920px; }
  .hd { margin-bottom: 4px; }
  .fl { display: flex; flex-direction: column; gap: 5px; margin-top: 8px; }
  .fl label { font-size: 12px; color: var(--fa); font-weight: 500; }
  .fl input { font-size: 13.5px; padding: 8px 11px; }
  .lrow { display: flex; justify-content: space-between; align-items: baseline; }
  .count { font-size: 11.5px; color: var(--fa); }
  textarea { font: 13px/1.55 var(--font-body); min-height: 150px; }
  .hint { font-size: 12px; line-height: 1.45; }
  .grp { border: 0; padding: 0; margin: 14px 0 0; min-width: 0; display: flex; flex-direction: column; gap: 6px; }
  .grp legend {
    padding: 0;
    margin-bottom: 2px;
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    font-weight: 500;
  }
  .callout { margin-top: 10px; }
  .acts { display: flex; gap: 8px; margin-top: 14px; align-items: center; flex-wrap: wrap; }
  .kh { margin-left: auto; color: var(--fa); font-size: 12px; }
  .spin.dark { width: 14px; height: 14px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
  @media (max-width: 760px) {
    .kh { display: none; }
  }
</style>
