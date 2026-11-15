// The build plan: the models picked in the Catalog (or from ⌘K), each with its
// own build settings, reviewed on the Build plan page and queued together.
// Persisted in localStorage so a refresh keeps the selection.
//
// Also here, because the Catalog and the Build plan share them: the server's
// preflight for the plan (POST /api/build/preflight), the Catalog's filters,
// saved quick picks, cloning an image's settings into the plan, and browser
// notifications when the plan's builds finish.
import { api, post } from "./api";
import { toast } from "./stores";
import { go } from "./router";
import { build, watchQueue } from "./build.svelte";
import { ctxDefaultFor } from "./util";
import { engineName } from "./format";
import { CTX_TIERS, defaultPath, effectiveCompute, predictRoute, type ImageConfig } from "./catalog";
import { defaultHarness, harnessBody, harnessFromConfig, normHarness, type PlanHarness } from "./baked";
import type { Model, PreflightItem, PreflightResult, SysInfo } from "./types";

export { effectiveCompute, predictRoute, type RoutePrediction } from "./catalog";

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
  harness: PlanHarness;  // tools baked into the image (lib/baked.ts); only chat models take them
}

const KEY = "llmfactory.plan.v1";

function read<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function write(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* private mode / quota: it just won't survive a refresh */
  }
}

function load(): PlanItem[] {
  const v = read<unknown>(KEY, []);
  // Plans saved before an item had a harness get one, turned off.
  return Array.isArray(v)
    ? v.filter((x) => x && typeof x.model_id === "string").map((x) => ({ ...x, harness: normHarness(x.harness) }))
    : [];
}

export const plan = $state({
  items: load(),
  submitting: false,
  error: "",
});

function save() {
  write(KEY, plan.items);
}

// Settings edited in place (bound inputs on the Build plan page) save too.
$effect.root(() => {
  $effect(() => {
    write(KEY, $state.snapshot(plan.items));
  });
});

/**
 * Sensible build settings for m on this machine: the machine's GPU compute when
 * the model has a build for it and fits on the engine with that GPU now (Vulkan
 * on a Mac with a libkrun Podman machine, CUDA on an NVIDIA host), else the CPU
 * on Docker (on a Mac it has far more memory than the Podman machine), else —
 * for a GPU-only model — its GPU anyway.
 */
export function defaultsFor(m: Model, si: SysInfo | null): PlanItem {
  const { engine, compute } = defaultPath(m, si);
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
    harness: defaultHarness(false),
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

/** Move an item to position `to` (clamped). Plan order is queue order. */
export function movePlanItem(id: string, to: number) {
  const from = plan.items.findIndex((i) => i.model_id === id);
  if (from < 0) return;
  const next = [...plan.items];
  const [it] = next.splice(from, 1);
  next.splice(Math.max(0, Math.min(next.length, to)), 0, it);
  plan.items = next;
  save();
}

export function clearPlan() {
  plan.items = [];
  save();
}

/** Replace the plan with these models. */
export function setPlan(models: Model[], si: SysInfo | null) {
  plan.items = models.map((m) => defaultsFor(m, si));
  save();
}

/** Turn "start with the engine" on or off for every item. */
export function setAutostartAll(on: boolean) {
  for (const it of plan.items) it.autostart = on;
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
    harness: harnessBody(it.harness, m),
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
  followJobs(results.filter((r) => r.ok && r.id).map((r) => r.id!));
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
    followJobs([r.id]);
    watchQueue();
    return { model_id: m.id, ok: true, id: r.id, position: r.position, engine: r.engine };
  } catch (e) {
    toast(`Couldn't queue ${m.name}: ${(e as Error).message}`, 7000);
    return { model_id: m.id, ok: false, error: (e as Error).message };
  }
}

// ---- preflight (POST /api/build/preflight) -------------------------------------------

export const PREFLIGHT_MAX = 50;

interface PreflightReq { model_id: string; engine: string; compute: string }

/** The server's answer for the plan, shared by the Catalog tray and the Build plan. */
export const pre = $state({
  result: null as PreflightResult | null,
  req: [] as PreflightReq[],   // what `result` answers, in order
  loading: false,
  error: "",
});

/** What the preflight depends on: each item's model, engine and compute, in order. */
export function planKey(items: PreflightReq[] = plan.items): string {
  return items.map((i) => `${i.model_id}|${i.engine}|${i.compute}`).join(",");
}

let pfTimer: ReturnType<typeof setTimeout> | undefined;
let pfSeq = 0;

/** Ask the server about the plan again after `delay` ms (debounced). */
export function checkPlan(delay = 300) {
  clearTimeout(pfTimer);
  pfTimer = setTimeout(runPreflight, delay);
}

async function runPreflight() {
  const req = plan.items.slice(0, PREFLIGHT_MAX).map((i) => ({ model_id: i.model_id, engine: i.engine, compute: i.compute }));
  const my = ++pfSeq;
  if (!req.length) {
    pre.result = null;
    pre.req = [];
    pre.error = "";
    pre.loading = false;
    return;
  }
  pre.loading = true;
  try {
    const r = await post<PreflightResult>("/api/build/preflight", { items: req });
    if (my !== pfSeq) return;
    pre.result = r;
    pre.req = req;
    pre.error = "";
  } catch (e) {
    if (my !== pfSeq) return;
    pre.error = (e as Error).message || "the factory didn't answer";
  } finally {
    if (my === pfSeq) pre.loading = false;
  }
}

/** The preflight answer for this item, if it's current (same engine and compute). */
export function preflightFor(it: PlanItem): PreflightItem | undefined {
  const i = pre.req.findIndex((r) => r.model_id === it.model_id);
  if (i < 0 || pre.req[i].engine !== it.engine || pre.req[i].compute !== it.compute) return undefined;
  return pre.result?.items?.[i];
}

/** The preflight answers the current plan exactly. */
export function preflightCurrent(): boolean {
  return !!pre.result && planKey(pre.req) === planKey(plan.items.slice(0, PREFLIGHT_MAX));
}

// ---- Catalog filters (kept across page visits) ----------------------------------------

export const browse = $state({
  q: "",
  kind: "",
  tags: [] as string[],
  maxSize: 0,
  legacy: false,
  fitsOnly: false,
  clones: false, // only models that clone a voice
  sort: "recommended",
  filtersOpen: false,
});

// ---- saved quick picks ---------------------------------------------------------------

export interface SavedPick { id: string; label: string; ids: string[] }

const PICKS_KEY = "llmfactory.picks.v1";

export const picks = $state({
  saved: read<SavedPick[]>(PICKS_KEY, []).filter((p) => p && typeof p.label === "string" && Array.isArray(p.ids)),
});

export function savePick(label: string, ids: string[]) {
  const name = label.trim();
  if (!name || !ids.length) return;
  const existing = picks.saved.find((p) => p.label.toLowerCase() === name.toLowerCase());
  if (existing) existing.ids = [...ids];
  else picks.saved.push({ id: "p" + Date.now().toString(36), label: name, ids: [...ids] });
  write(PICKS_KEY, picks.saved);
}

export function removePick(id: string) {
  picks.saved = picks.saved.filter((p) => p.id !== id);
  write(PICKS_KEY, picks.saved);
}

// ---- cloning an image (Library "use as template") ---------------------------------------

function ctxTierOf(label: string, compute: string): string {
  const n = parseInt(label, 10);
  if (label === "0") return "max";
  if (!n || n === ctxDefaultFor(compute)) return "auto";
  return CTX_TIERS.includes(String(n)) ? String(n) : "auto";
}

/**
 * Read the settings baked into an image (GET /api/image/config) and put them in
 * the plan as that model's item (added, or updated when it's already planned).
 * Returns the model id.
 */
export async function cloneImage(t: { ref: string; engine: string }, catalog: Model[], si: SysInfo | null): Promise<string> {
  const cfg = await api<ImageConfig>(
    `/api/image/config?ref=${encodeURIComponent(t.ref)}&engine=${encodeURIComponent(t.engine)}`,
  );
  const m = catalog.find((x) => x.id === cfg.model_id);
  if (!m) {
    throw new Error(
      cfg.model_id
        ? `${t.ref} was built from ${cfg.model_id}, which isn't in this factory's catalog.`
        : `${t.ref} has no factory build settings to copy.`,
    );
  }
  const compute = cfg.compute || defaultsFor(m, si).compute;
  const patch: Partial<PlanItem> = {
    engine: cfg.engine === "podman" ? "podman" : "docker",
    compute,
    image_name: (cfg.repository || "local-llm/" + m.id).replace(/^localhost\//, ""),
    tag: cfg.tag || "latest",
    system_prompt: cfg.system_prompt || "",
    inject_mode: cfg.inject_mode || "missing",
    ctx_tier: ctxTierOf(cfg.ctx_size || "", effectiveCompute(m, compute)),
    memory_gb: parseFloat(cfg.memory_gb) || 0,
    route: cfg.route || "",
    autostart: !!cfg.autostart,
    harness: harnessFromConfig(cfg.harness),
  };
  if (inPlan(m.id)) updatePlanItem(m.id, patch);
  else addToPlan(m, si, patch);
  return m.id;
}

// ---- browser notifications when the plan's builds finish ---------------------------------------

const NOTIFY_KEY = "llmfactory.notify.v1";
const FOLLOW_KEY = "llmfactory.follow.v1";

export const notifyCan = typeof window !== "undefined" && "Notification" in window && window.isSecureContext;

export const notify = $state({
  on: notifyCan && read<boolean>(NOTIFY_KEY, false) && Notification.permission === "granted",
  denied: notifyCan && Notification.permission === "denied",
});

/** Turn notifications on (asks the browser for permission) or off. */
export async function setNotify(on: boolean) {
  if (!notifyCan) return;
  if (on && Notification.permission !== "granted") {
    let p: NotificationPermission = "denied";
    try {
      p = await Notification.requestPermission();
    } catch {
      /* treated as denied */
    }
    notify.denied = p === "denied";
    if (p !== "granted") {
      notify.on = false;
      write(NOTIFY_KEY, false);
      return;
    }
  }
  notify.on = on;
  write(NOTIFY_KEY, on);
}

// Build ids queued from the plan or ⌘K whose end should notify.
const followed = new Set<string>(read<string[]>(FOLLOW_KEY, []));

function followJobs(ids: string[]) {
  for (const id of ids) followed.add(id);
  // Ids the queue never reports again (a factory restart drops its history) are
  // only trimmed here, oldest first.
  for (const id of [...followed].slice(0, Math.max(0, followed.size - 50))) followed.delete(id);
  write(FOLLOW_KEY, [...followed]);
}

$effect.root(() => {
  $effect(() => {
    const jobs = build.jobs;
    if (!jobs.length || !followed.size) return;
    let changed = false;
    for (const id of [...followed]) {
      const j = jobs.find((x) => x.id === id);
      if (!j || j.active) continue; // not in this poll yet, or still going
      followed.delete(id);
      changed = true;
      if (!notify.on || !notifyCan || Notification.permission !== "granted") continue;
      if (!document.hidden && document.hasFocus()) continue; // the page's own toast says it
      const title = j.status === "done" ? `${j.name} is built` : j.status === "error" ? `${j.name} failed` : `${j.name} was canceled`;
      const body = j.status === "done" ? j.ref : j.status === "error" ? j.error || "Open the build to see why." : "";
      try {
        const n = new Notification(title, { body, tag: j.id });
        n.onclick = () => {
          window.focus();
          go("builds", j.id);
          n.close();
        };
      } catch {
        /* some browsers only allow notifications from a service worker */
      }
    }
    if (changed) write(FOLLOW_KEY, [...followed]);
  });
});
