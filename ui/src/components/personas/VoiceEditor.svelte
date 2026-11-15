<script lang="ts">
  // A persona's voice: the speech member's default, one of a speech model's
  // built-in voices, or a voice cloned from a short clip. Clips are recorded or
  // uploaded here (after the consent box is ticked), converted to a mono WAV in
  // the browser, saved to config/voices, and transcribed by speech-to-text when
  // one is running; the transcript stays editable. Used by the persona editor
  // and, for the chosen persona, by the ensemble editor (which passes the
  // ensemble and its speech member).
  import { onDestroy, untrack } from "svelte";
  import Icon from "../Icon.svelte";
  import { api, post } from "../../lib/api";
  import { voices, loadVoices, catalog } from "../../lib/stores";
  import { ttsSpec, knownVoices } from "../../lib/meta";
  import { startRecording, toWav, audioError, elapsed, CLIP_RATE, type Recording } from "../../lib/audio";
  import type { PersonaVoice, VoiceClip } from "../../lib/types";

  let {
    voice = $bindable({ kind: "" }),
    ensembleId = "",
    speakModel = "",
    speakName = "",
    canClone = null,
    defaultClipName = "",
  }: {
    voice?: PersonaVoice;
    ensembleId?: string; // transcribe with this ensemble's speech-to-text member
    speakModel?: string; // catalog id of the speech member, when there is one
    speakName?: string; // its display name
    canClone?: boolean | null; // whether that member clones voices (null: no member to judge by)
    defaultClipName?: string;
  } = $props();

  const uid = $props.id();
  const MIN_S = 3;
  const MAX_S = 30;

  // ---- the saved clips --------------------------------------------------------------
  let listErr = $state("");
  let loaded = $state(false);
  async function reload() {
    try {
      await loadVoices();
      listErr = "";
    } catch (e) {
      listErr = (e as Error).message || "The factory didn't answer.";
    } finally {
      loaded = true;
    }
  }
  $effect(() => {
    untrack(() => reload());
  });

  const kind = $derived(voice?.kind ?? "");
  const clip = $derived(kind === "clone" ? $voices.find((c) => c.id === voice.clip) : undefined);
  const missingClip = $derived(kind === "clone" && !!voice.clip && loaded && !listErr && !clip);

  function setKind(k: PersonaVoice["kind"]) {
    if (k === "") voice = { kind: "" };
    else if (k === "builtin") voice = { kind: "builtin", name: voice.kind === "builtin" ? voice.name : (spec.voices[0]?.[0] ?? "") };
    else voice = { kind: "clone", clip: voice.kind === "clone" ? voice.clip : ($voices[0]?.id ?? "") };
  }

  // ---- built-in voices --------------------------------------------------------------
  const spec = $derived(ttsSpec(speakModel));
  const modelName = (id: string) => $catalog.find((m) => m.id === id)?.name ?? id;
  const all = knownVoices();
  const offList = $derived(kind === "builtin" && !!voice.name && spec.voices.length > 0 && !spec.voices.some(([v]) => v === voice.name));

  // ---- the transcript of the chosen clip ----------------------------------------------
  let transcript = $state("");
  let transcriptFor = "";
  $effect(() => {
    const c = clip;
    if (!c) return;
    if (c.id !== transcriptFor) {
      transcriptFor = c.id;
      transcript = c.transcript ?? "";
    }
  });
  const transcriptDirty = $derived(!!clip && transcript.trim() !== (clip.transcript ?? "").trim());
  let savingText = $state(false);
  let textErr = $state("");
  async function saveTranscript() {
    if (!clip) return;
    savingText = true;
    textErr = "";
    try {
      await post<VoiceClip>("/api/voices/update", { id: clip.id, transcript: transcript.trim() });
      await reload();
    } catch (e) {
      textErr = `The transcript wasn't saved: ${(e as Error).message || "the factory didn't answer"}.`;
    } finally {
      savingText = false;
    }
  }

  let transcribing = $state(false);
  let transcribeErr = $state("");
  async function transcribe(id: string) {
    transcribing = true;
    transcribeErr = "";
    try {
      const r = await post<{ text: string; source?: string }>("/api/voices/transcribe", { id, ensemble: ensembleId || undefined });
      const text = (r.text || "").trim();
      if (!text) {
        transcribeErr = "Speech-to-text found no words in the clip. Type what you said, or record it again more clearly.";
        return;
      }
      await post<VoiceClip>("/api/voices/update", { id, transcript: text });
      await reload();
      if (clip?.id === id) transcript = text;
    } catch (e) {
      transcribeErr = (e as Error).message || "Speech-to-text didn't answer. Type what you said in the clip.";
    } finally {
      transcribing = false;
    }
  }

  async function deleteClip(c: VoiceClip) {
    if (!confirm(`Delete the voice clip “${c.name}”? Personas that use it fall back to the default voice.`)) return;
    try {
      await post("/api/voices/delete", { id: c.id });
      if (voice.kind === "clone" && voice.clip === c.id) voice = { kind: "clone", clip: "" };
      await reload();
    } catch (e) {
      listErr = `It wasn't deleted: ${(e as Error).message || "the factory didn't answer"}.`;
    }
  }

  // ---- a new clip: consent, then record or upload, preview, save ----------------------
  let consent = $state(false);
  let rec: Recording | null = $state(null);
  let now = $state(0);
  let tick: ReturnType<typeof setInterval> | undefined;
  let capErr = $state("");
  let converting = $state(false);
  let pending = $state<{ wav: Blob; url: string; seconds: number } | null>(null);
  let clipName = $state(untrack(() => defaultClipName) || "My voice");
  let savingClip = $state(false);
  let fileEl: HTMLInputElement | undefined = $state();

  function dropPending() {
    if (pending) URL.revokeObjectURL(pending.url);
    pending = null;
  }

  async function take(audio: Blob, recorded: boolean) {
    converting = true;
    capErr = "";
    try {
      const out = await toWav(audio, CLIP_RATE, recorded ? MAX_S - 0.1 : 0);
      if (out.seconds < MIN_S) {
        capErr = `That clip is ${out.seconds.toFixed(1)} s. A voice needs at least ${MIN_S} seconds of speech: record again and say a full sentence or two.`;
        return;
      }
      if (out.seconds > MAX_S + 0.05) {
        capErr = `That file is ${Math.round(out.seconds)} s. Keep a voice clip to ${MAX_S} seconds or less: trim it, or record one here.`;
        return;
      }
      dropPending();
      pending = { wav: out.wav, url: URL.createObjectURL(out.wav), seconds: out.seconds };
    } catch (e) {
      capErr = audioError(e);
    } finally {
      converting = false;
    }
  }

  async function startRec() {
    capErr = "";
    dropPending();
    try {
      rec = await startRecording(true);
      now = performance.now();
      tick = setInterval(() => {
        now = performance.now();
        if (rec && now - rec.started >= MAX_S * 1000) stopRec();
      }, 200);
    } catch (e) {
      rec = null;
      capErr = audioError(e);
    }
  }
  async function stopRec() {
    const r = rec;
    if (!r) return;
    rec = null;
    clearInterval(tick);
    try {
      await take(await r.stop(), true);
    } catch (e) {
      capErr = audioError(e);
    }
  }
  function onFile() {
    const f = fileEl?.files?.[0];
    if (fileEl) fileEl.value = "";
    if (f) take(f, false);
  }

  async function saveClip() {
    if (!pending) return;
    savingClip = true;
    capErr = "";
    try {
      const fd = new FormData();
      fd.append("file", pending.wav, "voice.wav");
      fd.append("name", clipName.trim() || "My voice");
      fd.append("consent", "true");
      const saved = await api<VoiceClip>("/api/voices", { method: "POST", body: fd });
      dropPending();
      await reload();
      voice = { kind: "clone", clip: saved.id };
      transcribe(saved.id);
    } catch (e) {
      capErr = `The clip wasn't saved: ${(e as Error).message || "the factory didn't answer"}.`;
    } finally {
      savingClip = false;
    }
  }

  onDestroy(() => {
    clearInterval(tick);
    rec?.cancel();
    if (pending) URL.revokeObjectURL(pending.url);
  });

  const when = (ms: number) => (ms ? new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric", year: "numeric" }) : "");
</script>

<div class="ve">
  <div class="segc" role="radiogroup" aria-label="Voice">
    <label class:on={kind === ""}>
      <input type="radio" name="{uid}-kind" checked={kind === ""} onchange={() => setKind("")} />
      <b>Default</b><span>The speech member's own voice</span>
    </label>
    <label class:on={kind === "builtin"}>
      <input type="radio" name="{uid}-kind" checked={kind === "builtin"} onchange={() => setKind("builtin")} />
      <b>Built-in</b><span>One of its named voices</span>
    </label>
    <label class:on={kind === "clone"}>
      <input type="radio" name="{uid}-kind" checked={kind === "clone"} onchange={() => setKind("clone")} />
      <b>Cloned</b><span>From a clip you record</span>
    </label>
  </div>

  {#if kind === "builtin"}
    <div class="fl">
      <label for="{uid}-bv">Voice name</label>
      {#if spec.voices.length}
        <select id="{uid}-bv" value={voice.name ?? ""} onchange={(e) => (voice = { kind: "builtin", name: e.currentTarget.value })}>
          {#each spec.voices as [id, label] (id)}<option value={id}>{label}</option>{/each}
          {#if offList}<option value={voice.name}>{voice.name} (not one of its voices)</option>{/if}
        </select>
        <p class="hint">The voices of {speakName || modelName(speakModel)}.</p>
      {:else}
        <input
          id="{uid}-bv"
          type="text"
          list="{uid}-voices"
          value={voice.name ?? ""}
          oninput={(e) => (voice = { kind: "builtin", name: e.currentTarget.value.trim() })}
          placeholder="alba"
          autocomplete="off" />
        <datalist id="{uid}-voices">
          {#each all as v (v.model + v.id)}<option value={v.id}>{v.label} · {modelName(v.model)}</option>{/each}
        </datalist>
        <p class="hint">A voice of the speech model the persona will use, such as <span class="mono">alba</span> (Pocket TTS) or <span class="mono">af_heart</span> (Kokoro). Piper has one voice.</p>
      {/if}
    </div>
    {#if offList}
      <div class="callout wa"><Icon name="warn" size={16} /><span><p>“{voice.name}” isn't a voice of {speakName || modelName(speakModel)}, so it would refuse it. Pick one from the list.</p></span></div>
    {/if}
  {:else if kind === "clone"}
    {#if canClone === false}
      <div class="callout wa">
        <Icon name="warn" size={16} />
        <span>
          <b>{speakName || modelName(speakModel) || "This speech model"} can't clone voices: it only speaks its built-in voices.</b>
          <p>Pick a speech model that clones (marked “clones voices” in the Catalog), or a built-in voice.</p>
        </span>
      </div>
    {/if}

    {#if listErr}
      <div class="callout er"><Icon name="errc" size={16} /><span><b>The voice clips didn't load.</b><p>{listErr}</p><button class="small" type="button" onclick={reload}><Icon name="retry" size={14} />Try again</button></span></div>
    {:else if $voices.length}
      <div class="fl">
        <label for="{uid}-clip">Clip</label>
        <select id="{uid}-clip" value={voice.clip ?? ""} onchange={(e) => (voice = { kind: "clone", clip: e.currentTarget.value })}>
          {#if !voice.clip}<option value="" disabled>Pick a clip, or record one below</option>{/if}
          {#if missingClip}<option value={voice.clip}>A clip that isn't on this machine</option>{/if}
          {#each $voices as c (c.id)}<option value={c.id}>{c.name} · {c.seconds.toFixed(0)} s</option>{/each}
        </select>
      </div>
    {/if}

    {#if missingClip}
      <div class="callout wa">
        <Icon name="warn" size={16} />
        <span><b>This persona's voice clip isn't on this machine.</b><p>Voice clips never leave the computer they were recorded on. Record or upload it again here.</p></span>
      </div>
    {/if}

    {#if clip}
      <div class="clip">
        <div class="ch">
          <span class="ci" aria-hidden="true"><Icon name="wave" size={15} /></span>
          <span class="cn"><b>{clip.name}</b><small>{clip.seconds.toFixed(1)} s{clip.consent_at ? ` · consent given ${when(clip.consent_at)}` : ""}</small></span>
          <button class="small ghost icon danger" type="button" onclick={() => deleteClip(clip)} aria-label="Delete the clip {clip.name}" title="Delete clip"><Icon name="trash" size={14} /></button>
        </div>
        <audio controls preload="none" src={`/api/voices/audio?id=${encodeURIComponent(clip.id)}`}></audio>
        <div class="fl">
          <div class="lrow">
            <label for="{uid}-tr">What the clip says</label>
            {#if transcribing}<span class="busy"><span class="spin" aria-hidden="true"></span>Transcribing…</span>{/if}
          </div>
          <textarea id="{uid}-tr" rows="3" bind:value={transcript} disabled={transcribing} placeholder="The exact words spoken in the clip"></textarea>
          <p class="hint">Cloning needs the exact words. Speech-to-text fills this in when one is running; fix anything it got wrong.</p>
        </div>
        {#if transcribeErr}
          <div class="callout wa"><Icon name="warn" size={16} /><span><p>{transcribeErr}</p></span></div>
        {/if}
        {#if textErr}<div class="callout er"><Icon name="errc" size={16} /><span><p>{textErr}</p></span></div>{/if}
        <div class="acts">
          <button class="small" type="button" onclick={saveTranscript} disabled={!transcriptDirty || savingText || transcribing}>
            {#if savingText}<span class="spin" aria-hidden="true"></span>{:else}<Icon name="check" size={14} />{/if}Save transcript
          </button>
          <button class="small ghost" type="button" onclick={() => transcribe(clip.id)} disabled={transcribing}><Icon name="mic" size={14} />Transcribe again</button>
        </div>
      </div>
    {/if}

    <fieldset class="new">
      <legend>{$voices.length ? "Add a clip" : "Record your first clip"}</legend>
      <p class="hint">Read 3 to 30 seconds of ordinary speech in a quiet room, at the pace you'd like it to talk.</p>
      <label class="consent">
        <input type="checkbox" bind:checked={consent} />
        <span>This is my voice, or I have permission to use it</span>
      </label>
      <div class="acts">
        {#if rec}
          <button class="small primary" type="button" onclick={stopRec}><Icon name="stopsq" size={14} />Stop</button>
          <span class="rec" role="status"><i aria-hidden="true"></i>Recording {elapsed(now - rec.started)} / 0:30</span>
        {:else}
          <button class="small" type="button" onclick={startRec} disabled={!consent || converting || savingClip}><Icon name="mic" size={14} />Record</button>
          <input type="file" accept="audio/*" hidden bind:this={fileEl} onchange={onFile} />
          <button class="small" type="button" onclick={() => fileEl?.click()} disabled={!consent || converting || savingClip}><Icon name="upload" size={14} />Upload a file</button>
          {#if converting}<span class="busy"><span class="spin" aria-hidden="true"></span>Reading the audio…</span>{/if}
        {/if}
      </div>
      {#if !consent}<p class="hint">Tick the box to record or upload.</p>{/if}
      {#if capErr}<div class="callout er"><Icon name="errc" size={16} /><span><p>{capErr}</p></span></div>{/if}
      {#if pending}
        <div class="pend">
          <audio controls src={pending.url}></audio>
          <div class="fl">
            <label for="{uid}-cn">Name</label>
            <input id="{uid}-cn" type="text" bind:value={clipName} autocomplete="off" />
          </div>
          <p class="hint">{pending.seconds.toFixed(1)} s, mono WAV. Listen first: the model copies everything, background noise too.</p>
          <div class="acts">
            <button class="small primary" type="button" onclick={saveClip} disabled={savingClip}>
              {#if savingClip}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="check" size={14} />{/if}Save clip
            </button>
            <button class="small ghost" type="button" onclick={dropPending} disabled={savingClip}>Discard</button>
          </div>
        </div>
      {/if}
    </fieldset>
    <p class="fine"><Icon name="shield" size={13} />Voice clips stay on this machine: they're never exported with an image or uploaded.</p>
  {/if}
</div>

<style>
  .ve { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .segc { display: flex; padding: 3px; border: 1px solid var(--ln2); border-radius: 10px; gap: 2px; max-width: 640px; }
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

  .fl { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .fl label { font-size: 12px; color: var(--fa); font-weight: 500; }
  .fl input, .fl select { font-size: 13px; padding: 7px 10px; }
  .lrow { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; }
  textarea { font: 13px/1.5 var(--font-body); min-height: 64px; }
  .hint { font-size: 12px; line-height: 1.45; }
  .hint .mono { font-size: 11.5px; color: var(--ink); }
  .busy { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--pr); }
  .busy .spin { width: 12px; height: 12px; }

  .clip { display: flex; flex-direction: column; gap: 8px; padding: 12px; border: 1px solid var(--ln); border-radius: 11px; background: var(--p2); }
  .ch { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .ci { width: 30px; height: 30px; border-radius: 8px; background: var(--prs); color: var(--pr); display: grid; place-items: center; flex: none; }
  .cn { display: flex; flex-direction: column; min-width: 0; flex: 1; }
  .cn b { font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cn small { color: var(--mu); font-size: 12px; }
  audio { width: 100%; max-width: 420px; height: 36px; }

  .new { border: 1px dashed var(--ln2); border-radius: 11px; padding: 10px 12px 12px; margin: 0; display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .new legend { padding: 0 4px; font-size: 12px; font-weight: 600; color: var(--ink); }
  .consent { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
  .consent input { margin: 0; width: 15px; height: 15px; }
  .acts { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .rec { display: inline-flex; align-items: center; gap: 7px; font: 500 12.5px var(--font-mono); color: var(--er); }
  .rec i { width: 9px; height: 9px; border-radius: 50%; background: var(--er); animation: pulse 1.1s infinite ease-in-out; }
  @keyframes pulse { 50% { opacity: 0.3; } }
  @media (prefers-reduced-motion: reduce) { .rec i { animation: none; } }
  .pend { display: flex; flex-direction: column; gap: 8px; padding-top: 4px; }
  .pend .fl { max-width: 320px; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
  .callout p { margin: 0; color: var(--ink); }
  .callout button { margin-top: 6px; }
  .fine { display: flex; align-items: center; gap: 6px; color: var(--fa); font-size: 12px; }

  @media (max-width: 760px) {
    .segc { flex-direction: column; }
  }
</style>
