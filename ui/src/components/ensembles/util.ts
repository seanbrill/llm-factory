// Ensembles page helpers: which built images can be members, and what each
// member does, as the Conductor (cmd/ensemblegate) sees them.
import { labelVal } from "../../lib/api";
import { imageRef } from "../../lib/util";
import { sentence } from "../../lib/harness";
import type { BuildJob, Ensemble, ImageInfo, Model } from "../../lib/types";

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
  see_image: "Images in messages",
  generate_image: "Draws images",
  generate_video: "Makes videos",
  transcribe: "Speech to text",
  speak: "Speaks replies",
};

/** When the heuristic router sends a request to each tool (ensemblegate classify). */
export const TOOL_WHEN: Record<string, string> = {
  chat: "everything else",
  see_image: "a message with an image",
  generate_image: "“draw / make an image …”",
  generate_video: "“make a video …”",
  speak: "“say / read aloud …”",
  transcribe: "not routed yet",
};

export interface Candidate {
  ref: string;
  name: string;
  model: string; // catalog id ("" when unknown)
  modality: string;
  tool: string;
  vram: number | null; // catalog min_vram_gb, null when it has none
  engine: string;
  compute: string;
}

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
    out.push({
      ref,
      name,
      model,
      modality,
      tool,
      vram: cat?.min_vram_gb || null, // 0 = no figure; saving counts it as 6 GB
      engine: im.Engine || labelVal(im.Labels, "local-llm.engine") || "docker",
      compute: im.Compute || labelVal(im.Labels, "local-llm.compute") || "",
    });
  }
  return out.sort((a, b) => a.tool.localeCompare(b.tool) || a.name.localeCompare(b.name));
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
