// Overview helpers (pages/Overview.svelte, components/overview/*): rows for the
// running models, which failed builds still need the user, the problems a
// phone shows, and the sentences the cards say. Pure functions over the shared
// data (live, build, sysinfo) — nothing here invents a number: what the API
// doesn't say is left out.
import { api, labelVal } from "./api";
import { containerPort, isRunning } from "./util";
import { ago, engineName, gb, plural, upFor } from "./format";
import { explainFailure, PODMAN_MEMORY_CMD, type Explanation, type Fix } from "./explain";
import { terminalState, type HarnessStatus, type Tone } from "./harness";
import { href } from "./router";
import { gpuEngines } from "./catalog";
import {
  compileFloor,
  engineList,
  engineOf,
  fallbackFor,
  gpuEngine,
  isGPU,
  isMac,
  PODMAN_FIX_GB,
  PODMAN_START_CMD,
  podmanFixNoRoom,
  vmGB,
} from "./system.svelte";
import type { BuildJob, BuildSnapshot, ContainerInfo, EngineResources, Model, ResourceBudget, StorageInfo, SysInfo } from "./types";

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// ---- running models ---------------------------------------------------------------

export interface RunRow {
  key: string;
  id: string; // what /api/stop takes
  engine: string;
  name: string; // catalog name, else the image ref
  modelId: string;
  modality: string;
  ref: string; // image ref (Library deep link)
  port: string;
  compute: string;
  up: string; // "up 2 h"
  memGB: number | null; // the factory's estimate (/api/resources), null = unknown
}

export function runRows(cs: ContainerInfo[], cat: Model[], res: ResourceBudget | null): RunRow[] {
  const out: RunRow[] = [];
  const running = res?.running ?? [];
  for (const c of cs) {
    if (!isRunning(c)) continue;
    const cname = (c.Names || "").replace(/^\//, "");
    const modelId = labelVal(c.Labels, "local-llm.model");
    const m = cat.find((x) => x.id === modelId);
    const ref = labelVal(c.Labels, "local-llm.ref") || c.Image || "";
    const compute = labelVal(c.Labels, "local-llm.compute") || "cpu";
    // /api/resources names each running model by its container; fall back to
    // the model id when that's unambiguous.
    const byModel = running.filter((r) => r.model_id && r.model_id === modelId);
    const rm = running.find((r) => r.name === cname) ?? (byModel.length === 1 ? byModel[0] : undefined);
    const mem = rm ? (isGPU(compute) ? rm.vram_gb || rm.ram_gb : rm.ram_gb) : 0;
    out.push({
      key: c.ID || cname,
      id: c.ID || cname,
      engine: c.Engine || "docker",
      name: m?.name || shortRef(ref) || cname || "container",
      modelId,
      modality: labelVal(c.Labels, "local-llm.modality") || m?.modality || "text",
      ref,
      port: containerPort(c),
      compute,
      up: upFor(c.Status),
      memGB: mem > 0 ? mem : null,
    });
  }
  return out.sort((a, b) => (+a.port || 1e9) - (+b.port || 1e9));
}

const KIND: Record<string, string> = {
  text: "chat",
  code: "code",
  reasoning: "reasoning",
  vision: "vision",
  embedding: "embeddings",
  image: "image generation",
  video: "video generation",
  "audio-stt": "speech to text",
  tts: "text to speech",
};

/** What a model does, in lower case for running text: "speech to text". */
export function kindText(modality: string | undefined): string {
  return KIND[modality || "text"] ?? "chat";
}

/** "local-llm/gemma-4-12b:latest" -> "local-llm/gemma-4-12b". */
export function shortRef(ref: string): string {
  return ref.replace(/^localhost\//, "").replace(/:latest$/, "");
}

// ---- the build queue -------------------------------------------------------------

export function ordinal(n: number): string {
  const t = n % 100;
  if (t >= 11 && t <= 13) return `${n}th`;
  return n + (["th", "st", "nd", "rd"][n % 10] ?? "th");
}

/** A queued job's place in its lane: "Next", then "3rd" (counting the build running ahead). */
export function placeText(j: BuildJob, lane: BuildJob[]): string {
  const first = lane.find((x) => x.status === "queued");
  return first?.id === j.id ? "Next" : ordinal(j.position + 1);
}

/** Seconds until every active build should be done (lanes run side by side), 0 = unknown. */
export function allDoneIn(jobs: BuildJob[]): number {
  let most = 0;
  for (const j of jobs) {
    if (!j.active) continue;
    if (j.eta_seconds <= 0) return 0;
    const left = j.status === "running" ? j.eta_seconds - j.elapsed_ms / 1000 : j.wait_seconds + j.eta_seconds;
    if (j.status === "queued" && j.position > 0 && j.wait_seconds <= 0) return 0;
    most = Math.max(most, left);
  }
  return Math.max(0, most);
}

// ---- failures that still need the user ----------------------------------------------

export interface FailureItem {
  job: BuildJob;
  retry: BuildJob | null; // the same image building again right now
}

/**
 * The newest failed build of each image, unless a later build of it finished
 * (done or canceled) — newest first. One being built again comes with that job.
 */
export function failuresToShow(jobs: BuildJob[]): FailureItem[] {
  const out: FailureItem[] = [];
  const seen = new Set<string>();
  const finished = jobs.filter((j) => !j.active).sort((a, b) => (b.finished_at || 0) - (a.finished_at || 0));
  for (const j of finished) {
    if (seen.has(j.ref)) continue;
    seen.add(j.ref);
    if (j.status !== "error") continue;
    out.push({ job: j, retry: jobs.find((x) => x.active && x.ref === j.ref) ?? null });
  }
  return out;
}

/** "Last night", "This morning", "2 h ago", "Nov 12" — when a build finished, to open a sentence. */
export function whenText(ms: number, now = Date.now()): string {
  if (!ms) return "";
  if (now - ms < 6 * 3600e3) return cap(ago(ms, now));
  const n = new Date(now);
  const today = new Date(n.getFullYear(), n.getMonth(), n.getDate()).getTime();
  const h = new Date(ms).getHours();
  if (ms >= today) return h < 5 ? "Last night" : h < 12 ? "This morning" : h < 18 ? "This afternoon" : "This evening";
  if (ms >= today - 86400e3) return h >= 18 ? "Last night" : "Yesterday";
  return cap(ago(ms, now));
}

// Finished builds' logs don't change: fetch each once.
const logCache = new Map<string, string>();

/** A finished build's whole log (for explainFailure); "" when it can't be read. */
export async function failureLog(id: string): Promise<string> {
  const hit = logCache.get(id);
  if (hit !== undefined) return hit;
  try {
    const st = await api<BuildSnapshot>(`/api/builds/state?id=${encodeURIComponent(id)}&offset=0`);
    const log = (st.lines ?? []).join("\n");
    logCache.set(id, log);
    return log;
  } catch {
    return "";
  }
}

/** The engine a memory fix is for ("" when the fix isn't about memory). */
function memoryFixEngine(f: Fix): string {
  if (f.cmd === PODMAN_MEMORY_CMD) return "podman";
  if (/^Raise Docker's memory/.test(f.label)) return "docker";
  return "";
}

/** A fix to lead with; `to` is a link inside the app when the fix is a page to open. */
export type LeadFix = Fix & { to?: string };

/**
 * The fix to lead with for a failure, given the engines as they are now: a
 * memory fix the engine no longer needs (it has the memory now, or the other
 * engine can compile it) becomes "build it again", and Podman's 12 GB command
 * becomes the memory plan when this Mac has no room for it beside Docker.
 */
export function firstFix(x: Explanation, j: BuildJob, si: SysInfo | null): LeadFix | undefined {
  const f = x.fixes[0];
  if (!f) return undefined;
  const where = memoryFixEngine(f);
  if (!where) return f;
  const e = engineOf(si, where);
  const compute = j.compute || "cpu";
  if (e?.reachable && e.mem_gb >= compileFloor(si, compute)) {
    return { label: `${engineName(where)} has ${vmGB(e.mem_gb)} GB now, enough to compile it. Build it again`, action: "retry" };
  }
  const fb = e ? fallbackFor(si, e, compute) : undefined;
  if (fb) {
    return { label: `Build it again: it compiles on ${engineName(fb.engine)} now and moves to ${engineName(where)}`, action: "retry" };
  }
  if (where === "podman" && podmanFixNoRoom(si)) {
    return { label: `Giving Podman ${PODMAN_FIX_GB} GB means shrinking Docker first`, to: href("system", "memory") };
  }
  return f;
}

// ---- the machine ----------------------------------------------------------------------

/** How this machine routes builds, in a sentence (from the engines' GPU and defaults). */
export function routingSentence(si: SysInfo | null): string {
  if (!si) return "";
  const g = gpuEngine(si);
  const docker = engineOf(si, "docker");
  if (g?.gpu === "vulkan") {
    const cpuWhere = docker?.reachable || !g.reachable ? "Docker" : engineName(g.engine);
    const G = engineName(g.engine);
    const passes = `passes ${isMac(si) ? "the Mac's GPU" : "the GPU"} into containers as Vulkan${g.gpu_experimental ? " (experimental, found by the GPU check)" : ""}.`;
    let s = cpuWhere === G ? `Models build and run in ${G}, which ${passes}` : `CPU models build and run in ${cpuWhere}. GPU models run in ${G}, which ${passes}`;
    // Docker VMM beside a libkrun machine: GPU work goes where there's more memory.
    const other = gpuEngines(si, "vulkan").find((e) => e.engine !== g.engine);
    if (other) s += ` ${engineName(other.engine)} has the GPU too; builds set to it stay there.`;
    return s;
  }
  if (g?.gpu === "cuda") return `Models build and run in ${engineName(g.engine)}, which gives containers the NVIDIA GPU through CUDA.`;
  const up = engineList(si).find((e) => e.reachable);
  if (!up) return "Neither engine is answering, so nothing can build or run right now.";
  return `Models build and run on the CPU in ${engineName(up.engine)}. No engine here gives containers a GPU.`;
}

/** The biggest model being built for e that won't fit in its memory to run on its GPU. */
export function tooBigForEngine(e: EngineResources, jobs: BuildJob[], cat: Model[]): Model | undefined {
  let big: Model | undefined;
  for (const j of jobs) {
    if (!j.active || j.engine !== e.engine || !isGPU(j.compute)) continue;
    const m = cat.find((x) => x.id === j.model_id);
    if (m && m.size_gb > e.mem_gb && (!big || m.size_gb > big.size_gb)) big = m;
  }
  return big;
}

/** Why a low-memory GPU engine needs the user, and what the factory does meanwhile. */
export function lowMemoryText(si: SysInfo | null, e: EngineResources, jobs: BuildJob[], cat: Model[]): string {
  const compute = e.gpu || "vulkan";
  const big = tooBigForEngine(e, jobs, cat);
  const fb = fallbackFor(si, e, compute);
  let s = `Too little memory to compile for GPU (it needs ${compileFloor(si, compute)} GB)`;
  s += big ? ` or to run ${big.name}.` : ".";
  s += fb
    ? ` GPU builds compile on ${engineName(fb.engine)} and move here when done.`
    : " Neither engine can compile GPU images now.";
  return s;
}

/** Whether an engine's fix (EngineResources.fix) is a command to copy, not a settings path. */
export function fixIsCommand(fix: string): boolean {
  return /^(podman|docker|wsl)\s/.test(fix);
}

// ---- harness ---------------------------------------------------------------------------

export interface HarnessRow {
  key: string;
  label: string;
  tone: Tone;
  value: string;
  mono?: boolean;
}

export function harnessRows(h: HarnessStatus): HarnessRow[] {
  const gw = h.gateway;
  const t = terminalState(h.terminal);
  const skills = h.skills?.items ?? [];
  return [
    {
      key: "gateway",
      label: "Model gateway",
      tone: gw?.error ? "err" : "ok",
      value: gw?.error ? "not answering" : plural(gw?.models?.length ?? 0, "model"),
    },
    {
      key: "terminal",
      label: "Workspace files and terminal",
      tone: t.tone,
      // A glance: its port when it's up, else in a word (Harness has the detail).
      value:
        t.tone === "ok" ? `:${h.terminal.port}` : t.tone === "idle" ? (h.terminal.container?.exists ? "Stopped" : "Not set up") : t.label,
      mono: t.tone === "ok",
    },
    {
      key: "skills",
      label: "Skills",
      tone: h.skills?.error ? "err" : skills.length ? "ok" : "idle",
      value: h.skills?.error ? "can't read" : String(skills.length),
    },
  ];
}

/** The harness chip: "On" when everything is up, else what's off or wrong. */
export function harnessChip(h: HarnessStatus, rows: HarnessRow[]): { text: string; cls: string } {
  if (h.config_error) return { text: "Config error", cls: "chip er" };
  if (rows.some((r) => r.tone === "err")) return { text: "Needs you", cls: "chip er" };
  if (rows.some((r) => r.tone === "warn")) return { text: "Needs you", cls: "chip wa" };
  if (rows.some((r) => r.tone === "busy")) return { text: "Starting", cls: "chip pr" };
  if (rows.every((r) => r.tone === "ok")) return { text: "On", cls: "chip ok" };
  return { text: rows[0].tone === "ok" ? "Gateway on" : "Off", cls: "chip" };
}

// ---- disk -------------------------------------------------------------------------------

export interface DiskPart {
  key: string;
  label: string;
  gb: number;
  color: string;
}

/** What the factory keeps on disk, biggest kinds first in a fixed order. */
export function diskParts(s: StorageInfo): DiskPart[] {
  const parts: DiskPart[] = [
    { key: "weights", label: `Weights · ${plural(s.weights?.count ?? 0, "file")}`, gb: s.weights?.gb ?? 0, color: "var(--pr)" },
    { key: "tars", label: `Saved images · ${plural(s.tars?.count ?? 0, "tar")}`, gb: s.tars?.gb ?? 0, color: "var(--ac)" },
  ];
  if (s.leftovers?.count) {
    parts.push({ key: "leftovers", label: `Leftover build folders · ${s.leftovers.count}`, gb: s.leftovers.gb, color: "var(--fa)" });
  }
  if (s.media?.count) {
    parts.push({ key: "media", label: `Generated media · ${plural(s.media.count, "file")}`, gb: s.media.gb, color: "var(--ln2)" });
  }
  return parts;
}

/** Little enough free space that the next build may not fit. */
export function diskShort(s: StorageInfo | null): boolean {
  const free = s?.disk_free_gb;
  if (free == null) return false;
  return free < 20 || (!!s?.disk_total_gb && free / s.disk_total_gb < 0.05);
}

// ---- what needs the user (the phone's "one thing") ---------------------------------------

export interface Problem {
  key: string;
  tone: "er" | "wa";
  title: string;
  text: string;
  cmd?: string; // a command to copy and run on the host
  retryId?: string; // a failed build to build again
  href?: string;
  hrefLabel?: string;
}

/** Everything that needs the user, most urgent first. */
export function problems(
  si: SysInfo | null,
  jobs: BuildJob[],
  cat: Model[],
  failures: FailureItem[],
  logs: Record<string, string>,
  storage: StorageInfo | null,
): Problem[] {
  const out: Problem[] = [];
  for (const e of engineList(si)) {
    if (e.reachable) continue;
    const waiting = jobs.filter((j) => j.active && j.engine === e.engine).length;
    out.push({
      key: "down:" + e.engine,
      tone: "er",
      title: `${engineName(e.engine)} isn't running`,
      text:
        (waiting ? `${plural(waiting, "build")} wait for it. ` : "") +
        (e.engine === "podman"
          ? "Start the Podman machine, then restart the factory so it reconnects."
          : "Start Docker Desktop, then restart the factory."),
      cmd: e.engine === "podman" ? PODMAN_START_CMD : undefined,
      href: href("system"),
      hrefLabel: "Engines",
    });
  }
  for (const f of failures) {
    if (f.retry) continue;
    const x = explainFailure(f.job, logs[f.job.id] ?? "");
    if (!x) continue;
    const fix = firstFix(x, f.job, si);
    out.push({
      key: "fail:" + f.job.id,
      tone: "er",
      title: `${f.job.name} didn't build`,
      text: x.title + (fix ? ` ${fix.label}.` : ""),
      cmd: fix?.cmd,
      retryId: fix?.action === "retry" ? f.job.id : undefined,
      href: fix?.to ?? href("builds", f.job.id),
      hrefLabel: fix?.to ? "Memory plan" : "See why",
    });
  }
  for (const e of engineList(si)) {
    if (!e.reachable || !e.low_memory) continue;
    const fb = fallbackFor(si, e, e.gpu || "vulkan");
    const podman = e.engine === "podman";
    const noRoom = podman && podmanFixNoRoom(si);
    out.push({
      key: "low:" + e.engine,
      tone: "wa",
      title: `${engineName(e.engine)} has ${vmGB(e.mem_gb)} GB`,
      text:
        "Too little to compile for GPU. " +
        (fb
          ? `Builds go to ${engineName(fb.engine)} until you raise it${podman ? ` to ${PODMAN_FIX_GB} GB` : ""}.`
          : `Give it ${podman ? PODMAN_FIX_GB : compileFloor(si, e.gpu)} GB to build GPU images.`) +
        (noRoom ? " That means shrinking Docker first: the memory plan has the steps." : ""),
      cmd: noRoom ? undefined : fixIsCommand(e.fix) ? e.fix : podman ? PODMAN_MEMORY_CMD : undefined,
      href: href("system", "memory"),
      hrefLabel: "Memory plan",
    });
  }
  if (diskShort(storage)) {
    out.push({
      key: "disk",
      tone: "wa",
      title: `Only ${gb(storage?.disk_free_gb)} free on disk`,
      text: "The next build may not fit. Remove weights and images you don't use.",
      href: href("library"),
      hrefLabel: "Open the Library",
    });
  }
  return out;
}
