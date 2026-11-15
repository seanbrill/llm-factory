// API shapes, mirroring the Go structs (internal/catalog, internal/builder,
// internal/server, internal/ensemble). Hand-maintained at this size.

export type Modality =
  | "text" | "code" | "reasoning" | "vision" | "embedding"
  | "image" | "video" | "audio-stt" | "tts";

export interface WeightFile { file: string; url: string; role?: string; }

export interface Model {
  id: string;
  name: string;
  repo: string;
  file: string;
  url: string;
  size_gb: number;
  tier: string;
  params: string;
  min_ram_gb: number;
  min_vram_gb: number;
  recommended: boolean;
  description: string;
  modality?: Modality;
  mmproj_file?: string;
  mmproj_url?: string;
  extra_files?: WeightFile[];
  runtime?: "cpp" | "python" | "audiocpp"; // default cpp (GGUF); python = PyTorch; audiocpp = audio.cpp
  computes?: string[];        // compute targets this model's runtime can be built for
  legacy?: boolean;           // superseded; hidden in the Build picker by default
  compiles?: boolean;         // its build compiles a runtime from source (needs compile_min_gb on the engine)
  voice_clone?: boolean;            // TTS that speaks in a voice cloned from a reference recording
  clone_needs_transcript?: boolean; // ...and that recording needs its transcript
  license?: string;                 // weights' license: SPDX id or short name ("CPML")
  commercial_use?: boolean;         // false = the license forbids commercial use; absent = not stated
}

// Build identity from /api/version (stamped by the start scripts).
export interface VersionInfo {
  version: string; // e.g. "1.0.33" (VERSION major.minor + commit count), "dev" if unstamped
  commit: string;  // short SHA, "-dirty" when built with local edits
}

// One container engine's machine (Docker Desktop's VM, the Podman machine) as
// its containers see it, from /api/sysinfo.
export interface EngineResources {
  engine: string;       // "docker" | "podman"
  reachable: boolean;   // answered just now
  mem_gb: number;
  cpus: number;
  gpu: string;          // compute it gives containers: "" | "vulkan" | "cuda"
  // How the factory knows: "host" = the start script saw it (a libkrun Podman
  // machine, an NVIDIA card); "probe" = the GPU check got /dev/dri into a
  // container here (Docker VMM, a krunkit VM).
  gpu_from?: "host" | "probe" | "";
  gpu_experimental?: boolean; // a GPU path the factory's images aren't verified on
  gpu_check?: GPUCheckInfo;   // the last saved GPU check on this engine
  system?: string;      // "Docker Desktop · kernel 7.0.14-linuxkit", when it answered
  low_memory?: boolean; // a GPU engine too small to compile GPU images
  fix: string;          // how to give this machine more memory
}

// An engine's saved GPU check, as /api/sysinfo reports it.
export interface GPUCheckInfo extends GPUProbe {
  checked_at: number;     // unix ms
  source: "probe" | "run"; // the GPU check, or a Vulkan run that found no /dev/dri
  stale?: boolean;        // the engine's machine changed since
  stale_reason?: string;  // "its kernel changed (… → …)"
}

export interface SysInfo {
  // Legacy summary: the GPU engine when it's reachable, else the first engine.
  mem_gb: number;
  cpus: number;
  gpu: string;     // "" | "cuda" | "vulkan"
  vram_gb: number; // 0 if unknown
  engine: string;
  engines?: EngineResources[];
  default_engine?: string;  // build form defaults for this machine
  default_compute?: string;
  compile_min_gb?: Record<string, number>; // compute -> least engine memory to compile
  host_mem_gb?: number; // the computer's own memory (GiB), when the factory can tell
  host_os?: string;     // "darwin" | "linux" | "windows", when the factory can tell
}

// Build queue (/api/builds). Mirrors internal/server/buildqueue.go.
export type BuildStatus = "queued" | "running" | "done" | "error" | "canceled";

export interface BuildConfig {
  model_id: string;
  image_name: string;
  tag: string;
  engine: string;
  compute: string;
  system_prompt: string;
  inject_mode: string;
  ctx_size: number;
  memory_gb: number;
  route: string;
  autostart: boolean;
}

export interface BuildJob {
  id: string;
  kind: "model" | "ensemble";
  status: BuildStatus;
  active: boolean;       // queued or running
  ref: string;
  model_id: string;
  name: string;
  engine: string;        // the engine the image is for (its queue lane)
  build_engine?: string; // the engine compiling it, once chosen (differs from engine when transferred)
  compute: string;
  config: BuildConfig;
  position: number;      // queued: unfinished builds ahead on its engine
  wait_seconds: number;  // queued: estimated seconds until it starts, 0 = unknown
  created_at: number;    // unix ms
  started_at: number;    // unix ms, 0 = not yet
  finished_at: number;   // unix ms, 0 = not yet
  elapsed_ms: number;
  eta_seconds: number;   // expected run time, 0 = unknown
  last_line: string;
  error?: string;
  line_count: number;
}

export interface BuildSnapshot extends BuildJob {
  lines: string[];
  next_offset: number;
}

// One engine's answer to POST /api/gpu/probe.
export interface GPUProbe {
  engine: string;
  reachable: boolean;
  system?: string;
  dri: boolean;
  dri_devices?: string[];
  dri_error?: string;
  // Who could open the render node: "all", "group" (render_gid; runs join it),
  // "root" (the images' user can't), "none" (no render node), "" (unknown).
  render_access?: string;
  render_gid?: number;
  cdi_devices?: string[];
  webgpu: boolean;
  verdict: string;
}

// docker/podman list records arrive with capitalised keys + a comma-joined
// Labels string (docker) or object (podman) — kept loose, read via labelVal().
export interface ContainerInfo {
  ID?: string;
  Names?: string;
  Image?: string;
  Engine?: string;
  Status?: string;
  State?: string;
  Ports?: string;
  Labels?: string | Record<string, string>;
}

export interface ImageInfo {
  ID?: string;
  Repository?: string;
  Tag?: string;
  Size?: string;
  Engine?: string;
  Compute?: string;
  Labels?: string | Record<string, string>;
}

export interface ModelFile {
  id: string;
  name: string;
  file: string;
  downloaded: boolean;
  on_disk_gb: number;
}

export interface Persona { id: string; name: string; prompt: string; }

export interface EnsembleMember {
  tool: string;
  modality: string;
  image: string;
  port?: number;
  vram_gb?: number;
}
export interface Ensemble {
  id: string;
  name: string;
  package_mode: string;  // orchestrated | embedded
  routing: string;       // heuristic | tool-calling
  conductor?: string;
  vram_budget_gb: number;
  engine?: string;
  compute?: string;
  members: EnsembleMember[];
}

export interface ChatMessage {
  role: "user" | "assistant" | "system";
  content: string;
}

export interface RunningModel {
  name: string;
  model_id: string;
  compute: string;
  vram_gb: number;
  ram_gb: number;
}
// Live resource picture from /api/resources. used_*/free_* are GLOBAL (whole
// machine: VRAM via nvidia-smi, RAM via /proc/meminfo) so usage outside the
// factory counts. committed_* is the factory-managed subset. global_* say whether
// the used figure is a real measurement (true) or fell back to the estimate.
export interface ResourceBudget {
  gpu: string;
  total_vram_gb: number;
  total_ram_gb: number;
  used_vram_gb: number;
  used_ram_gb: number;
  free_vram_gb: number;
  free_ram_gb: number;
  committed_vram_gb: number;
  committed_ram_gb: number;
  global_vram: boolean;
  global_ram: boolean;
  cpus: number;
  cpu_load1: number;
  cpu_used_pct: number;
  running: RunningModel[] | null;
}

// POST /api/build/preflight: where each planned build compiles, what would stop
// it, and what it downloads. Mirrors internal/server/plan.go.
export interface PreflightItem {
  model_id: string;
  name: string;
  engine: string;        // the engine the image is for
  compute: string;       // the compute it will actually build for
  compiles: boolean;     // compiles a runtime from source
  build_engine: string;  // the engine that runs the build
  transfer: boolean;     // built on build_engine, then loaded into engine
  need_gb: number;       // least engine memory to compile (0 = doesn't compile)
  build_gb: number;      // memory of the building machine (0 = unknown)
  note?: string;
  error?: string;        // the build would fail before starting
  weights_gb: number;
  download_gb: number;   // still to download (0 = on disk / self-contained)
  eta_seconds: number;   // last build time for this compute (0 = unknown)
  run_block: boolean;    // too big to run on its engine at all
  run_note?: string;     // tight fit / slow paging warning
}

export interface EngineMemoryRead {
  installed: boolean;
  reachable: boolean;
  mem_gb: number;        // 0 = unknown
  error?: string;
}

export interface PreflightResult {
  items: PreflightItem[];
  engines: Record<string, EngineMemoryRead>;
  queued: Record<string, number>; // builds already queued or running, per engine
  download_gb: number;
  disk_need_gb: number;  // downloads + exported tars
  disk_free_gb?: number;
  disk_total_gb?: number;
}

// GET /api/storage
export interface UsageItem { name: string; gb: number; }
export interface UsageGroup { count: number; gb: number; items: UsageItem[]; }
export interface StorageInfo {
  weights: UsageGroup;
  tars: UsageGroup;
  leftovers: UsageGroup; // empty while a build runs (busy)
  media: UsageGroup;     // count + gb only
  busy: boolean;
  disk_total_gb?: number;
  disk_free_gb?: number;
}
