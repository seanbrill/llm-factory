// Playground state and logic (sheet frame f14). A tab is a conversation with a
// running model, or with two side by side in compare mode, where one prompt
// goes to both. Streams measure their own speed (time to the first token,
// tokens per second); media models generate images, video and speech or
// transcribe audio; two models can talk to each other (the bridge); every side
// can copy its last request as curl. Tabs persist in localStorage.
//
// An ensemble is one more kind of side: it chats like a text model, but its
// replies can carry images, clips and speech (delta.media), it says what it's
// doing while a member warms up (": ensemble: …" comments), and it takes a
// spoken question (voiceChat: speech to text -> chat -> speech in the
// persona's voice).
//
// A Svelte 5 rune module: every component shares this state. Mutate messages
// through the $state proxy (pane.msgs[i]), never a raw object, or the change
// only paints when the stream ends.
import { api, post, streamChat, labelVal, type StreamOpts } from "./api";
import { imageDefaults, ttsSpec } from "./meta";
import { containerPort, containerRef, isRunning, isStreamMod } from "./util";
import type { ContainerInfo, Ensemble, MediaItem, Model, Persona, RunningModel } from "./types";
import { recordResult, resultOf, type RunOutcome, type SavedPrompt } from "./prompts.svelte";

// ---- types ------------------------------------------------------------------------

/** What a reply measured: times in ms from sending. Media replies only have `ms`. */
export interface Stats {
  ms: number;            // until the last token (or the result)
  ttft?: number;         // until the first token
  tokens?: number;       // streamed chunks: llama.cpp sends one token per chunk
  tps?: number | null;   // tokens per second after the first
}

export interface Msg {
  role: "user" | "assistant" | "system";
  content: string;
  reasoning?: string;    // a reasoning model's thinking (delta.reasoning_content)
  kind?: string;         // "image" | "video" | "audio" | "text"
  image?: string;        // an image the user attached (vision)
  images?: string[];     // generated images
  audio?: string;
  video?: string;
  bridgeFrom?: string;
  bridgeSide?: "a" | "b";
  bridgeScene?: boolean;
  err?: string;          // what went wrong, as a sentence
  error?: boolean;       // older saved chats: content holds the error
  stopped?: boolean;     // the user stopped it
  stats?: Stats;
  // While a media generation runs: drives the progress ring. at = start ms,
  // eta = expected duration ms (for the % estimate).
  pending?: { at: number; eta: number; label: string };
  autoplay?: boolean;    // fresh speech plays on arrival (not on reload)
  spoken?: boolean;      // a question asked out loud (its content is the transcript)
  note?: string;         // a side remark under the reply (e.g. why it didn't speak)
}

/** The last request a side sent, for "Copy as curl". */
export interface Req {
  path: string;
  url?: string;          // a model's own endpoint, when the factory has no route for it
  body?: Record<string, unknown>;
  form?: { file: string; port: number };
  out?: string;          // curl -o for a binary answer
}

/** One side of a tab: a model and the conversation with it. */
export interface Pane {
  id: number;
  port: number;          // 0 = no model picked yet
  modality: string;
  model: string;         // catalog model id
  name: string;
  msgs: Msg[];
  busy: boolean;
  last: Req | null;
}

export interface Tab {
  id: number;
  panes: Pane[];         // [left] or [left, right]
  compare: boolean;      // show the right side and send to both
  bridge?: boolean;      // two models talking (read-only)
  // Shared by both sides, so a comparison is fair.
  system: string;
  persona: string;       // name of the persona the prompt came from, "" = none or custom
  temp: number;
  topP: number;
  seed: number | null;
  // Media output settings.
  vidFrames?: number;    // Wan plays ~16 fps; must be 4k+1 (17, 33, 49, 65, 81)
  negative?: string;
  imgRes?: number;
  imgSteps?: number;
  vidRes?: string;
  vidSteps?: number;
  ttsVoice?: string;
  ttsSpeed?: number;
  ttsStyle?: string;
  mediaSeed?: number | null;
  speakReplies?: boolean; // an ensemble also speaks its replies to typed messages
}

/** A running model the Playground can talk to. */
export interface Target {
  port: number;
  name: string;          // catalog name, else the container's
  modality: string;
  model: string;         // catalog model id, "" when unlabelled
  ref: string;           // the image it runs
  engine: string;        // docker | podman
  compute: string;       // cpu | vulkan | cuda
  persona: string;       // its run-time persona, or the name in its baked prompt
  container: string;
  ensemble?: string;     // the ensemble id, for an ensemble
}

// ---- state ------------------------------------------------------------------------

export const chat = $state({
  tabs: [] as Tab[],
  active: null as number | null,
  bridging: false,
  bridgeStop: false,
});

let tabSeq = 0;
let paneSeq = 0;
const streams = new Map<number, AbortController>(); // pane id -> its stream
let bridgeAbort: AbortController | null = null;

/** Composer placeholder and attachments per modality. */
export const MODE: Record<string, { ph: string; attach?: string; disabled?: boolean }> = {
  text: { ph: "Message the model…" },
  code: { ph: "Ask for code…" },
  reasoning: { ph: "Ask something that needs reasoning…" },
  vision: { ph: "Attach or paste an image, then ask about it…", attach: "image/*" },
  image: { ph: "Describe the image to generate…" },
  video: { ph: "Describe the video to generate…" },
  "audio-stt": { ph: "Attach an audio file to transcribe…", attach: "audio/*" },
  tts: { ph: "Text to speak aloud…" },
  embedding: { ph: "Embedding models return vectors, not chat.", disabled: true },
  ensemble: { ph: "Message the ensemble, ask it to draw or film something, or use the mic…", attach: "image/*" },
};

export function activeTab(): Tab | null {
  return chat.tabs.find((t) => t.id === chat.active) ?? null;
}

/** The sides on screen: both in compare mode, else the left one. */
export function visiblePanes(t: Tab): Pane[] {
  return t.compare && t.panes.length > 1 ? t.panes.slice(0, 2) : t.panes.slice(0, 1);
}

// ---- running models ---------------------------------------------------------------

/** "You are Ada, …" -> "Ada": the name a baked system prompt gives the model. */
function personaName(prompt: string): string {
  if (!prompt) return "";
  for (const re of [/\byou are\s+(?:called\s+)?([A-Z][A-Za-z0-9._-]{1,30})/, /\byour name is\s+([A-Z][A-Za-z0-9._-]{1,30})/i]) {
    const m = prompt.match(re);
    if (m) {
      const n = m[1].replace(/[.,;:!?'"]+$/, "");
      if (n && !/^(a|an|the|your)$/i.test(n)) return n;
    }
  }
  return "";
}

/**
 * Running containers -> the models the Playground can talk to, in port order.
 * `ens` names ensembles (and the persona each plays); their members, which
 * come and go as the Conductor starts and evicts them, are left out.
 */
export function targetsFrom(containers: ContainerInfo[], catalog: Model[], ens?: { ensembles: Ensemble[]; personas: Persona[] }): Target[] {
  const out: Target[] = [];
  for (const c of containers) {
    if (!isRunning(c)) continue;
    if (labelVal(c.Labels, "local-llm.ensemble-member")) continue;
    const port = parseInt(containerPort(c), 10) || 0;
    if (!port) continue;
    const ref = containerRef(c);
    const container0 = (c.Names || "").replace(/^\//, "");
    if (labelVal(c.Labels, "local-llm.kind") === "ensemble" || labelVal(c.Labels, "local-llm.modality") === "ensemble") {
      const eid = labelVal(c.Labels, "local-llm.ensemble");
      const e = ens?.ensembles.find((x) => x.id === eid);
      out.push({
        port,
        name: e?.name || container0 || `Ensemble on :${port}`,
        modality: "ensemble",
        model: "",
        ref,
        engine: c.Engine === "podman" ? "podman" : "docker",
        compute: labelVal(c.Labels, "local-llm.compute") || "cpu",
        persona: (e?.persona && ens?.personas.find((p) => p.id === e.persona)?.name) || "",
        container: container0,
        ensemble: eid,
      });
      continue;
    }
    const id = labelVal(c.Labels, "local-llm.model");
    // Unlabelled (older) containers: the longest catalog id in the image ref.
    const m = id
      ? catalog.find((x) => x.id === id)
      : catalog.filter((x) => ref.includes(x.id)).sort((a, b) => b.id.length - a.id.length)[0];
    const container = (c.Names || "").replace(/^\//, "");
    out.push({
      port,
      name: m?.name || container || `Model on :${port}`,
      // The container's own label is set at run time; `docker images` doesn't list labels.
      modality: labelVal(c.Labels, "local-llm.modality") || m?.modality || "text",
      model: id || m?.id || "",
      ref,
      engine: c.Engine === "podman" ? "podman" : "docker",
      compute: labelVal(c.Labels, "local-llm.compute") || "cpu",
      persona: labelVal(c.Labels, "local-llm.persona") || personaName(labelVal(c.Labels, "local-llm.system_prompt")),
      container,
    });
  }
  return out.sort((a, b) => a.port - b.port);
}

/** The factory's memory estimate for a running model (from its size, not measured). */
export function memEstimate(t: Target | undefined, running: RunningModel[] | null | undefined): { gb: number; gpu: boolean } | null {
  if (!t || !running?.length) return null;
  const r = running.find((x) => x.name === t.container) ?? running.find((x) => !!t.model && x.model_id === t.model);
  if (!r) return null;
  const gpu = r.compute === "vulkan" || r.compute === "cuda";
  const v = gpu ? r.vram_gb : r.ram_gb;
  return v > 0 ? { gb: v, gpu } : null;
}

// ---- tabs and sides ---------------------------------------------------------------

function newPane(t?: Target): Pane {
  return { id: ++paneSeq, port: t?.port ?? 0, modality: t?.modality ?? "text", model: t?.model ?? "", name: t?.name ?? "", msgs: [], busy: false, last: null };
}

/** A new tab with one side (on `t` when given); returns the reactive tab. */
export function newTab(t?: Target): Tab {
  chat.tabs.push({ id: ++tabSeq, panes: [newPane(t)], compare: false, system: "", persona: "", temp: 0.4, topP: 0.95, seed: null, vidFrames: 33 });
  const tab = chat.tabs[chat.tabs.length - 1];
  chat.active = tab.id;
  save();
  return tab;
}

export function closeTab(id: number) {
  const i = chat.tabs.findIndex((t) => t.id === id);
  if (i < 0) return;
  const tab = chat.tabs[i];
  for (const p of tab.panes) streams.get(p.id)?.abort();
  if (tab.bridge && chat.bridging) bridgeStop();
  chat.tabs.splice(i, 1);
  if (chat.active === id) chat.active = chat.tabs.length ? chat.tabs[Math.max(0, i - 1)].id : null;
  save();
}

/** Point a side at a running model. */
export function setTarget(p: Pane, t: Target) {
  p.port = t.port;
  p.modality = t.modality;
  p.model = t.model;
  p.name = t.name;
  save();
}

/** "#playground/<port>": that model's tab, else the active tab while it's empty, else a new tab. */
export function openTarget(t: Target) {
  const own = chat.tabs.find((x) => !x.bridge && !x.compare && x.panes[0]?.port === t.port);
  if (own) {
    chat.active = own.id;
    return;
  }
  const cur = activeTab();
  if (cur && !cur.bridge && !cur.compare && !cur.panes[0].msgs.length && !cur.panes[0].busy) {
    setTarget(cur.panes[0], t);
    return;
  }
  newTab(t);
}

/** Compare mode on: the right side gets another running model (a chat one for a chat one). */
export function setCompare(tab: Tab, on: boolean, targets: Target[]) {
  if (tab.bridge) return;
  if (on && tab.panes.length < 2) {
    const left = tab.panes[0];
    const other =
      targets.find((t) => t.port !== left.port && isStreamMod(t.modality) === isStreamMod(left.modality)) ??
      targets.find((t) => t.port !== left.port);
    tab.panes.push(newPane(other));
  }
  tab.compare = on;
  save();
}

export function swapSides(tab: Tab) {
  if (tab.panes.length < 2 || !tab.compare) return;
  tab.panes = [tab.panes[1], tab.panes[0], ...tab.panes.slice(2)];
  save();
}

/** Empty every side of the tab (stops what's still answering). */
export function clearTab(tab: Tab) {
  for (const p of tab.panes) {
    streams.get(p.id)?.abort();
    p.msgs = [];
    p.last = null;
  }
  save();
}

/** Stop the replies still streaming in this tab (media generations run to the end). */
export function stopTab(tab: Tab) {
  for (const p of tab.panes) streams.get(p.id)?.abort();
  if (tab.bridge) bridgeStop();
}

// Read p.busy first: it is what makes this reactive (the Map is not).
export const streaming = (tab: Tab) => tab.panes.some((p) => p.busy && streams.has(p.id));

/** What the side's latest reply measured; null while it hasn't measured anything. */
export function lastStats(p: Pane): Stats | null {
  for (let i = p.msgs.length - 1; i >= 0; i--) {
    const m = p.msgs[i];
    if (m.role === "assistant" && !m.bridgeFrom) return m.stats ?? null;
  }
  return null;
}

/** In compare mode, which side was quicker to start and which streamed faster. */
export function winners(a: Stats | null, b: Stats | null): { ttft: number; tps: number } {
  // Within 5% of each other is a tie: one run's noise, and both read the same.
  const pick = (x?: number | null, y?: number | null, lower = true) =>
    x == null || y == null || Math.abs(x - y) <= 0.05 * Math.max(x, y) ? -1 : (lower ? x < y : x > y) ? 0 : 1;
  return { ttft: pick(a?.ttft ?? a?.ms, b?.ttft ?? b?.ms), tps: pick(a?.tps, b?.tps, false) };
}

// ---- sending ----------------------------------------------------------------------

const label = (p: Pane) => p.name || `the model on :${p.port}`;

function sampling(t: Tab): StreamOpts {
  const o: StreamOpts = { temperature: t.temp ?? 0.4, top_p: t.topP ?? 0.95 };
  if (t.seed != null) o.seed = t.seed;
  return o;
}

function buildMessages(t: Tab, p: Pane, draft?: string): unknown[] {
  const out: unknown[] = [];
  if (t.system?.trim()) out.push({ role: "system", content: t.system.trim() });
  for (const m of p.msgs) {
    if (m.err || m.error) continue;
    if (m.role === "assistant") {
      if ((!m.kind || m.kind === "text") && m.content) out.push({ role: "assistant", content: m.content });
      continue;
    }
    if (m.role !== "user") continue;
    if (m.image) out.push({ role: "user", content: [{ type: "text", text: m.content || "" }, { type: "image_url", image_url: { url: m.image } }] });
    else if (!m.kind || m.kind === "text") out.push({ role: "user", content: m.content || "" });
  }
  if (draft != null) out.push({ role: "user", content: draft });
  return out;
}

/** The body streamChat (lib/api.ts) sends to /api/chat/stream, built the same way.
 *  No max_tokens: a reply (and each harness step, which writes whole tool calls)
 *  may run as long as the model's context allows. */
function chatBody(port: number, messages: unknown[], opts: StreamOpts): Record<string, unknown> {
  return { port, messages, temperature: 0.4, ...opts };
}

function failText(p: Pane, e: unknown): string {
  const msg = ((e as Error)?.message || "no answer").replace(/\.$/, "");
  return `${label(p)} on :${p.port} didn't answer: ${msg}. Check that it's still running in the Library, then try again.`;
}

/**
 * Send one prompt to every side on screen, each the way its model takes it.
 * Returns whether anything went out, and a sentence for each side that
 * couldn't use the input.
 */
/** A saved prompt being sent from the Playground, so its answers are recorded
 *  as results (lib/prompts.svelte.ts). The targets give each side's image. */
export interface SavedSend {
  prompt: SavedPrompt;
  targets: Target[];
}

/** Set a tab's system prompt and sampling to what a saved prompt asks for;
 *  returns what changed, for a note. */
export function applyPrompt(tab: Tab, p: SavedPrompt): string[] {
  const changed: string[] = [];
  if (p.system?.trim() && tab.system !== p.system) {
    tab.system = p.system;
    tab.persona = "";
    changed.push("system prompt");
  }
  if (p.temperature != null && tab.temp !== p.temperature) {
    tab.temp = p.temperature;
    changed.push("temperature");
  }
  if (p.top_p != null && tab.topP !== p.top_p) {
    tab.topP = p.top_p;
    changed.push("top-p");
  }
  if (changed.length) save();
  return changed;
}

export function send(tab: Tab, text: string, image?: string, file?: File, saved?: SavedSend): { sent: boolean; notes: string[] } {
  const notes: string[] = [];
  let sent = false;
  if (tab.bridge) return { sent, notes };
  for (const p of visiblePanes(tab)) {
    if (!p.port) {
      notes.push("Pick a model for each side first.");
      continue;
    }
    if (p.busy) {
      notes.push(`${label(p)} is still answering; wait for it or stop it.`);
      continue;
    }
    const mod = p.modality;
    if (mod === "image" || mod === "video" || mod === "tts") {
      if (!text) {
        notes.push(`${label(p)} needs a text prompt.`);
        continue;
      }
      if (mod === "image") genImage(tab, p, text);
      else if (mod === "video") genVideo(tab, p, text);
      else speak(tab, p, text);
      sent = true;
    } else if (mod === "audio-stt") {
      if (!file) {
        notes.push(`${label(p)} transcribes speech: attach an audio file.`);
        continue;
      }
      transcribe(p, file);
      sent = true;
    } else if (mod === "embedding") {
      notes.push(`${label(p)} is an embedding model: it returns vectors, not replies. Copy its request as curl to try it.`);
    } else {
      // An ensemble passes an image to its vision member.
      const img = mod === "vision" || mod === "ensemble" ? image : undefined;
      if (image && !img) notes.push(`${label(p)} doesn't read images, so it got the text only.`);
      if (!text && !img) continue;
      // A saved prompt is recorded when it opens the conversation, with no
      // image: then every model got the same request.
      const fresh = !p.msgs.some((m) => m.role !== "system");
      const rec = saved && fresh && !img ? { prompt: saved.prompt, target: saved.targets.find((t) => t.port === p.port) } : undefined;
      const um: Msg = { role: "user", content: text };
      if (img) um.image = img;
      p.msgs.push(um);
      streamTo(tab, p, rec?.target ? (rec as { prompt: SavedPrompt; target: Target }) : undefined);
      sent = true;
    }
  }
  return { sent, notes };
}

function measure(t0: number, first: number, now: number, n: number): Stats {
  return { ms: now - t0, ttft: first - t0, tokens: n, tps: n > 1 && now > first ? ((n - 1) * 1000) / (now - first) : null };
}

async function streamTo(tab: Tab, p: Pane, rec?: { prompt: SavedPrompt; target: Target }) {
  const messages = buildMessages(tab, p);
  const opts = sampling(tab);
  p.last = { path: "/api/chat/stream", body: chatBody(p.port, messages, opts) };
  const idx = p.msgs.push({ role: "assistant", content: "" }) - 1;
  const a = p.msgs[idx];
  p.busy = true;
  const ctrl = new AbortController();
  streams.set(p.id, ctrl);
  const t0 = performance.now();
  let first = 0;
  let n = 0;
  const ens = p.modality === "ensemble";
  try {
    await streamChat(
      p.port,
      messages,
      (d, kind) => {
        const now = performance.now();
        n++;
        if (!first) first = now;
        if (a.pending) a.pending = undefined;
        if (kind === "reasoning") a.reasoning = (a.reasoning ?? "") + d;
        else a.content += d;
        a.stats = measure(t0, first, now, n);
      },
      ctrl.signal,
      opts,
      ens
        ? {
            // "Starting the chat member…", "Drawing the image…": shown until words arrive.
            onStatus: (s) => {
              if (!a.content && !a.reasoning) a.pending = { at: a.pending?.at ?? Date.now(), eta: a.pending?.eta ?? 60000, label: s };
            },
            onMedia: (items) => {
              a.pending = undefined;
              addMedia(a, items);
              a.stats = { ms: performance.now() - t0 };
            },
          }
        : undefined,
    );
    a.pending = undefined;
    if (!a.content && !a.reasoning && !hasMedia(a)) a.err = `${label(p)} sent an empty reply. Try again, or raise the temperature.`;
    if (rec && !a.err) {
      const out: RunOutcome = { stats: a.stats ?? { ms: performance.now() - t0 }, output: a.content, reasoning: a.reasoning?.length ?? 0 };
      recordResult(resultOf(rec.prompt, rec.target, opts, out, "playground"))
        .then(() => (a.note = `Recorded as a result of “${rec.prompt.name}”.`))
        .catch((e) => (a.note = `Not recorded as a result: ${(e as Error).message}`));
    }
  } catch (e) {
    a.pending = undefined;
    if ((e as Error).name === "AbortError") a.stopped = true;
    else a.err = failText(p, e);
  } finally {
    p.busy = false;
    streams.delete(p.id);
    save();
  }
  if (ens && tab.speakReplies && a.content && !a.err && !a.audio) speakReply(p, a);
}

const hasMedia = (m: Msg) => !!(m.images?.length || m.video || m.audio);

/** Put an ensemble's generated media on its reply. */
function addMedia(a: Msg, items: MediaItem[]) {
  for (const it of items) {
    if (!it?.url) continue;
    if (it.type === "image") a.images = [...(a.images ?? []), it.url];
    else if (it.type === "video") a.video = it.url;
    else if (it.type === "audio") {
      a.audio = it.url;
      a.autoplay = true;
    }
  }
}

/** Text an ensemble should say aloud: no code, links, markup or thinking. */
export function speakable(md: string): string {
  return md
    .replace(/<think>[\s\S]*?<\/think>/gi, "")
    .replace(/```[\s\S]*?```/g, " The code is in the chat. ")
    .replace(/!\[[^\]]*\]\([^)]*\)/g, "")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/https?:\/\/\S+/g, "")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/^\s*[-*+]\s+/gm, "")
    .replace(/[*_~>|]/g, "")
    .replace(/\s+/g, " ")
    .trim();
}

/** Speak an ensemble's typed-message reply in its persona's voice (Speak replies). */
async function speakReply(p: Pane, a: Msg) {
  const text = speakable(a.content);
  if (!text) return;
  a.note = "Speaking…";
  try {
    const res = await fetch("/api/tts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ port: p.port, text }) });
    if (!res.ok) await fail(res);
    a.audio = URL.createObjectURL(await res.blob());
    a.autoplay = true;
    a.note = undefined;
  } catch (e) {
    a.note = `It didn't speak: ${((e as Error).message || "no answer").replace(/\.$/, "")}. Check the ensemble's speech member on the Ensembles page.`;
  } finally {
    save();
  }
}

/** The answer to a spoken question (POST /api/ensembles/voice-chat). */
interface VoiceReply {
  transcript: string;
  reply: { content: string; media?: MediaItem[] };
  audio?: string;
  speech_error?: string;
  timings?: { stt_ms: number; chat_ms: number; tts_ms: number };
}

/**
 * Ask every ensemble on screen a spoken question (a 16 kHz mono WAV): its
 * speech-to-text member transcribes it, its chat member answers in character,
 * and its speech member says the answer in the persona's voice.
 */
export function sendVoice(tab: Tab, wav: Blob): { sent: boolean; notes: string[] } {
  const notes: string[] = [];
  let sent = false;
  for (const p of visiblePanes(tab)) {
    if (p.modality !== "ensemble") {
      if (p.port) notes.push(`${label(p)} isn't an ensemble, so it didn't get the spoken question.`);
      continue;
    }
    if (p.busy) {
      notes.push(`${label(p)} is still answering; wait for it or stop it.`);
      continue;
    }
    voiceChat(tab, p, wav);
    sent = true;
  }
  return { sent, notes };
}

async function voiceChat(tab: Tab, p: Pane, wav: Blob) {
  const history = buildMessages(tab, p);
  const ui = p.msgs.push({ role: "user", content: "Transcribing…", spoken: true }) - 1;
  const idx = p.msgs.push({ role: "assistant", content: "", pending: { at: Date.now(), eta: 30000, label: "Listening, thinking, then speaking…" } }) - 1;
  p.busy = true;
  p.last = { path: "/api/ensembles/voice-chat", form: { file: "question.wav", port: p.port } };
  const t0 = performance.now();
  try {
    const fd = new FormData();
    fd.append("file", wav, "question.wav");
    fd.append("port", String(p.port));
    fd.append("messages", JSON.stringify(history));
    const r = await api<VoiceReply>("/api/ensembles/voice-chat", { method: "POST", body: fd });
    const heard = (r.transcript || "").trim();
    p.msgs[ui] = heard
      ? { role: "user", content: heard, spoken: true }
      : { role: "user", content: "(no words heard)", spoken: true, err: "no words" };
    const a: Msg = { role: "assistant", content: r.reply?.content ?? "", stats: { ms: performance.now() - t0 } };
    addMedia(a, r.reply?.media ?? []);
    if (r.audio) {
      a.audio = r.audio;
      a.autoplay = true;
    }
    if (r.speech_error) a.note = `It didn't speak: ${r.speech_error.replace(/\.$/, "")}.`;
    if (!heard) a.err = "Speech-to-text heard no words. Speak closer to the microphone, or check it in your system's sound settings, then try again.";
    else if (!a.content && !hasMedia(a)) a.err = `${label(p)} sent an empty reply. Try again.`;
    p.msgs[idx] = a;
  } catch (e) {
    p.msgs[ui] = { role: "user", content: "Your spoken question", spoken: true, err: "failed" };
    p.msgs[idx] = { role: "assistant", content: "", err: failText(p, e) };
  } finally {
    p.busy = false;
    save();
  }
}

async function genImage(tab: Tab, p: Pane, prompt: string) {
  p.msgs.push({ role: "user", content: prompt });
  // Replace the placeholder through the array index when done: an index
  // assignment goes through the proxy's set trap, so it's reactive.
  const idx = p.msgs.push({ role: "assistant", kind: "image", content: "", pending: { at: Date.now(), eta: 75000, label: "Generating the image…" } }) - 1;
  p.busy = true;
  const res = tab.imgRes ?? 512;
  const def = imageDefaults(p.model);
  const body = {
    port: p.port, prompt, negative_prompt: tab.negative?.trim() || undefined,
    steps: tab.imgSteps ?? def.steps, cfg_scale: def.cfg, width: res, height: res, seed: tab.mediaSeed ?? -1,
  };
  p.last = { path: "/api/image/generate", body };
  const t0 = performance.now();
  try {
    const data = await post<{ images?: string[] }>("/api/image/generate", body);
    p.msgs[idx] = data.images?.length
      ? { role: "assistant", kind: "image", content: "", images: data.images, stats: { ms: performance.now() - t0 } }
      : { role: "assistant", kind: "text", content: "", err: `${label(p)} finished without an image. Try again, or change the prompt.` };
  } catch (e) {
    p.msgs[idx] = { role: "assistant", kind: "text", content: "", err: failText(p, e) };
  } finally {
    p.busy = false;
    save();
  }
}

async function genVideo(tab: Tab, p: Pane, prompt: string) {
  p.msgs.push({ role: "user", content: prompt });
  const frames = tab.vidFrames ?? 33;
  const secs = Math.round(frames / 16);
  const eta = 35000 + frames * 2300; // sampling + VAE decode scale with frames
  const idx = p.msgs.push({ role: "assistant", kind: "video", content: "", pending: { at: Date.now(), eta, label: `Generating a ~${secs} s video…` } }) - 1;
  p.busy = true;
  const [w, h] = (tab.vidRes ?? "832x480").split("x").map(Number);
  const body = {
    port: p.port, prompt, negative: tab.negative?.trim() || undefined,
    width: w || 832, height: h || 480, frames, steps: tab.vidSteps ?? 10, seed: tab.mediaSeed ?? 0,
  };
  p.last = { path: "/api/video/generate", body };
  const t0 = performance.now();
  try {
    const data = await post<{ video?: string }>("/api/video/generate", body);
    p.msgs[idx] = data.video
      ? { role: "assistant", kind: "video", content: "", video: data.video, stats: { ms: performance.now() - t0 } }
      : { role: "assistant", kind: "text", content: "", err: `${label(p)} finished without a video. Try a shorter clip.` };
  } catch (e) {
    p.msgs[idx] = { role: "assistant", kind: "text", content: "", err: failText(p, e) };
  } finally {
    p.busy = false;
    save();
  }
}

async function fail(res: Response): Promise<never> {
  let m = res.statusText;
  try {
    m = JSON.parse(await res.text()).error || m;
  } catch {
    /* keep the status text */
  }
  throw new Error(m);
}

async function transcribe(p: Pane, file: File) {
  p.msgs.push({ role: "user", kind: "audio", content: file.name, audio: URL.createObjectURL(file) });
  const idx = p.msgs.push({ role: "assistant", content: "", pending: { at: Date.now(), eta: 20000, label: "Transcribing…" } }) - 1;
  p.busy = true;
  p.last = { path: "/api/transcribe", form: { file: file.name, port: p.port } };
  const t0 = performance.now();
  try {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("port", String(p.port));
    const res = await fetch("/api/transcribe", { method: "POST", body: fd });
    if (!res.ok) await fail(res);
    const text = (await res.json()).text as string;
    p.msgs[idx] = text
      ? { role: "assistant", content: text, stats: { ms: performance.now() - t0 } }
      : { role: "assistant", content: "", err: "No speech found in that file. Try a clearer recording." };
  } catch (e) {
    p.msgs[idx] = { role: "assistant", content: "", err: failText(p, e) };
  } finally {
    p.busy = false;
    save();
  }
}

async function speak(tab: Tab, p: Pane, text: string) {
  p.msgs.push({ role: "user", content: text });
  const idx = p.msgs.push({ role: "assistant", kind: "audio", content: "", pending: { at: Date.now(), eta: 15000, label: "Speaking…" } }) - 1;
  p.busy = true;
  // Only send what this model accepts: a voice from its own list (another
  // model's voice is refused), speed, and a style instruction.
  const spec = ttsSpec(p.model);
  const voice = spec.voices.some(([v]) => v === tab.ttsVoice) ? tab.ttsVoice : undefined;
  const body = {
    port: p.port, text, voice,
    speed: spec.speed ? tab.ttsSpeed ?? 1 : undefined,
    instructions: spec.style ? tab.ttsStyle?.trim() || undefined : undefined,
  };
  p.last = { path: "/api/tts", body, out: "speech.wav" };
  const t0 = performance.now();
  try {
    const res = await fetch("/api/tts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!res.ok) await fail(res);
    p.msgs[idx] = { role: "assistant", kind: "audio", content: "", audio: URL.createObjectURL(await res.blob()), autoplay: true, stats: { ms: performance.now() - t0 } };
  } catch (e) {
    p.msgs[idx] = { role: "assistant", kind: "text", content: "", err: failText(p, e) };
  } finally {
    p.busy = false;
    save();
  }
}

// ---- copy as curl -----------------------------------------------------------------

const sq = (s: string) => "'" + s.replace(/'/g, "'\\''") + "'";

/** The request a side would send now: its last one, else one built from the draft. */
export function requestFor(tab: Tab, p: Pane, draft: string): Req | null {
  if (!p.port) return null;
  if (p.last) return p.last;
  const text = draft.trim() || "Hello";
  switch (p.modality) {
    case "image": {
      const def = imageDefaults(p.model);
      const res = tab.imgRes ?? 512;
      return { path: "/api/image/generate", body: { port: p.port, prompt: text, negative_prompt: tab.negative?.trim() || undefined, steps: tab.imgSteps ?? def.steps, cfg_scale: def.cfg, width: res, height: res, seed: tab.mediaSeed ?? -1 } };
    }
    case "video": {
      const [w, h] = (tab.vidRes ?? "832x480").split("x").map(Number);
      return { path: "/api/video/generate", body: { port: p.port, prompt: text, negative: tab.negative?.trim() || undefined, width: w || 832, height: h || 480, frames: tab.vidFrames ?? 33, steps: tab.vidSteps ?? 10, seed: tab.mediaSeed ?? 0 } };
    }
    case "tts": {
      const spec = ttsSpec(p.model);
      const voice = spec.voices.some(([v]) => v === tab.ttsVoice) ? tab.ttsVoice : undefined;
      return { path: "/api/tts", body: { port: p.port, text, voice, speed: spec.speed ? tab.ttsSpeed ?? 1 : undefined, instructions: spec.style ? tab.ttsStyle?.trim() || undefined : undefined }, out: "speech.wav" };
    }
    case "audio-stt":
      return { path: "/api/transcribe", form: { file: "recording.wav", port: p.port } };
    case "embedding":
      // The factory has no embeddings route: llama.cpp's own, on the model's port.
      return { path: "/v1/embeddings", url: `http://localhost:${p.port}/v1/embeddings`, body: { input: text } };
    default:
      return { path: "/api/chat/stream", body: chatBody(p.port, buildMessages(tab, p, text), sampling(tab)) };
  }
}

/** A request as a curl command against this factory (the page's own address). */
export function curlFor(r: Req, origin = location.origin): string {
  const url = sq(r.url ?? origin + r.path);
  if (r.form) return `curl ${url} \\\n  -F ${sq("file=@" + r.form.file)} \\\n  -F ${sq("port=" + r.form.port)}`;
  const stream = r.path === "/api/chat/stream" ? " -N" : "";
  return (
    `curl${stream} ${url} \\\n  -H 'Content-Type: application/json' \\\n  -d ${sq(JSON.stringify(r.body ?? {}))}` +
    (r.out ? ` \\\n  -o ${r.out}` : "")
  );
}

// ---- bridge -----------------------------------------------------------------------

function escapeRegex(s: string) {
  return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
function cleanBridgeLine(text: string, self: string, other: string): string {
  let t = (text || "").replace(/<think>[\s\S]*?<\/think>/gi, "").trim();
  t = t.replace(new RegExp(`^["'\\s]*(?:(?:${escapeRegex(self)}|${escapeRegex(other)})\\s*:\\s*["']?\\s*)?`, "i"), "");
  return t.replace(/^["']\s*/, "").replace(/\s*["']\s*$/, "").trim();
}
const normLine = (s: string) => (s || "").toLowerCase().replace(/[^a-z0-9 ]+/g, " ").replace(/\s+/g, " ").trim();
function isBridgeRepeat(line: string, transcript: { text: string }[]): boolean {
  const a = normLine(line);
  if (a.length < 8) return false;
  for (const t of transcript.slice(-3)) {
    const b = normLine(t.text);
    if (!b) continue;
    if (a === b) return true;
    const shorter = a.length <= b.length ? a : b;
    const longer = a.length <= b.length ? b : a;
    if (shorter.length >= 14 && longer.includes(shorter)) return true;
    const wa = new Set(a.split(" "));
    const wb = new Set(b.split(" "));
    let inter = 0;
    wa.forEach((w) => {
      if (wb.has(w)) inter++;
    });
    if (inter / (wa.size + wb.size - inter) > 0.6) return true;
  }
  return false;
}

export function bridgeStop() {
  chat.bridgeStop = true;
  bridgeAbort?.abort();
}

/** The name a model speaks as in the bridge: its persona, else its model name. */
export function bridgeName(t: Target, other?: Target): string {
  const n = t.persona || t.name;
  return other && n === (other.persona || other.name) ? `${n} (:${t.port})` : n;
}

/** Two models take turns in character, seeded with a scene. Opens its own tab. */
export async function runBridge(a: Target, b: Target, seed: string, rounds: number, think: boolean) {
  const an = bridgeName(a, b);
  const bn = bridgeName(b, a);
  const tab = newTab();
  tab.bridge = true;
  tab.temp = 0.95;
  const p = tab.panes[0];
  p.name = `${an} ↔ ${bn}`;
  p.busy = true;
  chat.bridgeStop = false;
  chat.bridging = true;
  bridgeAbort = new AbortController();
  p.msgs.push({ role: "system", bridgeScene: true, content: seed });
  const transcript: { from: "a" | "b"; name: string; text: string }[] = [];
  let speaker: "a" | "b" = "a";
  for (let i = 0; i < rounds * 2 && !chat.bridgeStop; i++) {
    const tgt = speaker === "a" ? a : b;
    const me = speaker === "a" ? an : bn;
    const other = speaker === "a" ? bn : an;
    const convo = transcript.map((t) => `${t.name}: ${t.text}`).join("\n");
    const base =
      `You are ${me}, in a live voice chat with ${other}.\nScene: ${seed}\n\n` +
      (convo ? `Transcript so far:\n${convo}\n\n` : "") +
      `Write ONLY ${me}'s next spoken line — in character, 1–2 sentences, advancing the scene with a NEW specific action or detail. No surrounding quotes, no name label, never repeat an earlier line, and don't write ${other}'s part.`;
    const idx = p.msgs.push({ role: "assistant", bridgeFrom: me, bridgeSide: speaker, content: "" }) - 1;
    const ph = p.msgs[idx];
    let clean = "";
    for (let attempt = 0; attempt < 2 && !chat.bridgeStop; attempt++) {
      const nudge = attempt === 0 ? "" : "\n\nYou already said that. Say something COMPLETELY different — a new action, line, or reaction in the scene.";
      const userMsg = base + nudge + (think ? "" : " /no_think");
      ph.content = "";
      try {
        await streamChat(
          tgt.port,
          [{ role: "user", content: userMsg }],
          (d) => {
            ph.content += d;
          },
          bridgeAbort.signal,
          { temperature: 0.95 + attempt * 0.2, top_p: 0.95, max_tokens: think ? 400 : 200, stop: think ? [`\n${other}:`] : [`\n${other}:`, `${other}:`] },
        );
      } catch (e) {
        if ((e as Error).name === "AbortError") ph.stopped = true;
        else ph.err = `${me} on :${tgt.port} didn't answer: ${((e as Error).message || "no answer").replace(/\.$/, "")}. The bridge stopped here.`;
        chat.bridgeStop = true;
        break;
      }
      clean = cleanBridgeLine(ph.content, me, other);
      if (think || !isBridgeRepeat(clean, transcript)) break;
    }
    if (!ph.err) ph.content = think ? ph.content.trim() : clean;
    transcript.push({ from: speaker, name: me, text: clean || ph.content });
    speaker = speaker === "a" ? "b" : "a";
  }
  p.busy = false;
  bridgeAbort = null;
  chat.bridging = false;
  save();
}

// ---- export -----------------------------------------------------------------------

/** Download the tab as Markdown or JSON. False when there's nothing to export. */
export function exportTab(tab: Tab, fmt: "md" | "json"): boolean {
  const panes = visiblePanes(tab).filter((p) => p.msgs.length);
  if (!panes.length) return false;
  const who = (m: Msg) => (m.bridgeScene ? "Scene" : m.bridgeFrom || (m.role === "user" ? "You" : "Model"));
  const text = (m: Msg) => m.content || m.err || `(${m.kind || "media"})`;
  let content: string;
  let mime: string;
  let ext: string;
  if (fmt === "json") {
    content = JSON.stringify(
      {
        system: tab.system || undefined,
        sides: panes.map((p) => ({
          model: p.name, port: p.port,
          messages: p.msgs.map((m) => ({ who: who(m), kind: m.kind || "text", content: text(m), stats: m.stats })),
        })),
      },
      null,
      2,
    );
    mime = "application/json";
    ext = "json";
  } else {
    content = panes
      .map((p) => `# ${p.name || "Chat"}\n\n` + p.msgs.map((m) => `**${who(m)}:**\n\n${text(m)}`).join("\n\n---\n\n"))
      .join("\n\n\n");
    mime = "text/markdown";
    ext = "md";
  }
  const name = panes.map((p) => p.name || "chat").join("-vs-");
  const a = document.createElement("a");
  a.href = URL.createObjectURL(new Blob([content], { type: mime }));
  a.download = `chat-${name.replace(/[^a-z0-9]+/gi, "-").replace(/^-|-$/g, "").toLowerCase()}.${ext}`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  return true;
}

// ---- persistence ------------------------------------------------------------------

const KEY = "llmfactory.chat.tabs.v2";
const V1 = "llmfactory.chat.tabs.v1"; // one model per tab, before compare mode
let saveT: ReturnType<typeof setTimeout> | undefined;
let restored = false;

function storeMsg(m: Msg) {
  // Attached images and audio (data or blob URLs) aren't kept; generated images
  // and video, and an ensemble's spoken answers, are small /api/media URLs and
  // reload fine.
  const kept = (u?: string) => (u && u.startsWith("/api/media/") ? u : undefined);
  return {
    role: m.role, content: m.content || "", reasoning: m.reasoning, kind: m.kind, video: m.video, images: m.images,
    audio: kept(m.audio), spoken: m.spoken, note: m.note === "Speaking…" ? undefined : m.note,
    bridgeFrom: m.bridgeFrom, bridgeSide: m.bridgeSide, bridgeScene: m.bridgeScene,
    err: m.err, error: m.error, stopped: m.stopped, stats: m.stats,
  };
}

export function save() {
  if (saveT) return;
  saveT = setTimeout(() => {
    saveT = undefined;
    try {
      const tabs = chat.tabs.map((t) => ({
        ...t,
        panes: t.panes.map((p) => ({ id: p.id, port: p.port, modality: p.modality, model: p.model, name: p.name, msgs: p.msgs.filter((m) => !m.pending).map(storeMsg).slice(-300) })),
      }));
      localStorage.setItem(KEY, JSON.stringify({ tabs, active: chat.active, seq: tabSeq, pseq: paneSeq }));
    } catch {
      /* storage full or blocked: the chat still works, it just won't survive a reload */
    }
  }, 400);
}

function fromV1(d: any): { tabs: Tab[]; active: number | null } {
  const tabs: Tab[] = (d.tabs as any[]).map((t) => ({
    id: t.id, compare: false, bridge: !!t.bridge, system: t.system || "", persona: "",
    temp: t.temp ?? 0.4, topP: t.topP ?? 0.95, seed: t.seed ?? null, vidFrames: t.vidFrames ?? 33,
    negative: t.negative, imgRes: t.imgRes, imgSteps: t.imgSteps, vidRes: t.vidRes, vidSteps: t.vidSteps,
    ttsVoice: t.ttsVoice, ttsSpeed: t.ttsSpeed, ttsStyle: t.ttsStyle, mediaSeed: t.mediaSeed,
    panes: [{ id: ++paneSeq, port: t.port || 0, modality: t.modality || "text", model: "", name: t.bridge ? t.name || "" : "", msgs: Array.isArray(t.msgs) ? t.msgs : [], busy: false, last: null }],
  }));
  return { tabs, active: d.active ?? null };
}

/** Restore saved tabs, once per page load (the state outlives the page component). */
export function load() {
  if (restored) return;
  restored = true;
  try {
    const raw = localStorage.getItem(KEY);
    let d = raw ? JSON.parse(raw) : null;
    if (!d) {
      const old = JSON.parse(localStorage.getItem(V1) || "null");
      if (old && Array.isArray(old.tabs) && old.tabs.length) d = fromV1(old);
    }
    if (!d || !Array.isArray(d.tabs) || !d.tabs.length) return;
    chat.tabs = d.tabs.map((t: any) => ({
      temp: 0.4, topP: 0.95, seed: null, vidFrames: 33, system: "", persona: "", compare: false, ...t,
      panes: (Array.isArray(t.panes) && t.panes.length ? t.panes : [newPane()]).map((p: any) => ({ ...newPane(), ...p, busy: false, last: null, msgs: Array.isArray(p.msgs) ? p.msgs : [] })),
    }));
    chat.active = chat.tabs.some((t) => t.id === d.active) ? d.active : chat.tabs[0].id;
    tabSeq = Math.max(d.seq || 0, ...chat.tabs.map((t) => t.id || 0));
    paneSeq = Math.max(d.pseq || 0, paneSeq, ...chat.tabs.flatMap((t) => t.panes.map((p) => p.id || 0)));
  } catch {
    /* unreadable: start fresh */
  }
}
