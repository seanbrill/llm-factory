import type { Tuning } from "./tuning";
// Catalog helpers (sheet frames f8, f9): what a model needs, where it can run
// on this machine right now and why not, what the library already has of it,
// the quick picks, and how a build plan lines up in the engines' queues.
// Pure functions of /api/catalog, /api/sysinfo and the live data — no state.
//
// The memory rules mirror the server's: a build that compiles a runtime needs
// compile_min_gb on the engine that compiles it (decideBuild), and running
// needs the weights plus ~2 GB in the engine's machine (engineFit). A model
// "needs" the larger of that and the catalog's minimum RAM.
import type { BuildJob, ContainerInfo, EngineResources, HarnessConfig, ImageInfo, Model, SysInfo } from "./types";
import { labelVal } from "./api";
import { containerPort, isRunning, imageRef } from "./util";
import { computeName, engineName, gb } from "./format";
import { DOCKER_MEMORY_PATH } from "./explain";
import { META, dimsFor } from "./meta";
import { hwPerf, engineSys } from "./perf";

// ---- kinds ------------------------------------------------------------------

/** Filter order and labels (frame f8). */
export const KINDS: { id: string; label: string }[] = [
  { id: "text", label: "Text" },
  { id: "vision", label: "Vision" },
  { id: "code", label: "Code" },
  { id: "reasoning", label: "Reasoning" },
  { id: "embedding", label: "Embedding" },
  { id: "image", label: "Image" },
  { id: "audio-stt", label: "Speech" },
  { id: "tts", label: "Voice" },
  { id: "video", label: "Video" },
];

/** The chip a card shows for its kind (plain chat models need none). */
const KIND_CHIP: Record<string, string> = {
  vision: "Vision", code: "Code", reasoning: "Reasoning", embedding: "Embedding",
  image: "Image", video: "Video", "audio-stt": "Speech to text", tts: "Voice",
};

export const kindOf = (m: Model): string => m.modality ?? "text";

/** A mixture-of-experts model: "26B-A4B" params, or "(MoE)" in the name. */
export const isMoE = (m: Model): boolean => /\(MoE\)/i.test(m.name) || /\d-A\d/i.test(m.params || "");

export function kindChip(m: Model): string {
  return KIND_CHIP[kindOf(m)] ?? (isMoE(m) ? "MoE" : "");
}

/** "Gemma 4 12B Instruct" -> "Gemma 4 12B", "Z-Image Turbo (Q4_K)" -> "Z-Image Turbo". */
export function shortName(m: Pick<Model, "name">): string {
  return m.name
    .replace(/\s*\([^)]*\)/g, "")
    .replace(/\s+Instruct\b/i, "")
    .trim();
}

// ---- memory -------------------------------------------------------------------

/** The server's run overhead on top of the weights (engineFit's runOverheadGB). */
export const RUN_OVERHEAD_GB = 2;

/** Memory (GB) the model needs in the machine it runs in. 0 = unknown. */
export function needGB(m: Model): number {
  const w = m.size_gb || 0;
  return Math.max(m.min_ram_gb || 0, w ? w + RUN_OVERHEAD_GB : 0);
}

/** Whole GB, rounded up, for "needs 12 GB". */
export const needLabel = (m: Model): number => Math.ceil(needGB(m) - 1e-9);

/**
 * An engine machine's memory as its settings show it: an 18 GB Docker Desktop
 * VM reads 18.8e9 bytes, so round down (the top bar does the same).
 */
export const memLabel = (n: number): string => `${Math.max(1, Math.floor(n))} GB`;

/** The Podman command that gives its machine `gbNeeded` (at least 12 GB). */
export function podmanMemoryCmd(gbNeeded = 12): string {
  const mib = Math.max(12, Math.ceil(gbNeeded)) * 1024;
  return `podman machine stop && podman machine set --memory ${mib} && podman machine start`;
}

/** How to give an engine's machine more memory: a command (Podman) or a settings path (Docker). */
export function memoryFixFor(engine: string, gbNeeded = 12): { cmd?: string; text: string } {
  if (engine === "podman") {
    return {
      cmd: podmanMemoryCmd(gbNeeded),
      text: `Give Podman ${Math.max(12, Math.ceil(gbNeeded))} GB, then restart the factory so it reconnects.`,
    };
  }
  return { text: `Raise Docker's memory: ${DOCKER_MEMORY_PATH}.` };
}

// ---- engines and routes -----------------------------------------------------------

export const engineOf = (si: SysInfo | null, e: string): EngineResources | undefined =>
  si?.engines?.find((x) => x.engine === e);

/**
 * Whether a is a better engine than b for GPU work — the server's rule
 * (engines.go betterGPUEngine): one that's running, then more memory (a GPU in
 * a VM shares the VM's memory), then the GPU the start script saw on the host
 * (the libkrun machine) over one the GPU check found.
 */
function betterGPU(a: EngineResources, b: EngineResources): boolean {
  if (a.reachable !== b.reachable) return a.reachable;
  if ((a.mem_gb || 0) !== (b.mem_gb || 0)) return (a.mem_gb || 0) > (b.mem_gb || 0);
  return a.gpu_from === "host" && b.gpu_from !== "host";
}

/** Engines that give containers a GPU (compute "" = any), best first. */
export function gpuEngines(si: SysInfo | null, compute = ""): EngineResources[] {
  return (si?.engines ?? [])
    .filter((e) => !!e.gpu && (!compute || e.gpu === compute))
    .sort((a, b) => (betterGPU(a, b) ? -1 : betterGPU(b, a) ? 1 : 0));
}

/**
 * The engine GPU work for `compute` goes to by default: when Docker and Podman
 * both give containers the GPU (Docker VMM beside a libkrun machine), the one
 * with more usable memory. A build already set to an engine that has the GPU
 * keeps it.
 */
export function gpuEngineFor(si: SysInfo | null, compute = ""): EngineResources | undefined {
  return gpuEngines(si, compute)[0];
}

/**
 * The engine CPU images go to: Docker when it answers (on a Mac it has far more
 * memory than the Podman machine), else Podman, else the machine's default.
 */
export function cpuEngineId(si: SysInfo | null): string {
  if (!si?.engines?.length) return "docker";
  if (engineOf(si, "docker")?.reachable) return "docker";
  if (engineOf(si, "podman")?.reachable) return "podman";
  return si.default_engine || "docker";
}

/** The compute m will actually build for when `wanted` is asked for. */
export function effectiveCompute(m: Model, wanted: string): string {
  const have = m.computes;
  if (!have?.length || have.includes(wanted)) return wanted;
  return have.includes("cpu") ? "cpu" : have[0];
}

export const isGPU = (c: string | undefined): boolean => c === "vulkan" || c === "cuda";

export interface RoutePrediction {
  compute: string;     // compute it will build for
  buildEngine: string; // where it compiles
  transfer: boolean;   // compiles on the other engine, then moves
  blocked: string;     // why it can't build now ("" = it can)
}

/**
 * Instant client-side guess at where a build compiles, from /api/sysinfo (the
 * same rule as the server's preflight: compile where the image runs when that
 * machine has the memory, else on the other engine, else not at all). The
 * Build plan page asks the server (POST /api/build/preflight) for the real answer.
 */
export function predictRoute(m: Model, it: { engine: string; compute: string }, si: SysInfo | null): RoutePrediction {
  const compute = effectiveCompute(m, it.compute);
  const out: RoutePrediction = { compute, buildEngine: it.engine, transfer: false, blocked: "" };
  const t = engineOf(si, it.engine);
  if (t && !t.reachable) {
    out.blocked = `${engineName(it.engine)} isn't running`;
    return out;
  }
  if (!m.compiles || !t || !t.mem_gb) return out;
  const need = compileNeed(si, compute);
  if (t.mem_gb >= need) return out;
  const other = engineOf(si, it.engine === "podman" ? "docker" : "podman");
  if (other?.reachable && other.mem_gb >= need) {
    out.buildEngine = other.engine;
    out.transfer = true;
    return out;
  }
  out.blocked = `Not enough memory to compile: needs ${need} GB, ${engineName(it.engine)} has ${t.mem_gb.toFixed(0)} GB`;
  return out;
}

/** Least engine memory to compile a runtime for `compute` (sysinfo, else the server's floors). */
export function compileNeed(si: SysInfo | null, compute: string): number {
  return si?.compile_min_gb?.[compute] ?? (compute === "cpu" ? 3 : 6);
}

// ---- what the library has of a model ------------------------------------------------

export interface LibImage { ref: string; engine: string; compute: string }
export interface LibRun { port: string; engine: string; compute: string; ref: string }
export interface LibEntry {
  images: LibImage[];
  running: LibRun[];
  onDisk: boolean;           // weights downloaded
  active: BuildJob | null;   // queued or running now
  failed: BuildJob | null;   // its latest build failed (and none ran since)
}

const EMPTY: LibEntry = { images: [], running: [], onDisk: false, active: null, failed: null };

export function libraryIndex(
  images: ImageInfo[],
  containers: ContainerInfo[],
  files: { id: string; downloaded: boolean }[],
  jobs: BuildJob[],
): Map<string, LibEntry> {
  const out = new Map<string, LibEntry>();
  const entry = (id: string) => {
    let e = out.get(id);
    if (!e) {
      e = { images: [], running: [], onDisk: false, active: null, failed: null };
      out.set(id, e);
    }
    return e;
  };
  for (const im of images) {
    const id = labelVal(im.Labels, "local-llm.model");
    if (!id) continue;
    entry(id).images.push({
      ref: imageRef(im).ref,
      engine: im.Engine || "docker",
      compute: im.Compute || labelVal(im.Labels, "local-llm.compute") || "cpu",
    });
  }
  for (const c of containers) {
    if (!isRunning(c)) continue;
    const id = labelVal(c.Labels, "local-llm.model");
    if (!id) continue;
    entry(id).running.push({
      port: containerPort(c),
      engine: c.Engine || "docker",
      compute: labelVal(c.Labels, "local-llm.compute") || "cpu",
      ref: labelVal(c.Labels, "local-llm.ref") || c.Image || "",
    });
  }
  for (const f of files) if (f.downloaded) entry(f.id).onDisk = true;
  // Jobs arrive in submission order; the last finished one says how its latest build went.
  const lastDone = new Map<string, BuildJob>();
  for (const j of jobs) {
    if (j.kind !== "model" || !j.model_id) continue;
    if (j.active) {
      const e = entry(j.model_id);
      if (!e.active || j.status === "running") e.active = j;
    } else if (!lastDone.has(j.model_id) || (j.finished_at || 0) >= (lastDone.get(j.model_id)!.finished_at || 0)) {
      lastDone.set(j.model_id, j);
    }
  }
  for (const [id, j] of lastDone) if (j.status === "error") entry(id).failed = j;
  return out;
}

export const libFor = (idx: Map<string, LibEntry>, id: string): LibEntry => idx.get(id) ?? EMPTY;

// ---- where a model can run ------------------------------------------------------------

export type PathState =
  | "running" | "built"           // the library already has it
  | "fits" | "tight"              // builds and runs now
  | "later"                       // the engine needs more memory, or has to start
  | "no"                          // can't: too little memory anywhere / no engine
  | "na"                          // no build for this path (CPU-only model, no GPU here)
  | "unknown";                    // machine not read yet

export interface PathFit {
  state: PathState;
  tone: "ok" | "wa" | "er" | "gpu" | "mu";
  icon: string;
  text: string;    // the card line
  detail: string;  // a sentence or two for the details drawer
  engine: string;  // engine the image would be for ("" = none)
  compute: string; // compute it would build for ("" = none)
  fix?: { cmd?: string; text: string };
  transfer?: string; // engine it compiles on, when not its own
}

export interface Fit {
  cpu: PathFit;
  gpu: PathFit;
  /** Some path builds and runs today (or is already in the library). */
  runsNow: boolean;
}

const RUNNABLE: PathState[] = ["running", "built", "fits", "tight"];
export const runnable = (p: PathFit): boolean => RUNNABLE.includes(p.state);

/** Headroom under which a fit counts as tight (hwPerf's "fair" line). */
const TIGHT_GB = 3;

function unknownPath(compute: string): PathFit {
  return { state: "unknown", tone: "mu", icon: "clock", text: "Checking this machine…", detail: "", engine: "", compute };
}

/** The CPU path: CPU image on Docker (else Podman). Ignores the library. */
export function cpuPath(m: Model, si: SysInfo | null, onDisk = false): PathFit {
  const name = m.name;
  if (m.computes?.length && !m.computes.includes("cpu")) {
    const only = m.computes.map(computeName).join(" or ");
    return {
      state: "na", tone: "mu", icon: "info", text: `No CPU build: ${only} only`,
      detail: `${name} only builds for ${only}.`, engine: "", compute: "",
    };
  }
  if (!si) return unknownPath("cpu");
  const id = cpuEngineId(si);
  const e = engineOf(si, id);
  if (si.engines?.length && !e?.reachable) {
    return {
      state: "no", tone: "er", icon: "xmark", text: "No engine is running",
      detail: "Neither Docker nor Podman is answering. Start one, then restart the factory so it reconnects.",
      engine: id, compute: "cpu", fix: { cmd: "podman machine start", text: "Start Docker Desktop, or the Podman machine:" },
    };
  }
  const eng = engineName(id);
  const pr = predictRoute(m, { engine: id, compute: "cpu" }, si);
  if (pr.blocked) {
    const need = compileNeed(si, "cpu");
    return {
      state: "no", tone: "er", icon: "xmark", text: "Too little memory to compile",
      detail: `Building ${name} compiles its runtime, which needs ${need} GB on the engine that compiles it. ${pr.blocked}.`,
      engine: id, compute: "cpu", fix: memoryFixFor(id, need),
    };
  }
  const have = (e ? e.mem_gb : si.mem_gb) || 0;
  const need = needGB(m);
  const via = pr.transfer ? ` It compiles on ${engineName(pr.buildEngine)}, then moves to ${eng}.` : "";
  const dl = onDisk ? "weights on disk" : m.file ? `${gb(m.size_gb)} download` : "";
  if (!have || !need) {
    return { state: "fits", tone: "ok", icon: "check", text: `CPU on ${eng}${dl ? " · " + dl : ""}`, detail: `Runs on the CPU in ${eng}.${via}`, engine: id, compute: "cpu", transfer: pr.transfer ? pr.buildEngine : undefined };
  }
  if (need > have) {
    return {
      state: "no", tone: "er", icon: "xmark",
      text: `Needs ${needLabel(m)} GB; ${eng} has ${memLabel(have)}`,
      detail: `${name} needs about ${needLabel(m)} GB to run, but ${eng} has ${memLabel(have)}. It would start, then page its weights from disk and run very slowly.`,
      engine: id, compute: "cpu", fix: memoryFixFor(id, need),
    };
  }
  if (have - need < TIGHT_GB) {
    return {
      state: "tight", tone: "wa", icon: "warn",
      text: `Tight on ${eng}: ${needLabel(m)} of ${memLabel(have)}`,
      detail: `${name} needs about ${needLabel(m)} GB and ${eng} has ${memLabel(have)}, so a long context may not fit.${via}`,
      engine: id, compute: "cpu", transfer: pr.transfer ? pr.buildEngine : undefined,
    };
  }
  return {
    state: "fits", tone: "ok", icon: "check", text: `CPU on ${eng}${dl ? " · " + dl : ""}`,
    detail: `Runs on the CPU in ${eng}, which has ${memLabel(have)}; it needs about ${needLabel(m)} GB.${via}`,
    engine: id, compute: "cpu", transfer: pr.transfer ? pr.buildEngine : undefined,
  };
}

/** The GPU path: the machine's GPU engine, if the model has a build for its GPU. Ignores the library. */
export function gpuPath(m: Model, si: SysInfo | null): PathFit {
  const name = m.name;
  const gpus = (m.computes?.length ? m.computes : ["cpu", "cuda", "vulkan"]).filter(isGPU);
  if (!gpus.length) {
    return { state: "na", tone: "mu", icon: "info", text: "CPU only", detail: `${name} only builds for the CPU.`, engine: "", compute: "" };
  }
  if (!si) return unknownPath(gpus[0]);
  // An older factory reports one machine summary instead of per-engine data.
  const engines: EngineResources[] = si.engines ?? [
    { engine: si.engine || "docker", reachable: true, mem_gb: si.mem_gb, cpus: si.cpus, gpu: si.gpu, fix: "" },
  ];
  const withGPU = gpuEngines({ ...si, engines });
  if (!withGPU.length) {
    return {
      state: "na", tone: "mu", icon: "info", text: "No GPU engine on this machine",
      detail: "Neither engine gives containers a GPU here, so models run on the CPU.", engine: "", compute: "",
    };
  }
  // Best first: with two GPU engines, the one with more memory.
  const g = withGPU.find((e) => gpus.includes(e.gpu));
  if (!g) {
    const have = computeName(withGPU[0].gpu);
    return {
      state: "na", tone: "mu", icon: "info", text: `No ${have} build`,
      detail: `${name} builds for ${gpus.map(computeName).join(" or ")}, not for this machine's ${have} GPU.`, engine: "", compute: "",
    };
  }
  const eng = engineName(g.engine);
  const C = computeName(g.gpu);
  if (!g.reachable) {
    return {
      state: "later", tone: "wa", icon: "warn", text: `GPU once ${eng} is running`,
      detail: `${eng} gives containers the GPU (${C}) but isn't running.`,
      engine: g.engine, compute: g.gpu,
      fix: g.engine === "podman"
        ? { cmd: "podman machine start", text: "Start the Podman machine, then restart the factory so it reconnects:" }
        : { text: "Start Docker Desktop, then restart the factory so it reconnects." },
    };
  }
  const pr = predictRoute(m, { engine: g.engine, compute: g.gpu }, si);
  if (pr.blocked) {
    const need = compileNeed(si, g.gpu);
    return {
      state: "no", tone: "er", icon: "xmark", text: `GPU needs ${need} GB to compile`,
      detail: `A ${C} build compiles its runtime, which needs ${need} GB on the engine that compiles it. ${pr.blocked}.`,
      engine: g.engine, compute: g.gpu, fix: memoryFixFor(g.engine, 12),
    };
  }
  // Vulkan in a VM shares the machine's memory; CUDA keeps the weights in VRAM.
  const cuda = g.gpu === "cuda";
  const have = cuda ? si.vram_gb || 0 : g.mem_gb;
  const need = cuda ? m.min_vram_gb || m.size_gb || 0 : needGB(m);
  const needTxt = Math.ceil(need - 1e-9);
  // A GPU the GPU check found (Docker VMM, a krunkit VM) is experimental, and
  // with two GPU engines the other one stays a choice.
  const other = withGPU.find((e) => e.engine !== g.engine && e.gpu === g.gpu);
  const about =
    (g.gpu_experimental ? ` ${eng}'s GPU was found by the GPU check and is experimental with the factory's images.` : "") +
    (other ? ` ${engineName(other.engine)} has the GPU too (${other.reachable ? memLabel(other.mem_gb) : "not running"}); set the build to it to use that one.` : "");
  const via = (pr.transfer ? ` It compiles on ${engineName(pr.buildEngine)} (${eng} is too small to compile), then loads into ${eng}.` : "") + about;
  const transfer = pr.transfer ? pr.buildEngine : undefined;
  if (!have || !need) {
    return { state: "fits", tone: "gpu", icon: "bolt", text: `GPU on ${eng}`, detail: `Runs on the GPU (${C}) in ${eng}.${via}`, engine: g.engine, compute: g.gpu, transfer };
  }
  const where = cuda ? "the GPU's memory" : eng;
  if (need > have) {
    return {
      state: "later", tone: "wa", icon: "warn", text: cuda ? `GPU needs ${needTxt} GB of VRAM` : `GPU once ${eng} has ${needTxt} GB`,
      detail: `${eng} has the GPU (${C}) but ${cuda ? "the card has" : "only"} ${memLabel(have)}${cuda ? " of VRAM" : ""}; ${name} needs about ${needTxt} GB there.${about}`,
      engine: g.engine, compute: g.gpu, fix: cuda ? undefined : memoryFixFor(g.engine, need), transfer,
    };
  }
  if (have - need < TIGHT_GB) {
    return {
      state: "tight", tone: "wa", icon: "warn", text: `GPU on ${eng}, tight: ${needTxt} of ${memLabel(have)}`,
      detail: `${name} needs about ${needTxt} GB and ${where} has ${memLabel(have)}, so a long context may not fit.${via}`,
      engine: g.engine, compute: g.gpu, transfer,
    };
  }
  return {
    state: "fits", tone: "gpu", icon: "bolt", text: `GPU on ${eng} fits today`,
    detail: `Runs on the GPU (${C}) in ${eng}, which has ${memLabel(have)}; it needs about ${needTxt} GB.${via}`,
    engine: g.engine, compute: g.gpu, transfer,
  };
}

/** Both paths, with what the library already has laid over them (frame f8's two card lines). */
export function fitFor(m: Model, si: SysInfo | null, lib: LibEntry = EMPTY): Fit {
  let cpu = cpuPath(m, si, lib.onDisk);
  let gpu = gpuPath(m, si);
  const overlay = (p: PathFit, gpuSide: boolean): PathFit => {
    const run = lib.running.find((r) => isGPU(r.compute) === gpuSide);
    if (run) {
      return {
        ...p, state: "running", tone: gpuSide ? "gpu" : "ok", icon: gpuSide ? "bolt" : "check",
        text: `In your library · :${run.port || "?"}`,
        detail: `Running on ${engineName(run.engine)} (${computeName(run.compute)})${run.port ? ` at port ${run.port}` : ""}.`,
        engine: run.engine, compute: run.compute, fix: undefined,
      };
    }
    const im = lib.images.find((i) => isGPU(i.compute) === gpuSide);
    if (im) {
      return {
        ...p, state: "built", tone: gpuSide ? "gpu" : "ok", icon: gpuSide ? "bolt" : "check",
        text: `Built for ${engineName(im.engine)} · in your library`,
        detail: `${im.ref} is built for ${engineName(im.engine)} (${computeName(im.compute)}).`,
        engine: im.engine, compute: im.compute, fix: undefined,
      };
    }
    return p;
  };
  cpu = overlay(cpu, false);
  gpu = overlay(gpu, true);
  return { cpu, gpu, runsNow: runnable(cpu) || runnable(gpu) };
}

/**
 * Where to build m by default: the GPU when this machine has one the model can
 * use and it fits there now, else the CPU (Docker first), else — for a GPU-only
 * model — its GPU anyway, so the plan can say what it needs.
 */
export function defaultPath(m: Model, si: SysInfo | null): { engine: string; compute: string } {
  const cpuEngine = cpuEngineId(si);
  const gpuWanted = si?.default_compute && si.default_compute !== "cpu" ? si.default_compute : "";
  const canCPU = !m.computes?.length || m.computes.includes("cpu");
  if (gpuWanted) {
    // The machine's GPU compute on the GPU engine with the most memory (two
    // engines can have it: a libkrun Podman machine and Docker VMM).
    const compute = effectiveCompute(m, gpuWanted);
    if (isGPU(compute)) {
      const g = gpuEngineFor(si, compute);
      const engine = g?.engine ?? (compute === "cuda" ? "docker" : si?.default_engine || "docker");
      const p = gpuPath(m, si);
      if (!canCPU || p.state === "fits" || p.state === "tight" || p.state === "unknown") return { engine, compute };
    }
  }
  const compute = effectiveCompute(m, "cpu");
  if (isGPU(compute)) {
    // GPU-only (image generation): its GPU's engine when there is one.
    const g = gpuEngineFor(si, compute);
    return { engine: g?.engine ?? cpuEngine, compute };
  }
  return { engine: cpuEngine, compute };
}

// ---- quality and speed ----------------------------------------------------------------

/** Mean capability rating (0–100) from meta.ts, 0 when unrated. */
export function rating(m: Model): number {
  const r = META[m.id]?.ratings;
  if (!r) return 0;
  const dims = dimsFor(m.modality).filter((d) => d in r);
  const vals = (dims.length ? dims : Object.keys(r)).map((d) => r[d] || 0);
  return vals.length ? vals.reduce((a, b) => a + b, 0) / vals.length : 0;
}

const PERF_RANK = ["impossible", "poor", "warning", "fair", "good", "excellent"];

/** hwPerf levels on the engines each path would use (expected speed on this machine). */
export function perfHere(m: Model, si: SysInfo | null): { cpu: string; gpu: string } {
  const cpu = hwPerf(m, engineSys(si, cpuEngineId(si))).cpu;
  const g = gpuEngineFor(si);
  const gpu = hwPerf(m, engineSys(si, g?.engine ?? si?.engine ?? "docker")).gpu;
  return { cpu, gpu };
}

/** The better of the two speed levels, as a rank (higher = faster), for sorting. */
export function speedRank(m: Model, si: SysInfo | null): number {
  const p = perfHere(m, si);
  return Math.max(PERF_RANK.indexOf(p.cpu), PERF_RANK.indexOf(p.gpu));
}

/** Chip class for a speed level. */
export function perfTone(level: string): string {
  if (level === "excellent" || level === "good") return "ok";
  if (level === "fair" || level === "warning" || level === "poor") return "wa";
  if (level === "impossible") return "er";
  return "";
}

// ---- sorting ------------------------------------------------------------------------

export const SORTS: { id: string; label: string }[] = [
  { id: "recommended", label: "Recommended" },
  { id: "rating", label: "Best rated" },
  { id: "speed", label: "Fastest here" },
  { id: "small", label: "Smallest" },
  { id: "large", label: "Largest" },
  { id: "name", label: "Name" },
];

export function sortModels(list: Model[], by: string, si: SysInfo | null): Model[] {
  const order = new Map(list.map((m, i) => [m.id, i]));
  const byIdx = (a: Model, b: Model) => order.get(a.id)! - order.get(b.id)!;
  const out = [...list];
  switch (by) {
    case "rating":
      return out.sort((a, b) => rating(b) - rating(a) || byIdx(a, b));
    case "speed": {
      const s = new Map(list.map((m) => [m.id, speedRank(m, si)]));
      return out.sort((a, b) => s.get(b.id)! - s.get(a.id)! || (a.size_gb || 0) - (b.size_gb || 0));
    }
    case "small":
      return out.sort((a, b) => (a.size_gb || 0) - (b.size_gb || 0) || byIdx(a, b));
    case "large":
      return out.sort((a, b) => (b.size_gb || 0) - (a.size_gb || 0) || byIdx(a, b));
    case "name":
      return out.sort((a, b) => a.name.localeCompare(b.name));
    default:
      return out.sort((a, b) => Number(!!b.recommended) - Number(!!a.recommended) || byIdx(a, b));
  }
}

// ---- quick picks ------------------------------------------------------------------------

export interface QuickPick {
  id: string;
  label: string;
  models: Model[];
  custom?: boolean;
}

interface Want { kind: string; fast?: boolean; small?: boolean }

/**
 * Working sets for one click (frame f8), chosen from the catalog: for each part
 * the model that runs on this machine now, recommended first, then the best
 * rated (lighter ones where speed matters). A set is offered only when every
 * part has a model that runs now.
 */
export function quickPicks(pool: Model[], fits: (m: Model) => boolean): QuickPick[] {
  const live = pool.filter((m) => !m.legacy && fits(m));
  const best = (w: Want, skip: Set<string>): Model | null => {
    let top: Model | null = null;
    let topScore = -Infinity;
    for (const m of live) {
      if (kindOf(m) !== w.kind || skip.has(m.id)) continue;
      const tags = META[m.id]?.strengths ?? [];
      let s = (m.recommended ? 100 : 0) + rating(m);
      if (w.fast) s += (tags.includes("fast") || tags.includes("lightweight") ? 30 : 0) - (m.size_gb || 0) * 4;
      if (w.small) s -= (m.size_gb || 0) * 10;
      if (s > topScore) {
        top = m;
        topScore = s;
      }
    }
    return top;
  };
  const sets: { id: string; label: string; parts: Want[] }[] = [
    { id: "voice", label: "Voice assistant", parts: [{ kind: "audio-stt" }, { kind: "text", fast: true }, { kind: "tts" }] },
    { id: "coding", label: "Coding", parts: [{ kind: "code" }, { kind: "embedding", small: true }] },
    { id: "see", label: "See and chat", parts: [{ kind: "vision", fast: true }, { kind: "text" }] },
    { id: "images", label: "Make images", parts: [{ kind: "image" }, { kind: "vision", fast: true }] },
  ];
  const out: QuickPick[] = [];
  for (const s of sets) {
    const used = new Set<string>();
    const models: Model[] = [];
    for (const p of s.parts) {
      const m = best(p, used);
      if (!m) break;
      used.add(m.id);
      models.push(m);
    }
    if (models.length === s.parts.length) out.push({ id: s.id, label: s.label, models });
  }
  return out;
}

// ---- the plan in the engines' lanes ----------------------------------------------------------

/** Runtime family for compile reuse: a lane's second build of a family reuses the first's compile. */
export function family(m: Model | undefined): string {
  if (!m) return "";
  const k = kindOf(m);
  const f = k === "audio-stt" ? "stt" : k === "image" || k === "video" ? "sd" : k === "tts" ? "tts" : "llm";
  return `${m.runtime ?? "cpp"}|${f}`;
}

export interface LaneBlock {
  key: string;
  label: string;
  note: string;        // "compiles on Docker", "already queued" …
  start: number;       // seconds from now
  dur: number;         // seconds (nominal when unknown)
  known: boolean;      // dur is an estimate from a real build
  kind: "queued" | "cpu" | "gpu";
}

export interface Lane {
  engine: string;
  blocks: LaneBlock[];
  end: number;
  ahead: number;       // builds already queued or running in this lane
  mine: number;        // plan builds in this lane
}

export interface Schedule {
  lanes: Lane[];
  total: number;       // seconds until the last lane finishes
  known: boolean;      // every duration is a real estimate
}

export interface ScheduleItem {
  key: string;
  label: string;
  engine: string;      // the lane: the engine the image is for
  compute: string;
  eta: number;         // seconds, 0 = unknown
  transfer: string;    // compile engine when it isn't `engine`, else ""
}

/**
 * Lay the plan out in the server's queue: one lane per engine the images are
 * for, each running its builds one at a time after the builds already in it.
 * Durations are the server's last build time for each compute; unknown ones
 * get a nominal width (and no total is claimed).
 */
export function schedule(items: ScheduleItem[], jobs: BuildJob[]): Schedule {
  const known: number[] = [];
  const queued: Record<string, { j: BuildJob; rem: number }[]> = {};
  const active = jobs
    .filter((j) => j.active)
    .sort((a, b) => (a.status === b.status ? a.position - b.position || a.created_at - b.created_at : a.status === "running" ? -1 : 1));
  for (const j of active) {
    let rem = 0;
    if (j.eta_seconds > 0) rem = j.status === "running" ? Math.max(30, j.eta_seconds - j.elapsed_ms / 1000) : j.eta_seconds;
    if (rem) known.push(rem);
    (queued[j.engine] ??= []).push({ j, rem });
  }
  for (const it of items) if (it.eta > 0) known.push(it.eta);
  const sorted = [...known].sort((a, b) => a - b);
  const nominal = sorted.length ? sorted[Math.floor(sorted.length / 2)] : 300;
  let allKnown = true;

  const engines = [...new Set([...items.map((i) => i.engine), ...Object.keys(queued)])].sort((a, b) =>
    a === b ? 0 : a === "docker" ? -1 : b === "docker" ? 1 : a.localeCompare(b),
  );
  const lanes: Lane[] = [];
  for (const e of engines) {
    const lane: Lane = { engine: e, blocks: [], end: 0, ahead: 0, mine: 0 };
    let t = 0;
    for (const q of queued[e] ?? []) {
      const dur = q.rem || nominal;
      if (!q.rem) allKnown = false;
      lane.blocks.push({
        key: "job:" + q.j.id, label: q.j.name, note: q.j.status === "running" ? "building now" : "already queued",
        start: t, dur, known: !!q.rem, kind: "queued",
      });
      t += dur;
      lane.ahead++;
    }
    for (const it of items) {
      if (it.engine !== e) continue;
      const dur = it.eta || nominal;
      if (!it.eta) allKnown = false;
      lane.blocks.push({
        key: it.key, label: it.label, note: it.transfer ? `compiles on ${engineName(it.transfer)}` : "",
        start: t, dur, known: !!it.eta, kind: isGPU(it.compute) ? "gpu" : "cpu",
      });
      t += dur;
      lane.mine++;
    }
    lane.end = t;
    if (lane.blocks.length) lanes.push(lane);
  }
  return { lanes, total: Math.max(0, ...lanes.map((l) => l.end)), known: allKnown };
}

// ---- cloning an image's settings ------------------------------------------------------------------

/** GET /api/image/config: the build settings baked into an image's labels. */
export interface ImageConfig {
  repository: string;
  tag: string;
  model_id: string;
  engine: string;
  compute: string;
  inject_mode: string;
  system_prompt: string;
  ctx_size: string;   // label text: "0" = the model's max
  memory_gb: string;  // label text: "0" = unlimited
  route: string;
  autostart: boolean;
  harness?: HarnessConfig | null; // the baked agent harness (null = none)
  tuning?: Tuning | null;          // baked runtime tuning (label local-llm.tuning)
}

export const CTX_TIERS = ["2048", "4096", "8192", "16384", "32768"];
