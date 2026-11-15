// Library: one row per model on this machine. Joins what the factory reports
// separately — catalog weights (/api/models), images (/api/images), containers
// (/api/containers), the build queue (/api/builds) and disk usage
// (/api/storage) — by model id and image labels, and finds the space nothing
// uses. Pure functions: the page passes the live data in.
import { labelVal } from "./api";
import { imageRef, containerRef, containerPort, isRunning } from "./util";
import type { BuildJob, ContainerInfo, ImageInfo, Model, StorageInfo, UsageItem } from "./types";

export type ModelRow = Model & { downloaded?: boolean; on_disk_gb?: number };

export interface LibImage {
  ref: string;         // repo:tag, or the short id when untagged
  repo: string;        // repository without podman's "localhost/"
  engine: string;      // docker | podman
  compute: string;     // cpu | vulkan | cuda
  sizeGB: number | null;
  created: string;     // "2 days ago", "" when the engine doesn't say
  tar: UsageItem | null; // its export under images/
  ensemble: string;    // ensemble id for an ensemble image
  info: ImageInfo;
}

export interface LibContainer {
  id: string;
  name: string;
  engine: string;
  port: number;        // host port, 0 when unknown
  ref: string;
  running: boolean;
  status: string;      // "Up 2 hours", "Exited (1) 6 minutes ago"
  persona: string;     // run-time persona label
  modality: string;
  info: ContainerInfo;
}

export type RowState = "running" | "ready" | "tar" | "building" | "weights";

export interface LibRow {
  key: string;             // "m:<model id>", "e:<ensemble id>", "i:<ref>", "t:<tar name>"
  modelId: string;
  model: ModelRow | null;
  name: string;
  modality: string;        // catalog modality, "ensemble" for ensembles
  images: LibImage[];
  containers: LibContainer[];
  jobs: BuildJob[];        // queued or running, run order
  lastJob: BuildJob | null; // newest finished build
  weightsGB: number;       // the model's files in models/ (main + projector)
  weightsFile: UsageItem | null; // the main weights file (what "Delete weights" frees)
  tars: UsageItem[];       // saved images not loaded into an engine
  disk: { weights: number; images: number; tars: number; total: number };
  states: Set<RowState>;
}

/** Docker's "3.49GB" / "812MB" / podman's "9.47 GB" -> GB. */
export function parseSize(s: string | undefined): number | null {
  const m = String(s ?? "").trim().match(/^([\d.]+)\s*([kKMGT]?B)$/);
  if (!m) return null;
  const n = parseFloat(m[1]);
  const mult: Record<string, number> = { B: 1e-9, kB: 1e-6, KB: 1e-6, MB: 1e-3, GB: 1, TB: 1e3 };
  return isFinite(n) ? n * (mult[m[2]] ?? 1) : null;
}

/** The file an image is exported to under images/ (builder.exportFileName). */
export function exportFileName(ref: string): string {
  return ref.replace(/[/:]/g, "_") + ".tar";
}

/** "local-llm_qwen3-tts-1.7b_latest.tar" -> the catalog model it holds, if any. */
function tarModel(name: string, ids: Set<string>): string {
  const m = name.match(/^local-llm_(.+)_[^_]+\.tar$/);
  return m && ids.has(m[1]) ? m[1] : "";
}

/** True for an image row with no repository or tag (left over from a rebuild). */
export function isUntagged(im: ImageInfo): boolean {
  return !im.Repository || im.Repository === "<none>" || !im.Tag || im.Tag === "<none>";
}

/** Every file a catalog model keeps in models/: weights, projector, extras. */
function modelFiles(m: Model): string[] {
  return [m.file, m.mmproj_file ?? "", ...(m.extra_files ?? []).map((f) => f.file)].filter(Boolean);
}

export interface LibInput {
  images: ImageInfo[];
  containers: ContainerInfo[];
  files: ModelRow[];     // live.files (catalog + on-disk status)
  catalog: Model[];      // fallback when live.files hasn't loaded
  storage: StorageInfo | null;
  jobs: BuildJob[];
  ensembleName?: (id: string) => string;
}

export interface Library {
  rows: LibRow[];
  untagged: LibImage[];  // images no tag points at
}

export function buildLibrary(inp: LibInput): Library {
  const models: ModelRow[] = inp.files.length ? inp.files : inp.catalog;
  const byId = new Map(models.map((m) => [m.id, m]));
  const ids = new Set(byId.keys());
  const tars = new Map((inp.storage?.tars.items ?? []).map((t) => [t.name, t]));
  const weights = new Map((inp.storage?.weights.items ?? []).map((w) => [w.name, w]));
  const claimed = new Set<string>();
  const rows = new Map<string, LibRow>();
  const untagged: LibImage[] = [];

  function row(key: string, modelId: string, name: string, modality: string): LibRow {
    let r = rows.get(key);
    if (!r) {
      const model = modelId ? byId.get(modelId) ?? null : null;
      r = {
        key, modelId, model,
        name: model?.name || name,
        modality: model?.modality || modality || "text",
        images: [], containers: [], jobs: [], lastJob: null,
        weightsGB: 0, weightsFile: null, tars: [],
        disk: { weights: 0, images: 0, tars: 0, total: 0 },
        states: new Set(),
      };
      rows.set(key, r);
    }
    return r;
  }

  // Images: one row per model id (an image built for CPU on Docker and one for
  // Vulkan on Podman are the same model), ensembles and unlabelled images by ref.
  const imageRow = new Map<string, LibRow>(); // ref -> row, for containers
  for (const im of inp.images) {
    const r0 = imageRef(im);
    const engine = im.Engine === "podman" ? "podman" : "docker";
    const repo = r0.name.replace(/^localhost\//, "");
    const bare = r0.ref.replace(/^localhost\//, "");
    const tar = tars.get(exportFileName(bare)) ?? tars.get(exportFileName(r0.ref)) ?? null;
    if (tar) claimed.add(tar.name);
    const li: LibImage = {
      ref: r0.ref, repo, engine,
      compute: labelVal(im.Labels, "local-llm.compute") || im.Compute || "cpu",
      sizeGB: parseSize(im.Size),
      created: String((im as Record<string, unknown>).CreatedSince ?? ""),
      tar,
      ensemble: labelVal(im.Labels, "local-llm.ensemble"),
      info: im,
    };
    if (isUntagged(im)) {
      untagged.push(li);
      continue;
    }
    const mid = labelVal(im.Labels, "local-llm.model");
    let r: LibRow;
    if (mid) r = row("m:" + mid, mid, repo, labelVal(im.Labels, "local-llm.modality"));
    else if (li.ensemble) r = row("e:" + li.ensemble, "", inp.ensembleName?.(li.ensemble) || repo, "ensemble");
    else r = row("i:" + bare, "", repo, labelVal(im.Labels, "local-llm.modality"));
    r.images.push(li);
    imageRow.set(r0.ref, r);
    imageRow.set(bare, r);
  }

  // Containers (running and stopped): by model label, else by the image they run.
  for (const c of inp.containers) {
    const ref = containerRef(c);
    const mid = labelVal(c.Labels, "local-llm.model");
    const lc: LibContainer = {
      id: c.ID || c.Names || "",
      name: (c.Names || "").replace(/^\//, ""),
      engine: c.Engine === "podman" ? "podman" : "docker",
      port: parseInt(containerPort(c), 10) || 0,
      ref,
      running: isRunning(c),
      status: c.Status || c.State || "",
      persona: labelVal(c.Labels, "local-llm.persona"),
      modality: labelVal(c.Labels, "local-llm.modality"),
      info: c,
    };
    const r = mid
      ? row("m:" + mid, mid, ref, lc.modality)
      : imageRow.get(ref) ?? imageRow.get(ref.replace(/^localhost\//, "")) ?? row("i:" + ref.replace(/^localhost\//, ""), "", ref, lc.modality);
    r.containers.push(lc);
  }

  // Builds: queued and running ones on the row; the newest finished one as history.
  const active = inp.jobs.filter((j) => j.active && j.kind === "model");
  active.sort((a, b) => (a.status === b.status ? a.position - b.position : a.status === "running" ? -1 : 1));
  for (const j of active) row("m:" + j.model_id, j.model_id, j.name, "").jobs.push(j);
  for (const j of inp.jobs) {
    if (j.active || j.kind !== "model") continue;
    const r = rows.get("m:" + j.model_id);
    if (r && (!r.lastJob || (j.finished_at || 0) > (r.lastJob.finished_at || 0))) r.lastJob = j;
  }

  // Weights on disk: a row of its own when nothing else has the model yet. Two
  // catalog entries can share a weights file (Gemma 3 4B and its vision variant):
  // a variant whose own extra files are missing doesn't get a row for the
  // shared file alone.
  for (const m of models) {
    const gbOnDisk = m.on_disk_gb ?? 0;
    if (!m.downloaded && gbOnDisk <= 0) continue;
    if (!m.downloaded && !rows.has("m:" + m.id) && models.some((x) => x.id !== m.id && x.file === m.file && x.downloaded)) continue;
    const r = row("m:" + m.id, m.id, m.name, m.modality ?? "text");
    r.weightsGB = gbOnDisk;
    r.weightsFile = weights.get(m.file) ?? null;
  }
  // A model whose weights are only known from /api/storage (files not loaded yet).
  if (!inp.files.length) {
    for (const m of models) {
      const w = weights.get(m.file);
      if (!w) continue;
      const r = row("m:" + m.id, m.id, m.name, m.modality ?? "text");
      r.weightsFile = w;
      r.weightsGB = modelFiles(m).reduce((s, f) => s + (weights.get(f)?.gb ?? 0), 0);
    }
  }

  // Saved images (.tar) whose image isn't loaded into an engine.
  for (const t of inp.storage?.tars.items ?? []) {
    if (claimed.has(t.name)) continue;
    const mid = tarModel(t.name, ids);
    const r = mid ? row("m:" + mid, mid, t.name, "") : row("t:" + t.name, "", t.name.replace(/\.tar$/, ""), "");
    r.tars.push(t);
  }

  for (const r of rows.values()) {
    const imgGB = r.images.reduce((s, i) => s + (i.sizeGB ?? 0), 0);
    const tarGB = r.images.reduce((s, i) => s + (i.tar?.gb ?? 0), 0) + r.tars.reduce((s, t) => s + t.gb, 0);
    r.disk = { weights: r.weightsGB, images: imgGB, tars: tarGB, total: r.weightsGB + imgGB + tarGB };
    if (r.containers.some((c) => c.running)) r.states.add("running");
    if (r.images.length && !r.containers.some((c) => c.running)) r.states.add("ready");
    if (r.jobs.length) r.states.add("building");
    if (r.tars.length && !r.images.length) r.states.add("tar");
    if (!r.images.length && !r.tars.length && !r.jobs.length && !r.containers.length && r.weightsGB > 0) r.states.add("weights");
  }

  const order = (r: LibRow) =>
    r.states.has("running") ? 0 : r.states.has("ready") ? 1 : r.states.has("tar") ? 2 : r.states.has("building") ? 3 : 4;
  // Running models in port order (8080, 8081…), the rest by name.
  const port = (r: LibRow) => Math.min(...r.containers.filter((c) => c.running && c.port).map((c) => c.port), 1e6);
  const out = [...rows.values()].sort(
    (a, b) => order(a) - order(b) || (order(a) === 0 ? port(a) - port(b) : 0) || a.name.localeCompare(b.name),
  );
  return { rows: out, untagged };
}

// ---- cleanup -----------------------------------------------------------------------

export type CandidateGroup = "files" | "untagged" | "weights" | "tars";

export interface Candidate {
  key: string;
  group: CandidateGroup;
  title: string;
  why: string;
  gb: number | null;
  /** Something the factory can remove for you (each asks first). */
  action?: { kind: "weights"; modelId: string; name: string } | { kind: "image"; ref: string; engine: string };
  /** A file the API can't remove: the command to run in the factory folder. */
  cmd?: string;
}

/**
 * The optional groups in the cleanup card, in order: things something could
 * still want (a rebuild, a spare copy), so they're listed apart from the
 * space nothing uses and left out of its total.
 */
export const OPTIONAL: { id: CandidateGroup; title: string; why: string }[] = [
  { id: "weights", title: "Weights already inside an image", why: "Only needed to rebuild; a rebuild downloads them again." },
  { id: "tars", title: "Saved copies of loaded images", why: "Exports in images/ of images that are loaded anyway." },
];
export const isOptional = (c: Candidate) => c.group === "weights" || c.group === "tars";

/** Space nothing uses: leftover build folders, unused files and untagged images. */
export function reclaimable(storage: StorageInfo | null, cands: Candidate[]): number {
  return (storage?.leftovers.gb ?? 0) + cands.filter((c) => !isOptional(c)).reduce((s, c) => s + (c.gb ?? 0), 0);
}

/**
 * Space nothing needs right now, besides the leftover build folders (which
 * POST /api/storage/cleanup removes): weights already baked into an image,
 * files in models/ that no model in the catalog uses, exports of images that
 * are loaded anyway, and untagged images.
 */
export function candidates(lib: Library, storage: StorageInfo | null, catalog: Model[]): Candidate[] {
  const out: Candidate[] = [];
  const users = new Map<string, Model[]>(); // file in models/ -> the catalog models that use it
  for (const m of catalog) for (const f of modelFiles(m)) users.set(f, [...(users.get(f) ?? []), m]);
  const byModel = new Map(lib.rows.filter((r) => r.modelId).map((r) => [r.modelId, r]));
  // A build that's queued or running needs its model's files.
  const needed = (m: Model) => !!byModel.get(m.id)?.jobs.length;

  for (const r of lib.rows) {
    if (!r.model || !r.weightsFile || !r.images.length) continue;
    const sharers = (users.get(r.model.file) ?? []).filter((m) => m.id !== r.modelId);
    if (needed(r.model) || sharers.some(needed)) continue;
    const alsoGone = sharers.filter((m) => byModel.get(m.id)?.weightsFile);
    out.push({
      key: "w:" + r.modelId,
      group: "weights",
      title: r.name,
      why:
        `models/${r.model.file}` +
        (alsoGone.length ? `, also used by ${alsoGone.map((m) => m.name).join(" and ")}` : ""),
      gb: r.weightsFile.gb,
      action: { kind: "weights", modelId: r.modelId, name: r.name },
    });
  }

  for (const w of storage?.weights.items ?? []) {
    const ms = users.get(w.name) ?? [];
    // A model's main weights are offered above (or still needed); a projector or
    // extra file is unused once no model that needs it has its main weights here.
    if (ms.some((m) => m.file === w.name)) continue;
    if (ms.some((m) => byModel.get(m.id)?.weightsFile || needed(m))) continue;
    const m = ms[0];
    out.push({
      key: "f:" + w.name,
      group: "files",
      title: m ? `${m.mmproj_file === w.name ? "Vision projector" : "Extra file"} for ${m.name}` : w.name,
      why: m ? `${m.name} has no weights here, so nothing uses it.` : "No model in the catalog uses this file.",
      gb: w.gb,
      cmd: `rm "models/${w.name}"`,
    });
  }

  for (const im of lib.untagged) {
    out.push({
      key: "u:" + im.engine + im.ref,
      group: "untagged",
      title: `Image ${im.ref}`,
      why: `In ${im.engine === "podman" ? "Podman" : "Docker"}${im.created ? `, built ${im.created}` : ""}.`,
      gb: im.sizeGB,
      action: { kind: "image", ref: im.ref, engine: im.engine },
    });
  }

  for (const r of lib.rows) {
    for (const im of r.images) {
      if (!im.tar) continue;
      out.push({
        key: "t:" + im.tar.name,
        group: "tars",
        title: im.repo,
        why: `images/${im.tar.name}; the image is loaded in ${im.engine === "podman" ? "Podman" : "Docker"}.`,
        gb: im.tar.gb,
        cmd: `rm "images/${im.tar.name}"`,
      });
    }
  }
  return out;
}

// ---- display ----------------------------------------------------------------------

const KIND: Record<string, string> = {
  text: "Text", code: "Code", reasoning: "Reasoning", vision: "Vision", embedding: "Embeddings",
  image: "Image generation", video: "Video generation", "audio-stt": "Speech to text", tts: "Voice",
  ensemble: "Ensemble",
};
export const kindLabel = (mod: string) => KIND[mod] ?? "Text";

/** 1 -> "1st", 2 -> "2nd". */
export function ordinal(n: number): string {
  const s = n % 100 >= 11 && n % 100 <= 13 ? "th" : ({ 1: "st", 2: "nd", 3: "rd" } as Record<number, string>)[n % 10] ?? "th";
  return n + s;
}

/** Other catalog models built from m's weights file: deleting it removes theirs too. */
export function sharedWeights(m: Model, catalog: Model[]): Model[] {
  return catalog.filter((x) => x.id !== m.id && !!x.file && x.file === m.file);
}

/** Lowest free host port from 8080 up, skipping ports the factory's containers use. */
export function nextFreePort(containers: ContainerInfo[], start = 8080): number {
  const used = new Set(containers.map((c) => parseInt(containerPort(c), 10)).filter((p) => p > 0));
  let p = start;
  while (used.has(p)) p++;
  return p;
}

/** What a row can ask the page to do (the page owns the dialogs). */
export interface RowActions {
  run: (row: LibRow, im: LibImage) => void;
  stop: (row: LibRow, c: LibContainer) => void;
  logs: (row: LibRow, c: LibContainer) => void;
  config: (im: LibImage) => void;
  template: (im: LibImage) => void;
  download: (im: LibImage) => void;
  deleteImage: (row: LibRow, im: LibImage) => void;
  deleteWeights: (row: LibRow) => void;
  build: (row: LibRow) => void;
}

/** "2.7", "0.85", "12" — a GB figure without its unit, for breakdowns. */
export function num(gbVal: number): string {
  if (gbVal > 0 && gbVal < 0.1) return gbVal.toFixed(2);
  return (gbVal >= 100 ? gbVal.toFixed(0) : gbVal.toFixed(1)).replace(/\.0$/, "");
}

/** What a confirm dialog asks before removing something. */
export interface ConfirmSpec {
  title: string;
  body: string;          // what happens, plainly
  frees?: string;        // "Frees 3.5 GB."
  note?: string;         // a caveat (shared weights, a running container…)
  confirm: string;       // button label
  danger?: boolean;
  run: () => Promise<void>;
}

/** A model, image ref or container port from "#library/<arg>" -> the row it names. */
export function findRow(rows: LibRow[], arg: string): LibRow | undefined {
  if (!arg) return undefined;
  const bare = arg.replace(/^localhost\//, "");
  return (
    rows.find((r) => r.key === arg || r.modelId === arg) ??
    rows.find((r) => r.images.some((i) => i.ref === arg || i.ref.replace(/^localhost\//, "") === bare || i.repo === bare)) ??
    rows.find((r) => r.containers.some((c) => c.ref === arg || c.name === arg || String(c.port) === arg)) ??
    rows.find((r) => r.tars.some((t) => t.name === arg))
  );
}
