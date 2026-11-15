// Engine facts shared by the System and Overview pages: the GPU access check
// (POST /api/gpu/probe), this Mac's memory as the user told us (the factory
// runs inside Docker and can't see the host), and the server's build rules
// turned into numbers for each engine. The rules mirror internal/builder
// (engines.go compileFloorGB / decideBuild, preflight.go) and the Dockerfiles'
// BUILD_JOBS sizing; the numbers come from /api/sysinfo. Also the memory plan
// (how this Mac's memory could be split between the two engine machines) and
// the engines' versions from /readyz.
import { post } from "./api";
import { loadSysInfo, sysinfo } from "./stores";
import { engineName } from "./format";
import { DOCKER_MEMORY_PATH } from "./explain";
import type { EngineResources, GPUProbe, Model, SysInfo } from "./types";

// ---- rules the server applies (keep in step with the Go code) -----------------

/** GiB of free memory each parallel compile job gets (docker/Dockerfile.*: BUILD_JOBS=0). */
export const JOB_GIB = { cpu: 1.5, gpu: 2 } as const;
/** What a model needs beyond its weights to run (internal/server/resources.go runOverheadGB). */
export const RUN_OVERHEAD_GB = 2;
/** A build still running after this long is stopped (internal/server/buildqueue.go jobTimeout). */
export const JOB_TIMEOUT_H = 2;
/** The Podman size the factory's fix sets (explain.ts PODMAN_MEMORY_CMD: 12288 MB). */
export const PODMAN_FIX_GB = 12;

export const isGPU = (c: string | undefined) => c === "vulkan" || c === "cuda";

export function engineList(si: SysInfo | null): EngineResources[] {
  return si?.engines ?? [];
}

export function engineOf(si: SysInfo | null, engine: string): EngineResources | undefined {
  return si?.engines?.find((e) => e.engine === engine);
}

/** The engine that gives containers a GPU ("" gpu = none). */
export function gpuEngine(si: SysInfo | null): EngineResources | undefined {
  return si?.engines?.find((e) => !!e.gpu);
}

/**
 * A libkrun Podman machine (the only thing that sets FACTORY_GPU=vulkan on the
 * Podman engine) is the Mac GPU path, so it means this host is a Mac.
 */
export function isMac(si: SysInfo | null): boolean {
  return !!si?.engines?.some((e) => e.engine === "podman" && e.gpu === "vulkan");
}

/** Least engine memory a from-source build for compute needs (compile_min_gb). */
export function compileFloor(si: SysInfo | null, compute: string): number {
  return si?.compile_min_gb?.[compute] ?? (isGPU(compute) ? 6 : 3);
}

/**
 * Upper bound on parallel compile jobs a build of this kind runs on e: one per
 * JOB_GIB of memory, at most one per CPU, at least one (the Dockerfiles count
 * the memory that's free when the compile starts, so a busy engine runs fewer).
 * 0 when e can't compile it at all (below the compile floor) or is down.
 */
export function compileJobs(si: SysInfo | null, e: EngineResources, gpu: boolean): number {
  if (!e.reachable || !e.mem_gb) return 0;
  if (e.mem_gb < compileFloor(si, gpu ? "vulkan" : "cpu")) return 0;
  const gib = (e.mem_gb * 1e9) / 1024 ** 3;
  const jobs = Math.floor(gib / (gpu ? JOB_GIB.gpu : JOB_GIB.cpu));
  return Math.max(1, Math.min(e.cpus || 1, jobs));
}

/** The other engine, when it's up and big enough to compile what e can't. */
export function fallbackFor(si: SysInfo | null, e: EngineResources, compute: string): EngineResources | undefined {
  const need = compileFloor(si, compute);
  return si?.engines?.find((o) => o.engine !== e.engine && o.reachable && o.mem_gb >= need);
}

/**
 * An engine machine's size the way its settings show it: they report a little
 * under the configured size (an 18 GB Docker Desktop VM reads 18.8e9 bytes),
 * so round down, as the top bar does.
 */
export function vmGB(n: number): number {
  return Math.max(1, Math.floor(n));
}

/** The Podman resize command for a size in GB (the same form as PODMAN_MEMORY_CMD). */
export function podmanMemoryCmd(gbs: number): string {
  return `podman machine stop && podman machine set --memory ${Math.round(gbs * 1024)} && podman machine start`;
}

export const PODMAN_START_CMD = "podman machine start";

/** How the factory is started again, for "then restart the factory" notes. */
export function startScript(si: SysInfo | null): string {
  return isMac(si) ? "scripts/macos/start.sh" : "scripts/<your-os>/start.sh";
}

/** One line on what an engine gives containers: "Vulkan GPU", "no GPU". */
export function gpuLabel(e: EngineResources): string {
  return e.gpu === "vulkan" ? "Vulkan GPU" : e.gpu === "cuda" ? "CUDA GPU" : "no GPU";
}

// ---- the GPU access check ----------------------------------------------------------

export interface GPUCheck {
  running: boolean;
  results: GPUProbe[] | null;
  at: number; // when it last answered (unix ms), 0 = never
  error: string;
}

const CHECK_KEY = "llmfactory.gpucheck.v1";

function readCheck(): Pick<GPUCheck, "results" | "at"> {
  try {
    const v = JSON.parse(localStorage.getItem(CHECK_KEY) || "null");
    if (v && Array.isArray(v.results) && typeof v.at === "number") return { results: v.results, at: v.at };
  } catch {
    /* none saved */
  }
  return { results: null, at: 0 };
}

/** The last GPU access check (kept in this browser so a reload still shows it). */
export const gpuCheck = $state<GPUCheck>({ running: false, error: "", ...readCheck() });

/** Run the GPU access check on every engine (starts a throwaway container on each). */
export async function checkGPU(): Promise<void> {
  if (gpuCheck.running) return;
  gpuCheck.running = true;
  gpuCheck.error = "";
  try {
    const r = await post<{ engines: GPUProbe[] }>("/api/gpu/probe", {});
    gpuCheck.results = r.engines ?? [];
    gpuCheck.at = Date.now();
    try {
      localStorage.setItem(CHECK_KEY, JSON.stringify({ results: gpuCheck.results, at: gpuCheck.at }));
    } catch {
      /* private mode: it just won't survive a reload */
    }
    loadSysInfo();
  } catch (e) {
    gpuCheck.error = (e as Error).message || "no answer";
  } finally {
    gpuCheck.running = false;
  }
}

export function probeFor(engine: string): GPUProbe | undefined {
  return gpuCheck.results?.find((p) => p.engine === engine);
}

/** The engine's product line from the last check ("Docker Desktop · kernel …"), "" if unknown. */
export function engineSystem(engine: string): string {
  return probeFor(engine)?.system || "";
}

/**
 * "Docker Desktop" when the last GPU check said so, or on a Mac before any
 * check (what the factory's Mac setup uses), else "Docker" / "Podman".
 */
export function engineTitle(engine: string, si: SysInfo | null = null): string {
  if (engine !== "docker") return engineName(engine);
  const sys = engineSystem("docker");
  return /docker desktop/i.test(sys) || (!sys && isMac(si)) ? "Docker Desktop" : "Docker";
}

// ---- this Mac's memory (from the factory when it knows, else the user tells us) ----

const HOST_KEY = "llmfactory.hostmem.v1";

function readHost(): number | null {
  try {
    const v = parseFloat(localStorage.getItem(HOST_KEY) || "");
    return isFinite(v) && v > 0 ? v : null;
  } catch {
    return null;
  }
}

export const hostMem = $state({ gb: readHost(), detected: false });

// The factory knows the machine's memory when its start script passed it in
// (FACTORY_HOST_MEM_BYTES) or it runs natively; that beats a typed-in number.
sysinfo.subscribe((si) => {
  const v = si?.host_mem_gb;
  if (v && v > 0) {
    hostMem.gb = Math.round(v);
    hostMem.detected = true;
  }
});

export function setHostMem(v: number | null) {
  hostMem.gb = v && isFinite(v) && v > 0 ? v : null;
  try {
    if (hostMem.gb) localStorage.setItem(HOST_KEY, String(hostMem.gb));
    else localStorage.removeItem(HOST_KEY);
  } catch {
    /* it just won't survive a reload */
  }
}

/**
 * Giving Podman the fix's 12 GB would ask for more than this Mac has while
 * Docker keeps its size, so the fix is to plan both (known only once the user
 * has entered the Mac's memory).
 */
export function podmanFixNoRoom(si: SysInfo | null): boolean {
  const d = engineOf(si, "docker");
  return !!hostMem.gb && !!d?.reachable && d.mem_gb > 0 && vmGB(d.mem_gb) + PODMAN_FIX_GB > hostMem.gb;
}

/** A command that prints this machine's memory in GB. */
export function hostMemCmd(si: SysInfo | null): string {
  return isMac(si) ? `sysctl -n hw.memsize | awk '{print $1/1073741824 " GB"}'` : `free -g | awk '/^Mem:/{print $2 " GB"}'`;
}

// ---- engine versions (GET /readyz: each engine's server version, "" = down) ----------

export const engineVer = $state({ map: null as Record<string, string> | null });

export async function loadEngineVersions(): Promise<void> {
  try {
    // 503 only means no engine answered; the body still lists them.
    const r = await fetch("/readyz");
    const d = await r.json();
    if (d && typeof d.engines === "object" && d.engines) engineVer.map = d.engines;
  } catch {
    /* an older factory, or no answer: versions stay unknown */
  }
}

// ---- memory plan: both engine machines come out of this Mac's memory --------------

export type PlanId = "gpu" | "cpu" | "today";

export interface MemPlan {
  id: PlanId;
  title: string;
  text: string;
  docker: number | null; // GB for Docker's VM (null = unknown)
  podman: number | null; // GB for the Podman machine (0 = stopped, null = unknown)
  ok: boolean; // this machine can do it
  changes: boolean; // differs from how it's set now
}

export interface MemPlans {
  host: number | null;
  reserve: number | null; // what the plans leave for macOS and apps
  dockerNow: number | null;
  podmanNow: number | null; // 0 = not running
  gpuFloor: number;
  plans: MemPlan[]; // GPU first, CPU only, Today
  recommended: PlanId;
  /** Raising Podman to PODMAN_FIX_GB without shrinking Docker asks for more than the host has. */
  naiveOver: boolean;
}

/** What the plans leave for the host OS and apps: a fifth of it, 4 to 8 GB. */
export function osReserve(host: number): number {
  return Math.min(8, Math.max(4, Math.round(host * 0.2)));
}

/** The biggest chat model in the catalog that runs in gbs (weights + run overhead): "12B", or its name. */
export function biggestFit(cat: Model[], gbs: number, gpu: boolean): string {
  const m = cat
    .filter(
      (x) =>
        !x.legacy &&
        (x.modality ?? "text") === "text" &&
        x.size_gb > 0 &&
        x.size_gb + RUN_OVERHEAD_GB <= gbs &&
        (!gpu || (x.computes ?? []).includes("vulkan")),
    )
    .sort((a, b) => b.size_gb - a.size_gb)[0];
  if (!m) return "";
  return /^\d+(\.\d+)?B$/i.test(m.params) ? m.params : m.name;
}

export function memoryPlans(si: SysInfo | null, host: number | null, cat: Model[]): MemPlans {
  const d = engineOf(si, "docker");
  const p = engineOf(si, "podman");
  const dockerNow = d?.reachable && d.mem_gb ? vmGB(d.mem_gb) : null;
  const podmanNow = !p ? null : p.reachable && p.mem_gb ? vmGB(p.mem_gb) : 0;
  const gpuFloor = compileFloor(si, p?.gpu || "vulkan");
  const dockerMin = compileFloor(si, "cpu");
  const reserve = host ? osReserve(host) : null;
  const fitsHost = (dm: number | null, pm: number | null) => !host || dm == null || pm == null || dm + pm <= host;
  const gpuText = (pm: number) => {
    const fit = biggestFit(cat, pm, true);
    return fit ? `Podman compiles and runs GPU models up to ${fit}.` : "Podman compiles and runs GPU models.";
  };

  // Today: as the engines report themselves. Fine when Podman can compile for
  // its GPU and both leave macOS at least 4 GB.
  const todayOK =
    !!p?.reachable && (podmanNow ?? 0) >= gpuFloor && (!host || dockerNow == null || dockerNow + (podmanNow ?? 0) <= host - 4);
  let todayText: string;
  if (!p) todayText = "Docker only.";
  else if (!p.reachable) todayText = "Podman isn't running, so nothing runs on the GPU.";
  else if ((podmanNow ?? 0) < gpuFloor) todayText = "Podman is too small to build for GPU.";
  else if (!fitsHost(dockerNow, podmanNow)) todayText = "Both engines together ask for more than this Mac has.";
  else todayText = gpuText(podmanNow ?? 0);
  const today: MemPlan = {
    id: "today",
    title: "Today",
    text: todayText,
    docker: dockerNow,
    podman: podmanNow,
    ok: fitsHost(dockerNow, podmanNow),
    changes: false,
  };

  // GPU first: Podman gets the factory's 12 GB (more when there's room beside
  // Docker); Docker shrinks only as far as it has to, never below what a CPU
  // build needs.
  let gpu: MemPlan;
  if (todayOK && (podmanNow ?? 0) >= PODMAN_FIX_GB) {
    gpu = { ...today, id: "gpu", title: "GPU first", text: `${gpuText(podmanNow ?? 0)} It's set up this way now.` };
  } else if (host && reserve != null) {
    const avail = host - reserve;
    const pm = Math.min(dockerNow != null ? Math.max(PODMAN_FIX_GB, avail - dockerNow) : PODMAN_FIX_GB, avail - dockerMin);
    const dm = dockerNow != null ? Math.min(dockerNow, avail - pm) : avail - pm;
    if (pm < gpuFloor) {
      gpu = {
        id: "gpu",
        title: "GPU first",
        text: `No room: a GPU build needs ${gpuFloor} GB on Podman, beside Docker and macOS.`,
        docker: null,
        podman: null,
        ok: false,
        changes: false,
      };
    } else {
      const shrinks = dockerNow != null && dm < dockerNow;
      gpu = {
        id: "gpu",
        title: "GPU first",
        text: `${gpuText(pm)} ${shrinks ? "Docker keeps small CPU models." : "Docker stays as it is."}`,
        docker: dm,
        podman: pm,
        ok: true,
        changes: dm !== dockerNow || pm !== podmanNow,
      };
    }
  } else {
    gpu = {
      id: "gpu",
      title: "GPU first",
      text: `${gpuText(PODMAN_FIX_GB)} Docker shrinks to make room.`,
      docker: null,
      podman: PODMAN_FIX_GB,
      ok: true,
      changes: podmanNow !== PODMAN_FIX_GB,
    };
  }

  const cpu: MemPlan = {
    id: "cpu",
    title: "CPU only",
    text: "Everything builds and runs on Docker. No GPU.",
    docker: dockerNow,
    podman: 0,
    ok: true,
    changes: podmanNow !== 0,
  };

  return {
    host,
    reserve,
    dockerNow,
    podmanNow,
    gpuFloor,
    plans: [gpu, cpu, today],
    recommended: !todayOK && gpu.ok && gpu.changes ? "gpu" : "today",
    naiveOver: !!host && dockerNow != null && (podmanNow ?? 0) < PODMAN_FIX_GB && dockerNow + PODMAN_FIX_GB > host,
  };
}

export interface PlanStep {
  text: string;
  cmd?: string;
}

/** What to run on the host to get from today's sizes to plan's. */
export function planSteps(mp: MemPlans, plan: MemPlan, si: SysInfo | null): PlanStep[] {
  const steps: PlanStep[] = [];
  const podmanUp = !!engineOf(si, "podman")?.reachable;
  if (plan.id === "cpu") {
    if (podmanUp) {
      steps.push({ text: "Stop the Podman machine. GPU models running there stop with it; their images stay for later.", cmd: "podman machine stop" });
    }
    return steps;
  }
  if (plan.id !== "gpu" || !plan.changes) return steps;
  if (plan.docker != null && mp.dockerNow != null && plan.docker < mp.dockerNow) {
    steps.push({ text: `Shrink Docker to ${plan.docker} GB first: ${DOCKER_MEMORY_PATH}, then Apply & restart.` });
  } else if (plan.docker == null) {
    steps.push({ text: `Make room in Docker first (${DOCKER_MEMORY_PATH}): Docker and Podman's ${plan.podman} GB both come out of this Mac's memory.` });
  }
  if (plan.podman != null && plan.podman !== mp.podmanNow) {
    steps.push({
      text: `Give Podman ${plan.podman} GB:`,
      cmd: podmanUp
        ? podmanMemoryCmd(plan.podman)
        : `podman machine set --memory ${Math.round(plan.podman * 1024)} && podman machine start`,
    });
  }
  steps.push({ text: "Then start the factory again so it sees the new sizes:", cmd: startScript(si) });
  return steps;
}
