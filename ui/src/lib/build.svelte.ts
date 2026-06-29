// Build-page state hoisted to a module store so it survives page navigation.
// The build runs on the SERVER; this store owns the form selections, the live
// run state, and a module-scoped poll timer. Because the timer lives here (not
// in the component), switching app pages no longer drops the active build — the
// log keeps streaming and returning to Build shows it live. attachIfRunning()
// additionally reconnects after a full page refresh (which clears module state).
import { api, post } from "./api";
import { toast } from "./stores";
import { ctxDefaultFor } from "./util";
import type { Model } from "./types";

export const build = $state({
  // picker + filters
  q: "",
  filterMod: "",
  filterTags: [] as string[],
  maxSize: 0,
  pickerOpen: false,
  selected: null as Model | null,
  nameTouched: false,
  // form
  imageName: "",
  tag: "latest",
  engine: "docker",
  compute: "cuda",
  systemPrompt: "",
  injectMode: "missing",
  ctxTier: "auto",
  memTier: "0",
  route: "",
  autostart: false,
  // run state
  building: false,
  status: "",
  phase: "",
  pct: null as number | null,
  log: "",
  showLog: false,
});

let timer: ReturnType<typeof setInterval> | undefined;
let offset = 0;

export function ctxSize(): number {
  if (build.ctxTier === "auto") return ctxDefaultFor(build.compute);
  if (build.ctxTier === "max") return 0; // server resolves
  return parseInt(build.ctxTier, 10) || 0;
}

// Derive a phase label + determinate % from a streamed log line.
function trackPhase(line: string) {
  const dl = line.match(/Downloading\s+(.+?)\s*\.\.\./);
  if (dl) { build.phase = "Downloading " + dl[1]; build.pct = 0; return; }
  const pc = line.match(/(\d+)%\s*\(([\d.]+)\s*\/\s*([\d.]+)\s*GB\)/);
  if (pc) { build.pct = parseInt(pc[1], 10); return; }
  if (/^Saved /.test(line)) { build.pct = 100; return; }
  if (/^Stag(ing|ed)/.test(line)) { build.phase = line.trim(); build.pct = null; return; }
  if (/^#\d+/.test(line)) { build.phase = "Building image — " + line.slice(0, 64).trim(); build.pct = null; return; }
}

interface Snap { active?: boolean; status?: string; lines?: string[]; next_offset?: number; config?: Record<string, any> }

function poll() {
  clearInterval(timer);
  timer = setInterval(async () => {
    let st: Snap;
    try { st = await api("/api/build/state?offset=" + offset); } catch { return; }
    for (const l of st.lines ?? []) { build.log += l + "\n"; trackPhase(l); }
    offset = st.next_offset ?? offset;
    const running = !!st.active || st.status === "running";
    if (st.status) build.status = running ? "Building…" : st.status === "done" ? "Done ✓" : "Failed";
    if (!running) {
      clearInterval(timer); timer = undefined;
      build.building = false;
      if (st.status === "done") { build.pct = 100; build.phase = "Image built and exported"; toast("Image built."); }
      else if (st.status === "failed") { build.phase = "Build failed — see log"; toast("Build failed — see log."); }
    }
  }, 800);
}

export async function startBuild() {
  if (!build.selected) { alert("Pick a model first."); return; }
  build.building = true;
  build.status = "Starting…";
  build.phase = "";
  build.pct = null;
  build.log = "";
  build.showLog = true;
  offset = 0;
  const body = {
    model_id: build.selected.id, image_name: build.imageName, tag: build.tag,
    engine: build.engine, compute: build.compute, system_prompt: build.systemPrompt,
    inject_mode: build.injectMode, ctx_size: ctxSize(), memory_gb: parseFloat(build.memTier) || 0,
    route: build.route, autostart: build.autostart,
  };
  try {
    await post("/api/build", body);
  } catch (e) {
    build.status = "Error: " + (e as Error).message;
    build.building = false;
    return;
  }
  poll();
}

// Reconnect to a build still running on the server. No-op when this store is
// already tracking one (the page-navigation case — the timer never stopped).
// Restores the selected model + form from the build's saved config so a page
// refreshed mid-build comes back looking as it did.
export async function attachIfRunning(catalog: Model[]) {
  if (build.building || timer) return;
  let st: Snap;
  try { st = await api("/api/build/state?offset=0"); } catch { return; }
  if (!(st.active || st.status === "running")) return;
  const c = st.config ?? {};
  const m = catalog.find((x) => x.id === c.model_id);
  if (m) { build.selected = m; build.nameTouched = true; }
  if (c.image_name) build.imageName = c.image_name;
  if (c.tag) build.tag = c.tag;
  if (c.engine) build.engine = c.engine;
  if (c.compute) build.compute = c.compute;
  if (typeof c.system_prompt === "string") build.systemPrompt = c.system_prompt;
  if (c.inject_mode) build.injectMode = c.inject_mode;
  if (c.route) build.route = c.route;
  build.autostart = !!c.autostart;
  build.building = true;
  build.status = "Building…";
  build.showLog = true;
  offset = 0;
  build.log = "";
  poll();
}
