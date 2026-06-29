// Chat state + logic (threads, streaming, media gen, bridge, persistence), ported
// from the vanilla app into a Svelte 5 rune module so components share reactivity.
import { api, post, streamChat, labelVal, type StreamOpts } from "./api";
import { modMeta } from "./icons";
import { isStreamMod } from "./util";
import type { ContainerInfo, ImageInfo, Model } from "./types";

export interface Msg {
  role: "user" | "assistant" | "system";
  content: string;
  kind?: string;
  image?: string;
  images?: string[];
  audio?: string;
  video?: string;
  bridgeFrom?: string;
  bridgeSide?: "a" | "b";
  bridgeScene?: boolean;
  error?: boolean;
  // Present while a media generation is in flight — drives the progress ring.
  // `at` = start ms, `eta` = expected duration ms (for the % estimate), `label`.
  pending?: { at: number; eta: number; label: string };
}
export interface Tab {
  id: number;
  port: number;
  modality: string;
  name: string;
  system: string;
  msgs: Msg[];
  busy: boolean;
  bridge?: boolean;
  temp: number;
  topP: number;
  seed: number | null;
  // Video-gen frame count (Wan plays ~16fps, so frames/16 ≈ seconds). Must be
  // 4k+1 for the Wan VAE's temporal compression (17,33,49,65,81).
  vidFrames?: number;
}
export interface Running { port: number; name: string; modality: string; ref: string; }

export const chat = $state({
  tabs: [] as Tab[],
  running: [] as Running[],
  active: null as number | null,
  bridgeStop: false,
  bridging: false,
});

let tabSeq = 0;
let bridgeAbort: AbortController | null = null;
let catalogRef: Model[] = [];
export function setCatalog(c: Model[]) { catalogRef = c; }

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
};

export function activeTab(): Tab | null { return chat.tabs.find((t) => t.id === chat.active) ?? null; }
export function isPersonaName(n?: string): boolean {
  return !!n && !/^\/?localllm-/i.test(n) && !/^model:/i.test(n) && n !== "(no model)";
}
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

const isRunning = (c: ContainerInfo) => (c.State || "").toLowerCase() === "running" || /^Up/.test(c.Status || "");
const cRef = (c: ContainerInfo) => labelVal(c.Labels, "local-llm.ref") || (c.Image ?? "");
const cPort = (c: ContainerInfo) => {
  const m = (c.Names || "").match(/localllm-(\d+)/);
  if (m) return parseInt(m[1], 10);
  const pm = (c.Ports || "").match(/:(\d+)->/);
  return pm ? parseInt(pm[1], 10) : 0;
};

export async function refreshTargets() {
  let cs: ContainerInfo[] = [], imgs: ImageInfo[] = [];
  try { cs = await api("/api/containers"); } catch { /* none */ }
  try { imgs = await api("/api/images"); } catch { /* none */ }
  const modByRef: Record<string, string> = {}, nameByRef: Record<string, string> = {};
  for (const im of imgs) {
    const repo = im.Repository && im.Repository !== "<none>" ? im.Repository : null;
    const tagged = im.Tag && im.Tag !== "<none>" ? im.Tag : null;
    const shortId = (im.ID || "").replace(/^sha256:/, "").slice(0, 12);
    const ref = repo && tagged ? `${repo}:${tagged}` : shortId;
    modByRef[ref] = labelVal(im.Labels, "local-llm.modality") ||
      catalogRef.find((m) => m.id === labelVal(im.Labels, "local-llm.model"))?.modality || "text";
    nameByRef[ref] = personaName(labelVal(im.Labels, "local-llm.system_prompt"));
  }
  chat.running = cs.filter(isRunning).map((c) => {
    const ref = cRef(c), port = cPort(c);
    // The container carries its own modality label (always set at run time). The
    // images list returns empty Labels (`docker images` doesn't expose them), so
    // the container label — not the image map — is the reliable source.
    let mod = labelVal(c.Labels, "local-llm.modality") || modByRef[ref];
    if (!mod) { const hit = catalogRef.find((m) => ref.includes(m.id)); mod = hit ? (hit.modality ?? "text") : "text"; }
    const name = labelVal(c.Labels, "local-llm.persona")
      || personaName(labelVal(c.Labels, "local-llm.system_prompt"))
      || nameByRef[ref] || c.Names || "model:" + port;
    return { port, name, modality: mod, ref };
  });
  for (const tab of chat.tabs) {
    if (tab.bridge) continue;
    const r = chat.running.find((x) => x.port === tab.port);
    if (r) { tab.modality = r.modality; tab.name = r.name; }
  }
  if (!chat.tabs.length && chat.running.length) newTab(chat.running[0].port);
  save();
}

export function newTab(port: number) {
  const r = chat.running.find((x) => x.port === port) ?? chat.running[0];
  const tab: Tab = {
    id: ++tabSeq, port: r ? r.port : 0, modality: r ? r.modality : "text",
    name: r ? r.name : "(no model)", system: "", msgs: [], busy: false, temp: 0.4, topP: 0.95, seed: null, vidFrames: 33,
  };
  chat.tabs.push(tab);
  chat.active = tab.id;
  save();
}
export function newChatTab() {
  if (!chat.running.length) return;
  newTab(chat.running[0].port);
}
export function closeTab(id: number) {
  const i = chat.tabs.findIndex((t) => t.id === id);
  if (i < 0) return;
  chat.tabs.splice(i, 1);
  if (chat.active === id) chat.active = chat.tabs.length ? chat.tabs[Math.max(0, i - 1)].id : null;
  if (!chat.tabs.length && chat.running.length) newTab(chat.running[0].port);
  save();
}
export function clearChat() { const t = activeTab(); if (t) { t.msgs = []; save(); } }

function tabSampling(t: Tab): StreamOpts {
  const o: StreamOpts = { temperature: t.temp ?? 0.4, top_p: t.topP ?? 0.95 };
  if (t.seed != null) o.seed = t.seed;
  return o;
}
function buildMessages(t: Tab): any[] {
  const out: any[] = [];
  if (t.system?.trim()) out.push({ role: "system", content: t.system.trim() });
  for (const m of t.msgs) {
    if (m.role === "assistant") { if (!m.kind || m.kind === "text") out.push({ role: "assistant", content: m.content || "" }); continue; }
    if (m.role !== "user") continue;
    if (m.image) out.push({ role: "user", content: [{ type: "text", text: m.content || "" }, { type: "image_url", image_url: { url: m.image } }] });
    else if (!m.kind || m.kind === "text") out.push({ role: "user", content: m.content || "" });
  }
  return out;
}

export async function send(text: string, pendingImage?: string, pendingFile?: File) {
  const tab = activeTab();
  if (!tab || tab.bridge || tab.busy) return;
  if (!tab.port) { alert("No running model selected — Run one from the Images page first."); return; }
  const mod = tab.modality;
  if (mod === "image") { if (text) genImage(tab, text); return; }
  if (mod === "video") { if (text) genVideo(tab, text); return; }
  if (mod === "tts") { if (text) speak(tab, text); return; }
  if (mod === "audio-stt") { if (pendingFile) transcribe(tab, pendingFile); else alert("Attach an audio file first."); return; }
  if (mod === "embedding") { alert("Embedding models return vectors, not chat replies."); return; }
  if (!text && !pendingImage) return;
  const um: Msg = { role: "user", content: text };
  if (pendingImage) um.image = pendingImage;
  tab.msgs.push(um);
  sendStream(tab);
}

async function sendStream(tab: Tab) {
  const messages = buildMessages(tab);
  const asst: Msg = { role: "assistant", content: "" };
  tab.msgs.push(asst);
  tab.busy = true;
  try {
    await streamChat(tab.port, messages, (d) => { asst.content += d; }, undefined, tabSampling(tab));
    if (!asst.content) asst.content = "(empty response)";
  } catch (e) { asst.content += (asst.content ? "\n\n" : "") + "⚠ " + (e as Error).message; asst.error = true; }
  finally { tab.busy = false; save(); }
}
async function genImage(tab: Tab, prompt: string) {
  tab.msgs.push({ role: "user", content: prompt });
  // Reassign the placeholder THROUGH the array index on completion (push returns
  // the new length) rather than mutating a captured ref — an index assignment
  // goes through the $state proxy's set trap, so the result is unambiguously
  // reactive (a raw-ref mutation can be missed by the deep proxy).
  const idx = tab.msgs.push({ role: "assistant", kind: "image", content: "", pending: { at: Date.now(), eta: 75000, label: "Generating image…" } }) - 1;
  tab.busy = true;
  try {
    const data = await post<{ images?: string[] }>("/api/image/generate", { port: tab.port, prompt, steps: 8, cfg_scale: 1.5, width: 512, height: 512, seed: -1 });
    tab.msgs[idx] = data.images?.length
      ? { role: "assistant", kind: "image", content: "", images: data.images }
      : { role: "assistant", kind: "text", content: "(no image returned)" };
  } catch (e) {
    tab.msgs[idx] = { role: "assistant", kind: "text", content: "⚠ " + (e as Error).message, error: true };
  } finally { tab.busy = false; save(); }
}
async function genVideo(tab: Tab, prompt: string) {
  tab.msgs.push({ role: "user", content: prompt });
  const frames = tab.vidFrames ?? 33;
  const secs = Math.round(frames / 16);
  // Generation time scales with frame count (sampling + VAE decode).
  const eta = 35000 + frames * 2300;
  const idx = tab.msgs.push({ role: "assistant", kind: "video", content: "", pending: { at: Date.now(), eta, label: `Generating ~${secs}s video…` } }) - 1;
  tab.busy = true;
  try {
    const data = await post<{ video?: string }>("/api/video/generate", { port: tab.port, prompt, height: 480, width: 832, frames, seed: 0 });
    tab.msgs[idx] = data.video
      ? { role: "assistant", kind: "video", content: "", video: data.video }
      : { role: "assistant", kind: "text", content: "(no video returned)" };
  } catch (e) {
    tab.msgs[idx] = { role: "assistant", kind: "text", content: "⚠ " + (e as Error).message, error: true };
  } finally { tab.busy = false; save(); }
}
async function transcribe(tab: Tab, file: File) {
  tab.msgs.push({ role: "user", kind: "audio", content: file.name, audio: URL.createObjectURL(file) });
  const ph: Msg = { role: "assistant", content: "Transcribing…" };
  tab.msgs.push(ph); tab.busy = true;
  try {
    const fd = new FormData(); fd.append("file", file); fd.append("port", String(tab.port));
    const res = await fetch("/api/transcribe", { method: "POST", body: fd });
    if (!res.ok) { let m = res.statusText; try { m = JSON.parse(await res.text()).error || m; } catch { /* */ } throw new Error(m); }
    ph.content = (await res.json()).text || "(no speech detected)";
  } catch (e) { ph.content = "⚠ " + (e as Error).message; ph.error = true; }
  finally { tab.busy = false; save(); }
}
async function speak(tab: Tab, text: string) {
  tab.msgs.push({ role: "user", content: text });
  const ph: Msg = { role: "assistant", kind: "audio", content: "Synthesizing…" };
  tab.msgs.push(ph); tab.busy = true;
  try {
    const res = await fetch("/api/tts", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ port: tab.port, text }) });
    if (!res.ok) { let m = res.statusText; try { m = JSON.parse(await res.text()).error || m; } catch { /* */ } throw new Error(m); }
    ph.content = ""; ph.audio = URL.createObjectURL(await res.blob());
  } catch (e) { ph.kind = "text"; ph.content = "⚠ " + (e as Error).message; ph.error = true; }
  finally { tab.busy = false; save(); }
}

// ---- bridge ---------------------------------------------------------------
function escapeRegex(s: string) { return String(s).replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }
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
    const shorter = a.length <= b.length ? a : b, longer = a.length <= b.length ? b : a;
    if (shorter.length >= 14 && longer.includes(shorter)) return true;
    const wa = new Set(a.split(" ")), wb = new Set(b.split(" "));
    let inter = 0; wa.forEach((w) => { if (wb.has(w)) inter++; });
    if (inter / (wa.size + wb.size - inter) > 0.6) return true;
  }
  return false;
}
export function bridgeStop() { chat.bridgeStop = true; bridgeAbort?.abort(); }
export async function runBridge(aPort: number, bPort: number, seed: string, rounds: number, think: boolean) {
  const a = chat.running.find((r) => r.port === aPort);
  const b = chat.running.find((r) => r.port === bPort);
  if (!a || !b || !seed) { alert("Pick two models and a seed message."); return; }
  const tab: Tab = { id: ++tabSeq, port: 0, modality: "text", bridge: true, busy: true, name: `${a.name} ↔ ${b.name}`, system: "", msgs: [], temp: 0.95, topP: 0.95, seed: null };
  chat.tabs.push(tab); chat.active = tab.id;
  chat.bridgeStop = false; chat.bridging = true;
  bridgeAbort = new AbortController();
  tab.msgs.push({ role: "system", bridgeScene: true, content: seed });
  const transcript: { from: "a" | "b"; name: string; text: string }[] = [];
  let speaker: "a" | "b" = "a";
  for (let i = 0; i < rounds * 2 && !chat.bridgeStop; i++) {
    const tgt = speaker === "a" ? a : b;
    const other = speaker === "a" ? b : a;
    const convo = transcript.map((t) => `${t.name}: ${t.text}`).join("\n");
    const base =
      `You are ${tgt.name}, in a live voice chat with ${other.name}.\nScene: ${seed}\n\n` +
      (convo ? `Transcript so far:\n${convo}\n\n` : "") +
      `Write ONLY ${tgt.name}'s next spoken line — in character, 1–2 sentences, advancing the scene with a NEW specific action or detail. No surrounding quotes, no name label, never repeat an earlier line, and don't write ${other.name}'s part.`;
    const ph: Msg = { role: "assistant", bridgeFrom: tgt.name, bridgeSide: speaker, content: "" };
    tab.msgs.push(ph);
    let clean = "";
    for (let attempt = 0; attempt < 2 && !chat.bridgeStop; attempt++) {
      const nudge = attempt === 0 ? "" : "\n\nYou already said that. Say something COMPLETELY different — a new action, line, or reaction in the scene.";
      const userMsg = base + nudge + (think ? "" : " /no_think");
      ph.content = "";
      try {
        await streamChat(tgt.port, [{ role: "user", content: userMsg }], (d) => { ph.content += d; }, bridgeAbort.signal,
          { temperature: 0.95 + attempt * 0.2, top_p: 0.95, max_tokens: think ? 400 : 200, stop: think ? [`\n${other.name}:`] : [`\n${other.name}:`, `${other.name}:`] });
      } catch (e) {
        if ((e as Error).name === "AbortError") ph.content += " (stopped)"; else { ph.content = "⚠ " + (e as Error).message; ph.error = true; }
        chat.bridgeStop = true; break;
      }
      clean = cleanBridgeLine(ph.content, tgt.name, other.name);
      if (ph.error || think || !isBridgeRepeat(clean, transcript)) break;
    }
    if (!ph.error) ph.content = think ? ph.content.trim() : clean;
    transcript.push({ from: speaker, name: tgt.name, text: clean || ph.content });
    speaker = speaker === "a" ? "b" : "a";
  }
  tab.busy = false; bridgeAbort = null; chat.bridging = false; save();
}

// ---- persistence ----------------------------------------------------------
const KEY = "llmfactory.chat.tabs.v1";
let saveT: ReturnType<typeof setTimeout> | undefined;
export function save() {
  if (saveT) return;
  saveT = setTimeout(() => {
    saveT = undefined;
    try {
      const tabs = chat.tabs.map((t) => ({
        id: t.id, port: t.port, modality: t.modality, name: t.name, system: t.system || "",
        bridge: !!t.bridge, temp: t.temp, topP: t.topP, seed: t.seed, vidFrames: t.vidFrames,
        // video/images are now small /api/media URLs (not data URLs), so they
        // persist fine and the clip reloads after a restart or container kill.
        msgs: (t.msgs || []).map((m) => ({ role: m.role, content: m.content || "", kind: m.kind, video: m.video, images: m.images, bridgeFrom: m.bridgeFrom, bridgeSide: m.bridgeSide, bridgeScene: m.bridgeScene, error: m.error })).slice(-300),
      }));
      localStorage.setItem(KEY, JSON.stringify({ tabs, active: chat.active, seq: tabSeq }));
    } catch { /* quota */ }
  }, 400);
}
export function load() {
  try {
    const d = JSON.parse(localStorage.getItem(KEY) || "null");
    if (!d || !Array.isArray(d.tabs) || !d.tabs.length) return;
    chat.tabs = d.tabs.map((t: any) => ({ temp: 0.4, topP: 0.95, seed: null, vidFrames: 33, ...t, busy: false, msgs: Array.isArray(t.msgs) ? t.msgs : [] }));
    chat.active = chat.tabs.some((t) => t.id === d.active) ? d.active : chat.tabs[0].id;
    tabSeq = Math.max(d.seq || 0, ...chat.tabs.map((t) => t.id || 0));
  } catch { /* ignore */ }
}

export { modMeta, isStreamMod };
