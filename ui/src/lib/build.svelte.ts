// The build queue as this tab sees it. Builds run on the SERVER, in a queue:
// one at a time per engine, the engines in parallel. This module mirrors the
// queue (/api/builds), streams each open job's log, and keeps a module-scoped
// poll timer, so switching pages doesn't drop a running build.
import { api, post } from "./api";
import { toast } from "./stores";
import { engineBadge } from "./util";
import type { BuildJob, BuildSnapshot } from "./types";

// Whether /api/builds has answered at least once (so the Builds page can tell
// "no builds" from "not loaded yet"), and the last error reaching it.
export const queue = $state({ loaded: false, error: "" });

export const build = $state({
  jobs: [] as BuildJob[],
  showHistory: false, // the Builds page lists every earlier build, not just the latest
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

// Log fetches in flight, by job id: the poll and a log the reader just opened
// must not both append the same lines.
const fetching = new Set<string>();

async function fetchLog(j: BuildJob) {
  if (fetching.has(j.id)) return;
  const v = view(j.id);
  const from = v.offset;
  fetching.add(j.id);
  let st: BuildSnapshot;
  try {
    st = await api<BuildSnapshot>(`/api/builds/state?id=${encodeURIComponent(j.id)}&offset=${from}`);
  } catch { return; } finally { fetching.delete(j.id); }
  if (v.offset !== from) return; // superseded meanwhile
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
    queue.error = "";
  } catch (e) {
    ok = false; // factory restarting — retry while builds were in flight
    queue.error = (e as Error).message || "no answer";
  } finally {
    ticking = false;
    queue.loaded = true;
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

// ---- Builds page: read a log, reorder, retry ------------------------------------

/** Show job `id`'s log and keep it current (the Builds page's open job). */
export function openLog(id: string) {
  const v = view(id);
  v.open = true;
  const j = build.jobs.find((x) => x.id === id);
  if (j && j.line_count > v.offset) fetchLog(j);
}

/** Stop keeping job `id`'s log current (what it has stays loaded). */
export function closeLog(id: string) {
  if (views[id]) views[id].open = false;
}

/** Cancel a build without asking (the Builds page asks first, in its own UI). */
export async function stopJob(j: BuildJob): Promise<boolean> {
  try {
    await post("/api/builds/cancel", { id: j.id });
    return true;
  } catch (e) {
    toast("Couldn't cancel it: " + (e as Error).message, 6000);
    return false;
  } finally {
    watchQueue();
  }
}

/** POST /api/build and /api/builds/retry answer with the new job and its place. */
export interface Enqueued {
  status: string;
  id: string;
  position: number; // builds ahead on its engine
  engine: string;
}

/** Queue a finished (failed, canceled or built) job again with the same settings. */
export async function retryJob(j: BuildJob): Promise<Enqueued | null> {
  try {
    const r = await post<Enqueued>("/api/builds/retry", { id: j.id });
    seen.set(r.id, "queued");
    const name = j.name || j.ref;
    toast(r.position > 0
      ? `${name} is queued again: ${r.position} ahead on ${engineBadge(r.engine)}.`
      : `Building ${name} again on ${engineBadge(r.engine)}.`);
    return r;
  } catch (e) {
    toast("Couldn't queue it again: " + (e as Error).message, 7000);
    return null;
  } finally {
    watchQueue();
  }
}

export type MoveTo = "up" | "down" | "top" | "bottom";

/** Move a queued build within its engine's lane. */
export async function moveJob(j: BuildJob, to: MoveTo): Promise<boolean> {
  try {
    const r = await post<{ jobs: BuildJob[] }>("/api/builds/move", { id: j.id, to });
    if (Array.isArray(r.jobs)) build.jobs = r.jobs;
    return true;
  } catch (e) {
    toast("Couldn't move it: " + (e as Error).message, 6000);
    return false;
  } finally {
    watchQueue();
  }
}

/**
 * Move a queued build from place `from` to place `to` among the `count` queued
 * builds of its lane (a drag). The API moves one step at a time, or to an end.
 */
export async function moveJobTo(j: BuildJob, from: number, to: number, count: number): Promise<boolean> {
  if (to === from) return true;
  if (to <= 0) return moveJob(j, "top");
  if (to >= count - 1) return moveJob(j, "bottom");
  const dir: MoveTo = to < from ? "up" : "down";
  for (let i = 0; i < Math.abs(to - from); i++) {
    if (!(await moveJob(j, dir))) return false;
  }
  return true;
}
