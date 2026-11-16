<script lang="ts">
  // The prompt box: one message to the model, or to both in compare mode. Takes
  // an image (vision) or an audio file (speech to text) by button or paste, the
  // clip length for video models, and stops replies that are still streaming.
  // With an ensemble on screen it also has a mic (ask out loud, hear the answer
  // in the persona's voice) and "Speak replies" for typed messages.
  import { onDestroy } from "svelte";
  import Icon from "../Icon.svelte";
  import { toast } from "../../lib/stores";
  import { startRecording, toWav, audioError, elapsed, STT_RATE, type Recording } from "../../lib/audio";
  import { MODE, send, sendVoice, stopTab, streaming, visiblePanes, save, type Tab, type Target } from "../../lib/chat.svelte";
  import { createPrompt, refreshPrompts, type SavedPrompt } from "../../lib/prompts.svelte";

  let {
    tab,
    targets,
    draft = $bindable(""),
    fromPrompt = $bindable(null),
  }: { tab: Tab; targets: Target[]; draft?: string; fromPrompt?: SavedPrompt | null } = $props();

  // The box still holds the saved prompt it was filled with (unchanged).
  const saved = $derived(fromPrompt && draft.trim() === fromPrompt.text.trim() ? fromPrompt : null);

  // ---- save the box as a reusable prompt ----------------------------------------------
  let naming = $state(false);
  let pname = $state("");
  let withSystem = $state(true);
  let savingP = $state(false);
  let nameEl: HTMLInputElement | undefined = $state();
  function startSave() {
    if (!draft.trim()) return;
    naming = true;
    pname = "";
    requestAnimationFrame(() => nameEl?.focus());
  }
  async function savePrompt() {
    const text = draft.trim();
    if (!text || savingP) return;
    savingP = true;
    try {
      const p = await createPrompt({ name: pname.trim(), text, system: withSystem && tab.system.trim() ? tab.system : undefined });
      fromPrompt = p;
      naming = false;
      refreshPrompts();
      toast(`Saved “${p.name}”. It's in the Prompts menu; sent as the first message of a chat, its answers are kept.`, 5000);
    } catch (e) {
      toast(`Couldn't save the prompt: ${(e as Error).message}`, 6000);
    } finally {
      savingP = false;
    }
  }

  let image = $state<string | null>(null);
  let file = $state<File | null>(null);
  let fileEl: HTMLInputElement | undefined = $state();
  let ta: HTMLTextAreaElement | undefined = $state();

  const panes = $derived(visiblePanes(tab));
  const both = $derived(panes.length > 1);
  const mods = $derived(panes.map((p) => targets.find((t) => t.port === p.port)?.modality ?? p.modality));
  const accept = $derived([...new Set(mods.map((m) => MODE[m]?.attach).filter(Boolean))].join(","));
  const takesImage = $derived(accept.includes("image"));
  const takesAudio = $derived(accept.includes("audio"));
  const off = $derived(mods.every((m) => MODE[m]?.disabled) || panes.every((p) => !p.port));
  const busy = $derived(panes.some((p) => p.busy));
  const live = $derived(streaming(tab));
  const hasVideo = $derived(mods.includes("video"));
  const ph = $derived(
    panes.every((p) => !p.port)
      ? "Pick a model first"
      : both && mods[0] !== mods[1]
        ? "Message both models…"
        : (MODE[mods[0]]?.ph ?? "Message the model…"),
  );
  const attachLabel = $derived(takesImage && takesAudio ? "Attach an image or audio file" : takesImage ? "Attach an image" : "Attach an audio file");

  // ---- ask out loud (ensembles) ---------------------------------------------------------
  const MAX_MS = 60000;
  const hasEnsemble = $derived(mods.includes("ensemble"));
  let rec: Recording | null = $state(null);
  let now = $state(0);
  let converting = $state(false);
  let tick: ReturnType<typeof setInterval> | undefined;

  async function mic() {
    if (rec) return finish();
    if (busy) return;
    try {
      rec = await startRecording(false);
      now = performance.now();
      tick = setInterval(() => {
        now = performance.now();
        if (rec && now - rec.started >= MAX_MS) finish();
      }, 200);
    } catch (e) {
      rec = null;
      toast(audioError(e), 8000);
    }
  }
  async function finish() {
    const r = rec;
    if (!r) return;
    rec = null;
    clearInterval(tick);
    converting = true;
    try {
      const { wav, seconds } = await toWav(await r.stop(), STT_RATE);
      if (seconds < 0.6) {
        toast("That was too short to hear a question. Click the mic, ask, then click it again to send.", 6000);
        return;
      }
      const res = sendVoice(tab, wav);
      if (res.notes.length) toast(res.notes.join(" "), 6500);
    } catch (e) {
      toast(audioError(e), 8000);
    } finally {
      converting = false;
    }
  }
  function cancelMic() {
    rec?.cancel();
    rec = null;
    clearInterval(tick);
  }
  onDestroy(cancelMic);

  function toggleSpeak() {
    tab.speakReplies = !tab.speakReplies;
    save();
  }

  function grow() {
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, 200) + "px";
  }

  function submit(e?: Event) {
    e?.preventDefault();
    if (busy || off) return;
    const text = draft.trim();
    if (!text && !image && !file) return;
    const r = send(tab, text, image ?? undefined, file ?? undefined, saved ? { prompt: saved, targets } : undefined);
    if (r.notes.length) toast(r.notes.join(" "), 6500);
    if (r.sent) {
      draft = "";
      fromPrompt = null;
      image = null;
      file = null;
      requestAnimationFrame(grow);
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key !== "Enter" || e.isComposing) return;
    if (e.shiftKey && !(e.metaKey || e.ctrlKey)) return; // Shift+Enter: a new line
    e.preventDefault();
    submit();
  }

  function take(f: File) {
    if (f.type.startsWith("audio/")) {
      if (!takesAudio) return toast("None of these models transcribes audio.");
      file = f;
      image = null;
    } else if (f.type.startsWith("image/")) {
      if (!takesImage) return toast("None of these models reads images; pick a vision model to ask about one.");
      const rd = new FileReader();
      rd.onload = () => {
        image = rd.result as string;
        file = null;
      };
      rd.readAsDataURL(f);
    } else {
      toast(`That's not an ${takesImage ? "image" : "audio file"}.`);
    }
  }

  function onFile() {
    const f = fileEl?.files?.[0];
    if (f) take(f);
    if (fileEl) fileEl.value = "";
  }

  function onPaste(e: ClipboardEvent) {
    const f = [...(e.clipboardData?.files ?? [])].find((x) => x.type.startsWith("image/") || x.type.startsWith("audio/"));
    if (!f || !accept) return;
    e.preventDefault();
    take(f);
  }
</script>

<form class="comp" onsubmit={submit}>
  {#if rec}
    <div class="recbar" role="status">
      <i aria-hidden="true"></i>
      <span>Listening… {elapsed(now - rec.started)}</span>
      <small>Click the mic again to send, up to 1 minute.</small>
      <button class="small ghost" type="button" onclick={cancelMic}>Cancel</button>
    </div>
  {:else if converting}
    <div class="recbar" role="status"><span class="spin" aria-hidden="true"></span><span>Preparing your question…</span></div>
  {/if}
  {#if naming}
    <div class="recbar nm">
      <Icon name="bookmark" size={14} />
      <label class="vh" for="pg-pname">Name for the saved prompt</label>
      <input
        id="pg-pname"
        bind:this={nameEl}
        bind:value={pname}
        maxlength="120"
        placeholder={"Name (default: " + (draft.trim().split("\n")[0].slice(0, 40) || "its first line") + ")"}
        onkeydown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            savePrompt();
          } else if (e.key === "Escape") naming = false;
        }} />
      {#if tab.system.trim()}<label class="ck"><input type="checkbox" bind:checked={withSystem} /> with the system prompt</label>{/if}
      <button class="small primary" type="button" disabled={savingP || !draft.trim()} onclick={savePrompt}>Save prompt</button>
      <button class="small ghost" type="button" onclick={() => (naming = false)}>Cancel</button>
    </div>
  {/if}
  {#if saved}
    <div class="att sp">
      <Icon name="bookmark" size={14} />
      <span>Saved prompt: {saved.name}</span>
      <button class="small icon" type="button" aria-label="Forget the saved prompt" title="Send it as a plain message (not recorded)" onclick={() => (fromPrompt = null)}><Icon name="xmark" size={13} /></button>
    </div>
  {/if}
  {#if image || file}
    <div class="att">
      {#if image}<img src={image} alt="" />{:else}<Icon name="mic" size={14} />{/if}
      <span>{image ? "Image attached" : file?.name}</span>
      <button class="small icon" type="button" aria-label="Remove the attachment" title="Remove" onclick={() => ((image = null), (file = null))}><Icon name="xmark" size={13} /></button>
    </div>
  {/if}
  <div class="row">
    <label class="vh" for="pg-prompt">Message</label>
    <textarea
      id="pg-prompt"
      rows="1"
      bind:value={draft}
      bind:this={ta}
      placeholder={ph}
      disabled={off}
      oninput={grow}
      onkeydown={onKey}
      onpaste={onPaste}></textarea>
    <div class="tools">
      <button
        class="icon"
        class:on={!!saved}
        type="button"
        aria-label={saved ? `Saved as “${saved.name}”` : "Save as a reusable prompt"}
        title={saved ? `Saved as “${saved.name}”` : draft.trim() ? "Save this as a reusable prompt" : "Write a prompt, then save it here for reuse"}
        disabled={!draft.trim() || !!saved}
        onclick={startSave}><Icon name="bookmark" size={16} /></button>
      {#if accept}
        <input type="file" hidden {accept} bind:this={fileEl} onchange={onFile} />
        <button class="icon" type="button" aria-label={attachLabel} title={attachLabel} disabled={off} onclick={() => fileEl?.click()}><Icon name="attach" size={16} /></button>
      {/if}
      {#if hasEnsemble}
        <button
          class="icon"
          class:on={!!tab.speakReplies}
          type="button"
          aria-pressed={!!tab.speakReplies}
          aria-label="Speak replies to typed messages"
          title={tab.speakReplies ? "Speaking replies in the persona's voice: click to stop" : "Also speak replies to typed messages, in the persona's voice"}
          disabled={off}
          onclick={toggleSpeak}><Icon name="speaker" size={16} /></button>
        <button
          class="icon mic"
          class:live={!!rec}
          type="button"
          aria-pressed={!!rec}
          aria-label={rec ? "Stop and send the question" : "Ask out loud"}
          title={rec ? "Stop and send" : "Ask out loud: the answer comes back in the persona's voice"}
          disabled={off || converting || (busy && !rec)}
          onclick={mic}>
          {#if rec}<Icon name="stopsq" size={15} />{:else}<Icon name="mic" size={16} />{/if}
        </button>
      {/if}
      {#if hasVideo}
        <label class="len" title="Longer clips take more time and memory">
          <span>Length</span>
          <select value={String(tab.vidFrames ?? 33)} onchange={(e) => ((tab.vidFrames = parseInt(e.currentTarget.value, 10)), save())}>
            <option value="17">~1 s</option><option value="33">~2 s</option><option value="49">~3 s</option><option value="65">~4 s</option><option value="81">~5 s</option>
          </select>
        </label>
      {/if}
      {#if live}
        <button type="button" onclick={() => stopTab(tab)}><Icon name="stopsq" size={14} />Stop</button>
      {:else}
        <button class="primary" type="submit" disabled={busy || off}>
          {#if busy}<span class="spin"></span>{/if}{both ? "Send to both" : "Send"}<Icon name="arrowRight" size={15} />
        </button>
      {/if}
    </div>
  </div>
</form>

<style>
  .comp {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 10px 10px 10px 14px;
    background: var(--p1);
    border: 1px solid var(--ln2);
    border-radius: 16px;
  }
  .comp:focus-within { border-color: color-mix(in srgb, var(--pr) 60%, var(--ln2)); }
  .row { display: flex; align-items: flex-end; gap: 10px; }
  .vh { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
  textarea {
    flex: 1;
    min-width: 0;
    border: 0;
    background: none;
    box-shadow: none;
    resize: none;
    padding: 7px 0;
    font-size: 14px;
    line-height: 1.45;
    max-height: 200px;
  }
  textarea:focus { box-shadow: none; }
  .tools { display: flex; gap: 6px; align-items: center; flex: none; }
  .len { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--mu); }
  .len select { width: auto; height: 34px; padding: 0 8px; font-size: 12.5px; }
  .att {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    align-self: flex-start;
    padding: 4px 4px 4px 6px;
    border: 1px solid var(--ln);
    border-radius: var(--r);
    background: var(--p2);
    font-size: 12.5px;
    color: var(--mu);
    max-width: 100%;
  }
  .att img { width: 32px; height: 32px; object-fit: cover; border-radius: 6px; }
  .icon.on { background: var(--prs); color: var(--pr); border-color: color-mix(in srgb, var(--pr) 40%, var(--ln2)); }
  .mic.live { background: var(--ers); color: var(--er); border-color: color-mix(in srgb, var(--er) 45%, var(--ln2)); }
  .recbar { display: flex; align-items: center; gap: 9px; flex-wrap: wrap; font-size: 13px; color: var(--ink); }
  .recbar > i { width: 9px; height: 9px; border-radius: 50%; background: var(--er); animation: pulse 1.1s infinite ease-in-out; }
  .recbar span { font-variant-numeric: tabular-nums; }
  .recbar small { color: var(--fa); font-size: 12px; }
  .recbar .spin { width: 13px; height: 13px; }
  .recbar button { margin-left: auto; }
  .nm input:not([type="checkbox"]) { flex: 1; min-width: 160px; height: 32px; }
  .nm button { margin-left: 0; }
  .nm .ck { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--mu); }
  @keyframes pulse { 50% { opacity: 0.3; } }
  @media (prefers-reduced-motion: reduce) { .recbar > i { animation: none; } }
  .att span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  @media (max-width: 760px) {
    .row { flex-wrap: wrap; }
    textarea { flex-basis: 100%; }
    .tools { margin-left: auto; }
  }
</style>
