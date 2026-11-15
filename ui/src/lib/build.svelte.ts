// Build-page state hoisted to a module store so it survives page navigation.
// Builds run on the SERVER, in a queue: one at a time per engine, the engines in
// parallel. This store owns the form selections, a mirror of the queue
// (/api/builds), each job's streamed log, and a module-scoped poll timer — so
// switching app pages doesn't drop a running build, and attach() picks the
// queue back up after a full page refresh (which clears module state).
import { api, post } from "./api";
import { toast } from "./stores";
import { ctxDefaultFor, engineBadge } from "./util";
import type { BuildConfig, BuildJob, BuildSnapshot, Model, SysInfo } from "./types";

export const build = $state({
  // picker + filters
  q: "",
  filterMod: "",
  filterTags: [] as string[],
  maxSize: 0,
  showLegacy: false,
  pickerOpen: false,
  selected: null as Model | null,
  nameTouched: false,
  // form
  imageName: "",
  tag: "latest",
  // Replaced by this machine's defaults from /api/sysinfo (applyMachineDefaults)
  // until the user picks an engine/compute themselves (formTouched).
  engine: "docker",
  compute: "cpu",
  formTouched: false,
  systemPrompt: "",
  injectMode: "missing",
  ctxTier: "auto",
  memTier: "0",
  route: "",
  autostart: false,
  // queue
  submitting: false,
  error: "",
  jobs: [] as BuildJob[],
  showHistory: false,
});

// A job's log and progress as this tab has streamed it, keyed by job id.
export interface JobView {
  open: boolean; // log expanded
  log: string;
  offset: number; // next log line to fetch
  phase: string;
  pct: number | null; // determinate progress (downloads), else null
}
export const views = $state<Record<string, JobView>>({});

// The compute the selected model will actually build for. build.compute is the
// user's pick, kept as-is so it comes back when they switch models; a runtime
// that doesn't ship it (Piper TTS is CPU-only, image gen Vulkan-only) uses CPU,
// else whichever variant it does have.
export function effectiveCompute(): string {
  const have = build.selected?.computes;
  if (!have?.length || have.includes(build.compute)) return build.compute;
  return have.includes("cpu") ? "cpu" : have[0];
}

export function ctxSize(): number {
  if (build.ctxTier === "auto") return ctxDefaultFor(effectiveCompute());
  if (build.ctxTier === "max") return 0; // server resolves
  return parseInt(build.ctxTier, 10) || 0;
}

// Machine-aware form defaults: a Mac with a libkrun Podman machine builds for
// podman + vulkan, an NVIDIA host for docker + cuda, anything else docker + cpu.
// Never overrides an engine/compute the user (or a restored build) chose.
export function applyMachineDefaults(si: SysInfo | null) {
  if (!si?.default_engine || build.formTouched) return;
  build.engine = si.default_engine;
  build.compute = si.default_compute || "cpu";
}

// ---- queue polling -----------------------------------------------------------

let timer: ReturnType<typeof setTimeout> | undefined;
let ticking = false;
let again = false;
let lastActive = false;
const seen = new Map<string, string>(); // job id -> status this tab last saw (for toasts)

function view(id: string): JobView {
  if (!views[id]) views[id] = { open: false, log: "", offset: 0, phase: "", pct: null };
  return views[id];
}

// Derive a phase label + determinate % from a streamed log line.
function trackPhase(v: JobView, line: string) {
  const dl = line.match(/Downloading\s+(.+?)\s*\.\.\./);
  if (dl) { v.phase = "Downloading " + dl[1]; v.pct = 0; return; }
  const pc = line.match(/(\d+)%\s*\(([\d.]+)\s*\/\s*([\d.]+)\s*GB\)/);
  if (pc) { v.pct = parseInt(pc[1], 10); return; }
  if (/^Saved /.test(line)) { v.pct = 100; return; }
  if (/^Stag(ing|ed)/.test(line)) { v.phase = line.trim(); v.pct = null; return; }
  if (/^(Memory check|Transfer \d\/\d|Loaded into|Exporting)/.test(line) || / so it compiles on /.test(line)) {
    v.phase = line.trim().slice(0, 140); v.pct = null; return;
  }
  if (/^#\d+/.test(line)) { v.phase = "Building image — " + line.slice(0, 64).trim(); v.pct = null; return; }
}

async function fetchLog(j: BuildJob) {
  const v = view(j.id);
  let st: BuildSnapshot;
  try {
    st = await api<BuildSnapshot>(`/api/builds/state?id=${encodeURIComponent(j.id)}&offset=${v.offset}`);
  } catch { return; }
  const lines = st.lines ?? [];
  if (lines.length) {
    v.log += lines.join("\n") + "\n";
    for (const l of lines) trackPhase(v, l);
  }
  v.offset = st.next_offset ?? v.offset;
}

// A finished job this tab watched (or started) gets a toast. Jobs first seen
// already finished — history after a refresh — don't.
function noteTransition(j: BuildJob) {
  const before = seen.get(j.id);
  seen.set(j.id, j.status);
  if (j.active || !before || before === j.status) return;
  const v = views[j.id];
  if (j.status === "done" && v) { v.pct = 100; v.phase = "Image built and exported"; }
  if (j.kind !== "model") return; // the Ensembles page reports its own builds
  if (j.status === "done") toast(`Image built: ${j.ref}`);
  else if (j.status === "error") toast(`Build failed: ${j.error || "see its log"}`, 9000);
  else if (j.status === "canceled") toast(`Build canceled: ${j.ref}`);
}

function schedule(ms: number) {
  clearTimeout(timer);
  timer = setTimeout(tick, ms);
}

async function tick() {
  timer = undefined;
  ticking = true;
  let ok = true;
  try {
    const jobs = (await api<{ jobs: BuildJob[] }>("/api/builds")).jobs ?? [];
    build.jobs = jobs;
    // Stream every running job, and keep any log this tab opened or followed current.
    await Promise.all(
      jobs
        .filter((j) => {
          const v = views[j.id];
          return j.status === "running" || (!!v && (v.open || v.offset > 0) && j.line_count > v.offset);
        })
        .map(fetchLog),
    );
    for (const j of jobs) noteTransition(j);
    for (const id of Object.keys(views)) if (!jobs.some((j) => j.id === id)) delete views[id];
    lastActive = jobs.some((j) => j.active);
  } catch {
    ok = false; // factory restarting — retry while builds were in flight
  } finally {
    ticking = false;
  }
  if (again) { again = false; schedule(0); }
  else if (lastActive) schedule(ok ? 1000 : 3000);
}

// watchQueue refreshes the queue now and keeps polling while builds are queued
// or running.
export function watchQueue() {
  if (ticking) { again = true; return; }
  schedule(0);
}

// ---- actions -------------------------------------------------------------------

export async function startBuild() {
  if (!build.selected) { alert("Pick a model first."); return; }
  build.submitting = true;
  build.error = "";
  const body = {
    model_id: build.selected.id, image_name: build.imageName, tag: build.tag,
    engine: build.engine, compute: effectiveCompute(), system_prompt: build.systemPrompt,
    inject_mode: build.injectMode, ctx_size: ctxSize(), memory_gb: parseFloat(build.memTier) || 0,
    route: build.route, autostart: build.autostart,
  };
  try {
    const r = await post<{ id: string; position: number; engine: string }>("/api/build", body);
    seen.set(r.id, "queued");
    // Follow the new build's log unless the user is already reading another.
    view(r.id).open = !Object.values(views).some((v) => v.open);
    toast(r.position > 0
      ? `Added to the queue — ${r.position} build${r.position > 1 ? "s" : ""} ahead on ${engineBadge(r.engine)}.`
      : "Build started.");
  } catch (e) {
    build.error = (e as Error).message;
  } finally {
    build.submitting = false;
  }
  watchQueue();
}

export async function cancelJob(j: BuildJob) {
  if (j.status === "running" && !confirm(`Cancel the running build of ${j.ref}?`)) return;
  try {
    await post("/api/builds/cancel", { id: j.id });
  } catch (e) {
    toast("Cancel failed: " + (e as Error).message);
  }
  watchQueue();
}

export function toggleLog(j: BuildJob) {
  const v = view(j.id);
  v.open = !v.open;
  if (v.open && j.line_count > v.offset) fetchLog(j);
}

// restoreForm refills the form from a build's saved config.
function restoreForm(c: Partial<BuildConfig>, catalog: Model[]) {
  const m = catalog.find((x) => x.id === c.model_id);
  if (m) { build.selected = m; build.nameTouched = true; }
  if (c.image_name) build.imageName = c.image_name;
  if (c.tag) build.tag = c.tag;
  if (c.engine) build.engine = c.engine;
  if (c.compute) build.compute = c.compute;
  build.formTouched = true;
  if (typeof c.system_prompt === "string") build.systemPrompt = c.system_prompt;
  if (c.inject_mode) build.injectMode = c.inject_mode;
  if (c.route) build.route = c.route;
  build.autostart = !!c.autostart;
}

// attach loads the queue when the Build page mounts. After a full page refresh
// it also restores the form from the newest queued/running build and opens the
// running build's log, so the page comes back looking as it did; on plain page
// navigation it just refreshes the queue (the poll loop never stopped).
let attached = false;
export async function attach(catalog: Model[]) {
  if (attached) { watchQueue(); return; }
  attached = true;
  await tick();
  const newest = [...build.jobs].reverse().find((j) => j.active && j.kind === "model");
  if (newest && !build.formTouched) restoreForm(newest.config, catalog);
  const running = build.jobs.find((j) => j.status === "running");
  if (running && !Object.values(views).some((v) => v.open)) view(running.id).open = true;
}
