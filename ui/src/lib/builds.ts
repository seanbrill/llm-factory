// Builds page helpers (sheet frames f10, f17): what each engine lane, queued or
// finished build, log and failure says. Everything is derived from the queue
// (/api/builds), the engines (/api/sysinfo), the catalog and a build's streamed
// log — nothing here invents a number.
import type { BuildJob, EngineResources, Model, SysInfo } from "./types";
import { views, type JobView } from "./build.svelte";
import { ENGINES, compiledOn, history, phase, timeText } from "./jobs";
import { CMAKE_CHECK, focusLine, memoryFix, type Explanation, type Fix } from "./explain";
import { predictRoute } from "./plan.svelte";
import { ago, computeName, dur, engineName, plural } from "./format";

// ---- small formatting -------------------------------------------------------------

/** Engine memory as its settings show it (an 18 GB VM reads 18.8e9 bytes): "18 GB". */
export function memGB(n: number | undefined | null): string {
  return n && n > 0 ? `${Math.max(1, Math.floor(n))} GB` : "—";
}

/** A run time: "45 s", "1 min 32 s", "29 min", "1 h 4 min". */
export function took(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s} s`;
  if (s < 600) {
    const m = Math.floor(s / 60), r = s % 60;
    return r ? `${m} min ${r} s` : `${m} min`;
  }
  const mins = Math.round(s / 60);
  if (mins < 60) return `${mins} min`;
  const h = Math.floor(mins / 60), m = mins % 60;
  return m ? `${h} h ${m} min` : `${h} h`;
}

/** 1 -> "1st", 3 -> "3rd", 12 -> "12th". */
export function ordinal(n: number): string {
  const t = n % 100;
  if (t >= 11 && t <= 13) return `${n}th`;
  return `${n}${["th", "st", "nd", "rd"][n % 10] ?? "th"}`;
}

/** A fix that's a shell command, as opposed to a place in an app's settings. */
export function isCommand(s: string | undefined): boolean {
  return !!s && /^(podman|docker)\s/.test(s.trim());
}

/** Upper-case the first letter and end with a full stop (not after a "→" path). */
function sentence(s: string): string {
  s = s.trim();
  if (!s) return s;
  s = s[0].toUpperCase() + s.slice(1);
  return /[.!?…]$/.test(s) || s.includes("→") ? s : s + ".";
}

// ---- models -------------------------------------------------------------------------

export function modelOf(j: BuildJob, catalog: Model[]): Model | undefined {
  const id = j.model_id || j.config?.model_id;
  return id ? catalog.find((m) => m.id === id) : undefined;
}

const MOD_TEXT: Record<string, string> = {
  text: "chat", code: "code", reasoning: "reasoning", vision: "vision", embedding: "embeddings",
  image: "image generation", video: "video generation", "audio-stt": "speech to text", tts: "text to speech",
};

/** What the model does, lower case for a meta line: "speech to text". */
export function modalityText(m: Model | undefined): string {
  return m ? (MOD_TEXT[m.modality ?? "text"] ?? "") : "";
}

/** The runtime family a model builds from (internal/builder modelFamily): one Dockerfile per family + compute. */
function family(m: Model): string {
  if (m.runtime === "python" || m.runtime === "audiocpp") return m.runtime;
  switch (m.modality) {
    case "image": return "sd";
    case "video": return "video";
    case "audio-stt": return "whisper";
    case "tts": return "tts";
    default: return "llama";
  }
}

// ---- routes ---------------------------------------------------------------------------

export interface JobRoute {
  build: string;     // engine that compiles it
  target: string;    // engine the image is for (its lane)
  transfer: boolean; // compiled on one, then loaded into the other
}

/**
 * Where a build compiles and runs. A running or finished build says so in its
 * log; a queued one gets the factory's rule (compile where it runs when that
 * machine has the memory, else on the other engine).
 */
export function jobRoute(j: BuildJob, m: Model | undefined, si: SysInfo | null): JobRoute {
  if (j.status !== "queued" || !m) {
    const b = j.status === "queued" ? j.engine : compiledOn(j);
    return { build: b, target: j.engine, transfer: b !== j.engine };
  }
  const p = predictRoute(m, { engine: j.engine, compute: j.compute }, si);
  return { build: p.buildEngine, target: j.engine, transfer: p.transfer };
}

/**
 * A queued build reuses the compile of a build ahead of it in its lane when both
 * compile the same runtime for the same compute: the engine's cache keeps the
 * compiled layer and the second image only adds its weights.
 */
export function reusesCompile(j: BuildJob, ahead: BuildJob[], catalog: Model[]): boolean {
  const m = modelOf(j, catalog);
  if (!m?.compiles) return false;
  return ahead.some((o) => {
    const mo = modelOf(o, catalog);
    return !!mo?.compiles && o.compute === j.compute && family(mo) === family(m);
  });
}

// ---- lanes ------------------------------------------------------------------------------

export interface LaneNote {
  tone: "er" | "wa";
  title: string;
  text: string;
  cmd?: string; // a command to copy
}

export interface Lane {
  engine: string;
  name: string;
  res: EngineResources | null; // null: not known yet
  jobs: BuildJob[];            // queued and running, in run order
  running: BuildJob | null;
  queued: BuildJob[];
  dot: "" | "wa" | "off";      // .dot modifier ("" = ready)
  chip: { t: string; c: string } | null;
  facts: string;               // "3 jobs · 18 GB" / "12 GB · 7 CPUs"
  note: LaneNote | null;       // the engine is down or too small, with its fix
}

/** One lane per engine: those installed here, plus any that has builds. */
export function engineLanes(byLane: Record<string, BuildJob[]>, si: SysInfo | null): Lane[] {
  const known = si?.engines ?? null;
  const ids: string[] = [...ENGINES];
  for (const e of Object.keys(byLane)) if (!ids.includes(e)) ids.push(e);
  const out: Lane[] = [];
  for (const engine of ids) {
    const jobs = byLane[engine] ?? [];
    const res = known?.find((e) => e.engine === engine) ?? null;
    if (known && !res && !jobs.length) continue; // not installed on this machine
    const name = engineName(engine);
    const running = jobs.find((j) => j.status === "running") ?? null;
    const queued = jobs.filter((j) => j.status === "queued");
    const down = !!res && !res.reachable;
    const low = !!res?.reachable && !!res.low_memory;

    let chip: Lane["chip"] = null;
    if (down) chip = { t: "Stopped", c: "chip" };
    else if (running) chip = { t: "Building", c: "chip pr" };
    else if (queued.length) chip = { t: "Waiting", c: "chip wa" };

    const facts: string[] = [];
    if (jobs.length) facts.push(plural(jobs.length, "job"));
    if (res?.reachable) {
      facts.push(memGB(res.mem_gb));
      if (!jobs.length && res.cpus) facts.push(`${res.cpus} CPUs`);
    }

    let note: LaneNote | null = null;
    if (down) {
      note = {
        tone: "er",
        title: jobs.length
          ? `${plural(jobs.length, "build")} ${jobs.length === 1 ? "waits" : "wait"} here until ${name} is running.`
          : `${name} isn't running.`,
        text: engine === "podman"
          ? "Start the Podman machine, then restart the factory so it reconnects:"
          : "Start Docker Desktop, then restart the factory so it reconnects.",
        cmd: engine === "podman" ? "podman machine start" : undefined,
      };
    } else if (low && res) {
      const need = si?.compile_min_gb?.[res.gpu] ?? 6;
      const other = known?.find((e) => e.engine !== engine && e.reachable && e.mem_gb >= need);
      note = {
        tone: "wa",
        title: `${memGB(res.mem_gb)} is too little to compile ${computeName(res.gpu)} images here (they need ${need} GB).`,
        text: other
          ? `They compile on ${engineName(other.engine)} and move here when they're done. To compile here, give ${name} more memory${isCommand(res.fix) ? ":" : `: ${res.fix}`}`
          : `Give ${name} more memory to build them${isCommand(res.fix) ? ":" : `: ${res.fix}`}`,
        cmd: isCommand(res.fix) ? res.fix : undefined,
      };
    }

    out.push({
      engine, name, res, jobs, running, queued,
      dot: !res || down ? "off" : low ? "wa" : "",
      chip, facts: facts.join(" · "), note,
    });
  }
  return out;
}

// ---- queued and running builds -------------------------------------------------------------

/** "Next" for the first queued build of a lane, else its place in line ("3rd"). */
export function placeText(j: BuildJob, queuedIndex: number): string {
  return queuedIndex === 0 ? "Next" : ordinal(j.position + 1);
}

/** "~9 min" until a queued build starts ("" when unknown). */
export function waitText(j: BuildJob): string {
  return j.status === "queued" && j.wait_seconds > 0 ? `~${dur(j.wait_seconds)}` : "";
}

/** Time left for a running build; says so when it runs past its estimate. */
export function leftText(j: BuildJob): string {
  if (j.status !== "running" || !(j.eta_seconds > 0)) return "";
  return timeText(j); // "about 3 min left" / "almost done" / "past the ~X estimate"
}

/** The llama.cpp release a build compiles, from its log's git clone line ("v0.9.0"). */
export function llamaRelease(log: string): string {
  const m =
    log.match(/--branch\s+"?([\w.\-]+)"?\s+https:\/\/github\.com\/ggml-org\/llama\.cpp/) ??
    log.match(/\bARG LLAMA_CPP_REF=([\w.\-]+)/);
  return m ? m[1] : "";
}

/** What a running build is doing now: "Compiling llama.cpp v0.9.0", "Downloading …". */
export function phaseText(j: BuildJob): string {
  const p = phase(j);
  if (p !== "Compiling llama.cpp") return p;
  const rel = llamaRelease(views[j.id]?.log ?? "");
  return rel ? `${p} ${rel}` : p;
}

// ---- finished builds --------------------------------------------------------------------

/** "failed after 29 min", "1 min 32 s" (built), "canceled before it started". */
export function outcomeText(j: BuildJob): string {
  const ran = j.started_at ? took(j.elapsed_ms) : "";
  switch (j.status) {
    case "done": return ran || "built";
    case "error": return ran ? `failed after ${ran}` : "failed before it started";
    case "canceled": return ran ? `canceled after ${ran}` : "canceled before it started";
    default: return "";
  }
}

/** When it happened, for a build's header: "11 h ago · ran 29 min", "started 6 min ago". */
export function whenText(j: BuildJob): string {
  switch (j.status) {
    case "running": return j.started_at ? `started ${ago(j.started_at)}` : "starting";
    case "queued": return `queued ${ago(j.created_at)}`;
    default: {
      const t = ago(j.finished_at);
      return j.started_at ? `${t} · ran ${took(j.elapsed_ms)}` : t;
    }
  }
}

export const STATUS: Record<string, { t: string; c: string }> = {
  queued: { t: "Queued", c: "chip" },
  running: { t: "Building", c: "chip pr" },
  done: { t: "Built", c: "chip ok" },
  error: { t: "Failed", c: "chip er" },
  canceled: { t: "Canceled", c: "chip wa" },
};

/** Later builds of the same image for the same engine (a retry or a fresh build), oldest first. */
export function laterBuilds(j: BuildJob, jobs: BuildJob[]): BuildJob[] {
  return jobs
    .filter((o) => o.id !== j.id && o.ref === j.ref && o.engine === j.engine && o.created_at > j.created_at)
    .sort((a, b) => a.created_at - b.created_at);
}

/** The newest failure no later build has fixed: what the page opens on. */
export function needsYou(jobs: BuildJob[]): BuildJob | null {
  for (const j of history(jobs)) {
    if (j.status !== "error") continue;
    if (laterBuilds(j, jobs).some((o) => o.status === "done")) continue;
    return j;
  }
  return null;
}

// ---- logs ---------------------------------------------------------------------------------

/** A build's streamed log as lines, with the absolute index of the first one. */
export function logLines(v: JobView | undefined): { lines: string[]; first: number } {
  if (!v?.log) return { lines: [], first: 0 };
  const lines = v.log.split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  return { lines, first: Math.max(0, v.offset - lines.length) };
}

// A line that reports an error (CMake's configure checks are excluded where it's used).
const ERROR = /(killed signal|fatal error|\berror\b|out of memory|virtual memory exhausted|no space left|cannot connect|connection refused|did not complete successfully|exit (status|code):? [1-9])/i;

/** A log line that reports an error (highlighted, and counted in "Errors"). */
export function isErrorLine(l: string): boolean {
  return ERROR.test(l) && !CMAKE_CHECK.test(l);
}

/**
 * The line a failed build's log opens on: the cause (a compiler kill, a fatal
 * error, a full disk), else focusLine's pick with CMake's checks left out.
 */
export function focusAt(lines: string[]): number {
  return focusLine(lines);
}

/** Save text as a file (the log's Download button). */
export function saveText(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** "local-llm_gemma-4-12b_latest-<id>.log" */
export function logFileName(j: BuildJob): string {
  return `${(j.ref || j.name || "build").replace(/[^\w.-]+/g, "_")}-${j.id}.log`;
}

// ---- steps ----------------------------------------------------------------------------------

export type StepState = "ok" | "run" | "er" | "stop" | "todo";
export interface Step {
  key: string;
  label: string;
  state: StepState;
  note: string;
}

/** "quay.io/fedora/fedora:44" -> "fedora:44"; stage names ("builder") -> "". */
function baseName(ref: string): string {
  ref = ref.replace(/@sha256:\w+$/, "");
  if (!ref.includes(":") && !ref.includes("/")) return "";
  return ref.split("/").pop() ?? "";
}

/**
 * Where a model build got to, read from its log: the weights, the base image,
 * the compile, the packaged image, and the load into its engine when it was
 * built on the other one. Steps it never needed are left out; a failed or
 * canceled build marks the step it stopped at.
 */
export function buildSteps(j: BuildJob, lines: string[], m: Model | undefined, downloadPct: number | null): Step[] {
  if (j.kind !== "model" || !lines.length) return [];
  // A load step only when another engine compiled it (its log names that engine).
  const transfer = compiledOn(j) !== j.engine || lines.some((l) => /^Transfer 1\/3/.test(l));
  type Acc = { key: string; label: string; reached: boolean; done: boolean; note: string };
  const mk = (key: string, label: string): Acc => ({ key, label, reached: false, done: false, note: "" });
  const weights = mk("weights", "Weights");
  const base = mk("base", "Base image");
  const compile = mk("compile", "Compile");
  const pkg = mk("package", "Package");
  const load = mk("load", `Load into ${engineName(j.engine)}`);
  let pct = -1;
  let downloading = false;

  for (const raw of lines) {
    const l = raw.trim();
    if (/^Downloading /.test(l)) { weights.reached = true; downloading = true; continue; }
    if (/^(Model already present|Weights on disk)/.test(l)) {
      weights.reached = weights.done = true;
      if (!weights.note) weights.note = "on disk";
      continue;
    }
    if (/^Saved .*\bmodels\//.test(l)) {
      weights.reached = weights.done = true;
      downloading = false;
      const gbm = l.match(/\(([\d.]+) GB\)/);
      weights.note = gbm ? `downloaded ${(+gbm[1]).toFixed(1)} GB` : "downloaded";
      continue;
    }
    if (/^Stag(ing|ed) /.test(l)) { weights.reached = weights.done = true; continue; }
    const from = l.match(/\bFROM\s+(?:--platform=\S+\s+)?(\S+)/);
    if (from && (/^STEP \d+\/\d+:/.test(l) || /^#\d+ \[/.test(l))) {
      base.reached = true;
      const b = baseName(from[1]);
      if (b && !base.note) base.note = b;
      continue;
    }
    if (/^Building .+ \[|^STEP \d+\/\d+:|^#\d+ \[/.test(l)) base.reached = true;
    if (/Compiling with \d+ parallel job|\bcmake\b/.test(l)) { base.done = true; compile.reached = true; }
    const p = l.match(/\[\s*(\d{1,3})%\]/);
    if (p) { base.done = true; compile.reached = true; pct = +p[1]; continue; }
    if (/^Built image|^Exporting|exporting to image|^COMMIT\b|^Successfully tagged/.test(l)) {
      base.done = compile.done = true;
      pkg.reached = true;
      continue;
    }
    const ex = l.match(/^Exported ([\d.]+) GB/);
    if (ex) { pkg.reached = pkg.done = true; pkg.note = `${(+ex[1]).toFixed(1)} GB`; continue; }
    if (/^Saved images\//.test(l)) { pkg.reached = pkg.done = true; pkg.note = "exported"; continue; }
    if (/^Transfer 1\/3/.test(l)) { pkg.done = true; load.reached = true; continue; }
    if (/^Loaded into /.test(l)) { load.reached = load.done = true; load.note = "loaded"; continue; }
  }
  if (downloading && downloadPct != null) weights.note = `downloading ${downloadPct}%`;
  else if (downloading && !weights.done) weights.note = "downloading";
  if (pct >= 0 && !compile.done) compile.note = `${pct}%`;

  const all = [weights, base];
  if (m?.compiles !== false || compile.reached) all.push(compile);
  all.push(pkg);
  if (transfer) all.push(load);

  let last = -1;
  all.forEach((s, i) => { if (s.reached) last = i; });
  if (last < 0) return [];
  const steps: Step[] = [];
  all.forEach((s, i) => {
    if (!s.reached) {
      if (i < last || j.status === "done") return; // didn't apply to this build
      steps.push({ key: s.key, label: s.label, state: "todo", note: j.active ? "" : "not reached" });
      return;
    }
    if (i < last || s.done || j.status === "done") {
      steps.push({ key: s.key, label: s.label, state: "ok", note: s.note || "done" });
      return;
    }
    if (j.status === "running") steps.push({ key: s.key, label: s.label, state: "run", note: s.note || "now" });
    else if (j.status === "error") {
      const at = s.key === "compile" && pct >= 0 ? `stopped at ${pct}%` : "stopped here";
      steps.push({ key: s.key, label: s.label, state: "er", note: at });
    } else steps.push({ key: s.key, label: s.label, state: "stop", note: "canceled here" });
  });
  return steps;
}

// ---- fixes ------------------------------------------------------------------------------------

export type FixKind = "retry" | "cmd" | "cleanup" | "system" | "library" | "cpu" | "note";

export interface FixCard {
  kind: FixKind;
  kicker: string; // "Recommended", "Fix Podman", "Try again" …
  title: string;
  desc?: string;
  cmd?: string;
  rec?: boolean;
}

export interface FixContext {
  si: SysInfo | null;
  model?: Model;
}

/** "Give Podman 12 GB, then restart the factory so it reconnects" -> title + description. */
function splitLabel(label: string): { title: string; desc?: string } {
  let i = label.indexOf(": ");
  if (i > 0) return { title: label.slice(0, i), desc: sentence(label.slice(i + 2)) };
  i = label.indexOf(", then ");
  if (i > 0) return { title: label.slice(0, i), desc: sentence("then " + label.slice(i + 7)) };
  return { title: label };
}

function kickerFor(f: Fix): string {
  if (f.action === "retry") return "Try again";
  if (f.action === "cleanup") return "Free space";
  if (f.action === "system" || f.action === "library") return "Check";
  const t = `${f.label} ${f.cmd ?? ""}`;
  if (/memory/i.test(t)) return /podman/i.test(t) ? "Fix Podman" : /docker/i.test(t) ? "Fix Docker" : "Add memory";
  if (/machine start|^Start /i.test(t)) return "Start the engine";
  if (/prune/.test(t)) return "Free space";
  return "Do this";
}

/**
 * The explanation's fixes as cards, made specific to this machine: a retry
 * that would now compile on the other engine says so (and is recommended when
 * the build ran out of memory), a memory fix the engine already has is
 * dropped, and a GPU build that ran out of memory also offers a CPU build.
 */
export function fixCards(j: BuildJob, ex: Explanation, ctx: FixContext): FixCard[] {
  const si = ctx.si;
  const eng = (e: string) => si?.engines?.find((x) => x.engine === e);
  const need = si?.compile_min_gb?.[j.compute] ?? (j.compute === "cpu" ? 3 : 6);

  // "Give Podman 12 GB" is done once the Podman machine has that much: drop it,
  // and say so on the retry.
  const podmanCmd = memoryFix("podman").cmd;
  const target = +(podmanCmd?.match(/--memory (\d+)/)?.[1] ?? 12288) / 1024;
  const podman = eng("podman");
  const podmanDone = !!podman?.reachable && podman.mem_gb >= target && podman.mem_gb >= need;
  const roomy =
    podmanDone && ex.fixes.some((f) => f.cmd === podmanCmd) && (j.engine === "podman" || compiledOn(j) === "podman")
      ? `Podman has ${memGB(podman!.mem_gb)} now, enough to compile it.`
      : "";

  // Compiling on the other engine answers a failure about memory, not others.
  const memory = /memory/i.test(ex.title) || ex.fixes.some((f) => (f.cmd && f.cmd === podmanCmd) || f.label === memoryFix("docker").label);

  const cards: FixCard[] = [];
  for (const f of ex.fixes) {
    const { title, desc } = splitLabel(f.label);
    if (podmanDone && f.cmd && f.cmd === podmanCmd) continue;

    if (f.action === "retry") {
      if (cards.some((c) => c.kind === "retry")) continue;
      const m = ctx.model;
      const r = m && j.kind === "model" ? predictRoute(m, { engine: j.engine, compute: j.compute }, si) : null;
      if (r && !r.blocked && r.transfer && compiledOn(j) !== r.buildEngine) {
        cards.push({
          kind: "retry", kicker: memory ? "Recommended" : "Try again", rec: memory,
          title: `Build on ${engineName(r.buildEngine)}, run on ${engineName(j.engine)}`,
          desc: `${engineName(r.buildEngine)} has ${memGB(eng(r.buildEngine)?.mem_gb)}. The image moves to ${engineName(j.engine)} when it's done.`,
        });
      } else if (roomy && !r?.blocked) {
        cards.push({ kind: "retry", kicker: "Recommended", rec: true, title: "Build it again", desc: roomy });
      } else {
        cards.push({
          kind: "retry", kicker: "Try again",
          title: /^Build (it )?again$/.test(title) ? "Build it again" : title,
          desc: r?.blocked ? sentence(r.blocked) : desc,
        });
      }
      continue;
    }
    if (f.action === "cleanup") {
      cards.push({ kind: "cleanup", kicker: "Free space", title: "Clean up leftover builds" });
      continue;
    }
    if (f.action === "system" || f.action === "library") {
      cards.push({ kind: f.action, kicker: kickerFor(f), title, desc });
      continue;
    }
    cards.push({ kind: f.cmd ? "cmd" : "note", kicker: kickerFor(f), title, desc, cmd: f.cmd });
  }

  const cpu = cpuCard(j, ex, ctx);
  if (cpu) cards.push(cpu);

  // Recommend one: the specific retry above, else the explanation's first fix.
  const rec = cards.findIndex((c) => c.rec);
  if (rec > 0) cards.unshift(...cards.splice(rec, 1));
  else if (rec < 0 && cards.length > 1) {
    cards[0] = { ...cards[0], rec: true, kicker: "Recommended" };
  }
  return cards;
}

/** A GPU build that failed for memory or GPU reasons can be built for CPU on Docker instead. */
function cpuCard(j: BuildJob, ex: Explanation, ctx: FixContext): FixCard | null {
  const m = ctx.model;
  if (j.kind !== "model" || !m || (j.compute !== "vulkan" && j.compute !== "cuda")) return null;
  if (m.computes?.length && !m.computes.includes("cpu")) return null;
  const memOrGpu = ex.fixes.some(
    (f) =>
      (f.cmd && f.cmd === memoryFix("podman").cmd) ||
      f.label === memoryFix("docker").label ||
      (f.action === "system" && /GPU/.test(f.label)),
  );
  if (!memOrGpu) return null;
  const docker = ctx.si?.engines?.find((e) => e.engine === "docker");
  if (!docker?.reachable) return null;
  if (predictRoute(m, { engine: "docker", compute: "cpu" }, ctx.si).blocked) return null;
  return { kind: "cpu", kicker: "Without the GPU", title: "Build for CPU instead", desc: "Runs on Docker now, at CPU speed." };
}
