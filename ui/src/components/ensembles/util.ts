// Ensembles page helpers: which built images can be members, and what each
// member does, as the Conductor (cmd/ensemblegate) sees them.
import { labelVal } from "../../lib/api";
import { imageRef } from "../../lib/util";
import { sentence } from "../../lib/harness";
import type { BuildJob, Ensemble, EnsembleMember, ImageInfo, Model, PersonaVoice } from "../../lib/types";

/** Modality -> the Conductor tool a member of that modality backs. */
export const TOOL_FOR: Record<string, string> = {
  text: "chat",
  code: "chat",
  reasoning: "chat",
  vision: "see_image",
  image: "generate_image",
  video: "generate_video",
  "audio-stt": "transcribe",
  tts: "speak",
};

/** What a tool does, in words. */
export const TOOL_TEXT: Record<string, string> = {
  chat: "Chat",
  see_image: "Reads images",
  generate_image: "Draws images",
  generate_video: "Makes videos",
  transcribe: "Speech to text",
  speak: "Speaks replies",
  _conductor: "Conductor",
};

/** What sends a request to each tool (cmd/ensemblegate: the router and the voice endpoints). */
export const TOOL_WHEN: Record<string, string> = {
  chat: "everything else",
  see_image: "a message with an image",
  generate_image: "“draw / make an image …”",
  generate_video: "“make a video …”",
  speak: "spoken answers, /v1/audio/speech, and “say …”",
  transcribe: "voice questions (the mic) and /v1/audio/transcriptions",
};

/** The member slots an ensemble is composed of: one member per tool. */
export interface Slot {
  tool: string;
  label: string;
  mods: string[]; // image modalities that can fill it
  help: string;
}
export const SLOTS: Slot[] = [
  { tool: "chat", label: "Chat", mods: ["text", "code", "reasoning", "vision"], help: "Answers in the persona's words. Required." },
  { tool: "see_image", label: "Vision", mods: ["vision"], help: "Reads images you attach. Without one, the chat member gets the text only." },
  { tool: "transcribe", label: "Speech to text", mods: ["audio-stt"], help: "Turns a spoken question into text, and fills in voice-clip transcripts." },
  { tool: "speak", label: "Speech", mods: ["tts"], help: "Speaks the answers in the persona's voice." },
  { tool: "generate_image", label: "Images", mods: ["image"], help: "Draws pictures asked for in the chat." },
  { tool: "generate_video", label: "Video", mods: ["video"], help: "Makes short clips asked for in the chat." },
];

export interface Candidate {
  ref: string;
  name: string;
  model: string; // catalog id ("" when unknown)
  modelName: string; // catalog name, else the image name
  modality: string;
  tool: string;
  runtime: string; // cpp | python | audiocpp ("" when unlabelled)
  /** Its memory footprint toward the budget: VRAM on a GPU, RAM on CPU; null when the catalog has no figure. */
  footprint: number | null;
  voiceClone: boolean; // a speech model that clones voices
  engine: string;
  compute: string;
}

const gpu = (c: string) => c === "cuda" || c === "vulkan";

/** Built images that can be members (an ensemble image can't be one). */
export function candidates(images: ImageInfo[], catalog: Model[]): Candidate[] {
  const out: Candidate[] = [];
  const seen = new Set<string>();
  for (const im of images) {
    if (labelVal(im.Labels, "local-llm.kind") === "ensemble") continue;
    const { ref, name } = imageRef(im);
    if (seen.has(ref)) continue;
    const model = labelVal(im.Labels, "local-llm.model");
    const cat = catalog.find((m) => m.id === model);
    const modality = labelVal(im.Labels, "local-llm.modality") || cat?.modality || "text";
    const tool = TOOL_FOR[modality];
    if (!tool) continue; // embeddings: nothing routes to them
    seen.add(ref);
    const compute = im.Compute || labelVal(im.Labels, "local-llm.compute") || "";
    const fp = cat ? (gpu(compute) ? cat.min_vram_gb : cat.min_ram_gb) : 0;
    out.push({
      ref,
      name,
      model,
      modelName: cat?.name || name.replace(/^(localhost\/)?local-llm\//, ""),
      modality,
      tool,
      runtime: labelVal(im.Labels, "local-llm.runtime") || cat?.runtime || "",
      footprint: fp > 0 ? fp : null,
      voiceClone: !!cat?.voice_clone,
      engine: im.Engine || labelVal(im.Labels, "local-llm.engine") || "docker",
      compute,
    });
  }
  return out.sort((a, b) => a.tool.localeCompare(b.tool) || a.name.localeCompare(b.name));
}

/** The member a slot holds, built from its candidate image. */
export function memberOf(slot: Slot, c: Candidate, pinned: boolean): EnsembleMember {
  return {
    tool: slot.tool,
    modality: c.modality,
    image: c.ref,
    model: c.model || undefined,
    runtime: c.runtime || undefined,
    compute: c.compute || undefined,
    vram_gb: c.footprint ?? 0,
    pinned: pinned || undefined,
    voice_clone: c.voiceClone || undefined,
  };
}

/** A persona voice in a few words. */
export function voiceText(v: PersonaVoice | undefined, clipName?: string): string {
  if (!v?.kind) return "the speech member's default voice";
  if (v.kind === "builtin") return v.name ? `the built-in voice “${v.name}”` : "a built-in voice (not picked yet)";
  return clipName ? `a voice cloned from “${clipName}”` : "a cloned voice";
}

/** The id the factory gives a new ensemble or persona (Go: Slug / slugify). */
export function slug(s: string): string {
  return s
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** The image an ensemble builds into (server default image name and tag). */
export const ensembleRef = (e: Ensemble) => `local-llm/ens-${e.id}:latest`;

/** The newest build-queue job for an ensemble. */
export function latestJob(jobs: BuildJob[], e: Ensemble): BuildJob | undefined {
  const ref = ensembleRef(e);
  let best: BuildJob | undefined;
  for (const j of jobs) {
    if (j.kind !== "ensemble" || j.ref !== ref) continue;
    if (!best || j.created_at > best.created_at) best = j;
  }
  return best;
}

/** The built ensemble image, if there is one. */
export function builtImage(images: ImageInfo[], e: Ensemble): ImageInfo | undefined {
  const [repo] = ensembleRef(e).split(":");
  return images.find((im) => {
    const r = (im.Repository || "").replace(/^localhost\//, "");
    return r === repo && (im.Tag || "") === "latest";
  });
}

/**
 * Why a saved list (config/ensembles.json, config/personas.json) didn't load, as
 * a sentence with the next step. No HTTP status means the factory didn't answer;
 * "parse <file>: …" is the Go store failing to read a damaged file (a missing
 * file is just an empty list, so moving it away starts fresh).
 */
export function listLoadError(e: unknown, file: string): string {
  const err = e as { status?: number; message?: string } | undefined;
  const msg = err?.message || "";
  if (!err?.status) return "The factory didn't answer. Check that it's running, then try again.";
  const parse = msg.match(/^parse [\w.-]+: (.*)$/);
  if (parse) {
    return `The factory couldn't read config/${file} (${parse[1]}). Fix the file, or move it away to start with an empty list, then try again.`;
  }
  return sentence(msg || "The factory answered with an error") + " Try again.";
}
