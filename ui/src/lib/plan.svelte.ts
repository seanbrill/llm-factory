// The build plan: the models picked in the Catalog (or from ⌘K), each with its
// own build settings, reviewed on the Build plan page and queued together.
// Persisted in localStorage so a refresh keeps the selection.
import { post } from "./api";
import { toast } from "./stores";
import { watchQueue } from "./build.svelte";
import { ctxDefaultFor } from "./util";
import { engineName } from "./format";
import type { Model, SysInfo } from "./types";

export interface PlanItem {
  model_id: string;
  engine: string;        // engine the image is for: "docker" | "podman"
  compute: string;       // requested compute: "cpu" | "cuda" | "vulkan"
  image_name: string;
  tag: string;
  system_prompt: string;
  inject_mode: string;   // "missing" | "always"
  ctx_tier: string;      // "auto" | "2048" … "32768" | "max"
  memory_gb: number;     // 0 = unlimited
  route: string;         // local URL alias ("" = none)
  autostart: boolean;
}

const KEY = "llmfactory.plan.v1";

function load(): PlanItem[] {
  try {
    const raw = localStorage.getItem(KEY);
    const v = raw ? JSON.parse(raw) : [];
    return Array.isArray(v) ? v.filter((x) => x && typeof x.model_id === "string") : [];
  } catch {
    return [];
  }
}

export const plan = $state({
  items: load(),
  submitting: false,
  error: "",
});

function save() {
  try {
    localStorage.setItem(KEY, JSON.stringify(plan.items));
  } catch {
    /* private mode / quota: the plan just won't survive a refresh */
  }
}

/** The compute m will actually build for when `wanted` is asked for. */
export function effectiveCompute(m: Model, wanted: string): string {
  const have = m.computes;
  if (!have?.length || have.includes(wanted)) return wanted;
  return have.includes("cpu") ? "cpu" : have[0];
}

/**
 * Sensible build settings for m on this machine: the machine's GPU compute when
 * the model has a build for it (Vulkan on a Mac with a libkrun Podman machine,
 * CUDA on an NVIDIA host), else CPU. CPU images go to Docker when it's up — on
 * a Mac it has far more memory than the Podman machine — GPU images to the
 * engine that has the GPU.
 */
export function defaultsFor(m: Model, si: SysInfo | null): PlanItem {
  const gpuCompute = si?.default_compute && si.default_compute !== "cpu" ? si.default_compute : "";
  const compute = gpuCompute ? effectiveCompute(m, gpuCompute) : effectiveCompute(m, "cpu");
  let engine = si?.default_engine || "docker";
  if (compute === "cpu" || compute === "cuda") {
    const docker = si?.engines?.find((e) => e.engine === "docker");
    if (!si?.engines || docker?.reachable) engine = "docker";
  } else {
    const gpuEngine = si?.engines?.find((e) => e.gpu === compute);
    if (gpuEngine) engine = gpuEngine.engine;
  }
  return {
    model_id: m.id,
    engine,
    compute,
    image_name: "local-llm/" + m.id,
    tag: "latest",
    system_prompt: "",
    inject_mode: "missing",
    ctx_tier: "auto",
    memory_gb: 0,
    route: "",
    autostart: false,
  };
}

export function inPlan(id: string): boolean {
  return plan.items.some((i) => i.model_id === id);
}

export function addToPlan(m: Model, si: SysInfo | null, patch: Partial<PlanItem> = {}) {
  if (inPlan(m.id)) return;
  plan.items.push({ ...defaultsFor(m, si), ...patch });
  save();
}

export function removeFromPlan(id: string) {
  plan.items = plan.items.filter((i) => i.model_id !== id);
  save();
}

export function togglePlan(m: Model, si: SysInfo | null) {
  if (inPlan(m.id)) removeFromPlan(m.id);
  else addToPlan(m, si);
}

export function updatePlanItem(id: string, patch: Partial<PlanItem>) {
  const it = plan.items.find((i) => i.model_id === id);
  if (!it) return;
  Object.assign(it, patch);
  save();
}

export function clearPlan() {
  plan.items = [];
  save();
}

/** Replace the plan with these models (Catalog quick picks). */
export function setPlan(models: Model[], si: SysInfo | null) {
  plan.items = models.map((m) => defaultsFor(m, si));
  save();
}

function ctxSize(it: PlanItem, m: Model | undefined): number {
  if (it.ctx_tier === "max") return 0; // the server resolves the model's max
  if (it.ctx_tier === "auto") return ctxDefaultFor(m ? effectiveCompute(m, it.compute) : it.compute);
  return parseInt(it.ctx_tier, 10) || 0;
}

/** The POST /api/build body for a plan item. */
export function buildBody(it: PlanItem, m: Model | undefined) {
  return {
    model_id: it.model_id,
    image_name: it.image_name.trim() || "local-llm/" + it.model_id,
    tag: it.tag.trim() || "latest",
    engine: it.engine,
    compute: m ? effectiveCompute(m, it.compute) : it.compute,
    system_prompt: it.system_prompt,
    inject_mode: it.inject_mode,
    ctx_size: ctxSize(it, m),
    memory_gb: it.memory_gb || 0,
    route: it.route,
    autostart: it.autostart,
  };
}

export interface QueueResult {
  model_id: string;
  ok: boolean;
  id?: string;        // build job id
  position?: number;  // builds ahead on its engine
  engine?: string;
  error?: string;
}

/**
 * Queue builds in order (one request each, so each lands in its engine's lane
 * in plan order). Items that queued are removed from the plan; failures stay,
 * with the reason in the result.
 */
export async function queueItems(items: PlanItem[], catalog: Model[]): Promise<QueueResult[]> {
  plan.submitting = true;
  plan.error = "";
  const results: QueueResult[] = [];
  try {
    for (const it of items) {
      const m = catalog.find((x) => x.id === it.model_id);
      try {
        const r = await post<{ id: string; position: number; engine: string }>("/api/build", buildBody(it, m));
        results.push({ model_id: it.model_id, ok: true, id: r.id, position: r.position, engine: r.engine });
      } catch (e) {
        results.push({ model_id: it.model_id, ok: false, error: (e as Error).message });
      }
    }
  } finally {
    plan.submitting = false;
  }
  const queued = new Set(results.filter((r) => r.ok).map((r) => r.model_id));
  plan.items = plan.items.filter((i) => !queued.has(i.model_id));
  save();
  const failed = results.filter((r) => !r.ok);
  if (failed.length) plan.error = failed.map((f) => `${f.model_id}: ${f.error}`).join("\n");
  watchQueue();
  return results;
}

/** Build one model now with the settings the factory would pick (⌘K "Build …"). */
export async function quickBuild(m: Model, si: SysInfo | null): Promise<QueueResult> {
  const it = plan.items.find((i) => i.model_id === m.id) ?? defaultsFor(m, si);
  try {
    const r = await post<{ id: string; position: number; engine: string }>("/api/build", buildBody(it, m));
    toast(
      r.position > 0
        ? `${m.name} queued: ${r.position} ahead on ${engineName(r.engine)}.`
        : `Building ${m.name} on ${engineName(r.engine)}.`,
    );
    watchQueue();
    return { model_id: m.id, ok: true, id: r.id, position: r.position, engine: r.engine };
  } catch (e) {
    toast(`Couldn't queue ${m.name}: ${(e as Error).message}`, 7000);
    return { model_id: m.id, ok: false, error: (e as Error).message };
  }
}

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
export function predictRoute(m: Model, it: Pick<PlanItem, "engine" | "compute">, si: SysInfo | null): RoutePrediction {
  const compute = effectiveCompute(m, it.compute);
  const out: RoutePrediction = { compute, buildEngine: it.engine, transfer: false, blocked: "" };
  const eng = (e: string) => si?.engines?.find((x) => x.engine === e);
  const t = eng(it.engine);
  if (t && !t.reachable) {
    out.blocked = `${engineName(it.engine)} isn't running`;
    return out;
  }
  if (!m.compiles || !t || !t.mem_gb) return out;
  const need = si?.compile_min_gb?.[compute] ?? (compute === "cpu" ? 3 : 6);
  if (t.mem_gb >= need) return out;
  const other = eng(it.engine === "podman" ? "docker" : "podman");
  if (other?.reachable && other.mem_gb >= need) {
    out.buildEngine = other.engine;
    out.transfer = true;
    return out;
  }
  out.blocked = `Not enough memory to compile: needs ${need} GB, ${engineName(it.engine)} has ${t.mem_gb.toFixed(0)} GB`;
  return out;
}
