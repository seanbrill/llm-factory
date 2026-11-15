// Native packages (docs/NATIVE.md): a model and its agent harness as one zip
// per operating system that runs without Docker or Podman. API types and calls
// for /api/native/*, used by the Library's "Native package" dialog.
import { api, post } from "./api";
import type { HarnessConfig, Model } from "./types";

export interface NativeTarget {
  id: string;            // macos-arm64 | linux-x64 | windows-x64
  label: string;         // "macOS (Apple Silicon)"
  os: string;            // macos | linux | windows
  accel: string;         // metal | vulkan | cpu
  gpu_layers: string;    // the launcher's default --n-gpu-layers
  requirements: string;
  preview: boolean;      // packaged like macOS, not yet run on such a machine
  asset: string;         // the llama.cpp release file it carries
  ready: boolean;        // this factory can make its launcher
  why?: string;
}

export interface NativeSource {
  model_id: string;
  model_name: string;
  size_gb: number;
  modality: string;
  capable: boolean;
  why?: string;
  from_image?: string;
  system_prompt: string;
  inject_mode: string;
  ctx_size: number;
  harness: HarnessConfig | null; // the shell already removed
  shell_dropped: boolean;
  default_names: string[];       // per target, in the targets' order
}

export interface NativePackage {
  name: string;
  file: string;
  size_bytes: number;
  sha256?: string;
  model_id: string;
  model_name: string;
  target: string;
  target_label: string;
  created: string;
  ctx_size?: number;
  system_prompt: boolean;
  toolsets: string[];
  llama_cpp: string;
  license?: string;
  shareable: boolean;
  source_url?: string;
  from_image?: string;
}

export interface NativeJobState {
  id: string;
  status: "queued" | "running" | "done" | "error" | "canceled";
  active: boolean;
  name: string;          // the model's name
  package: string;
  target: string;
  target_label: string;
  error?: string;
  elapsed_ms: number;
  lines: string[];
  next_offset: number;
}

/** A packaging job as /api/native/jobs lists it. */
export interface NativeJobSummary {
  id: string;
  status: NativeJobState["status"];
  active: boolean;
  model_id: string;
  name: string;
  package: string;
  target: string;
  target_label: string;
  last_line: string;
  error?: string;
}

export const listPackageJobs = () => api<{ jobs: NativeJobSummary[] }>("/api/native/jobs");

export interface NativeRequest {
  ref?: string;
  engine?: string;
  model_id?: string;
  target: string;
  name?: string;
  system_prompt?: string;
  inject_mode?: string;
  ctx_size?: number;
  harness?: HarnessConfig | false;
}

export const nativeTargets = () => api<{ targets: NativeTarget[]; llama_cpp: string }>("/api/native/targets");

export function nativeSource(from: { ref: string; engine: string } | { model_id: string }): Promise<NativeSource> {
  const q = "ref" in from
    ? `ref=${encodeURIComponent(from.ref)}&engine=${encodeURIComponent(from.engine)}`
    : `model_id=${encodeURIComponent(from.model_id)}`;
  return api<NativeSource>(`/api/native/source?${q}`);
}

export const startPackage = (req: NativeRequest) =>
  post<{ id: string; status: string; position: number; package: string }>("/api/native/package", req);

export const packageState = (id: string, offset: number) =>
  api<NativeJobState>(`/api/native/state?id=${encodeURIComponent(id)}&offset=${offset}`);

export const cancelPackage = (id: string) => post("/api/native/cancel", { id });

export const listPackages = () => api<{ packages: NativePackage[] }>("/api/native/packages");

export const deletePackage = (name: string) => post("/api/native/delete", { name });

export const downloadURL = (name: string) => `/api/native/download?name=${encodeURIComponent(name)}`;

/** Why a model can't be packaged natively ("" = it can); mirrors builder.NativeCapable. */
export function nativeWhy(m: Pick<Model, "name" | "modality" | "runtime"> | null | undefined): string {
  if (!m) return "Only catalog models can be packaged.";
  if (m.runtime === "python") return `${m.name} runs on Python (PyTorch), which stays container-only.`;
  if (m.runtime === "audiocpp") return `${m.name} runs on audio.cpp, which native packages don't carry yet.`;
  switch (m.modality ?? "text") {
    case "text":
    case "code":
    case "reasoning":
    case "vision":
      return "";
    case "embedding":
      return "Native packages run chat models for now.";
    default:
      return `Native packages run llama.cpp chat models for now; ${m.name} needs another engine.`;
  }
}

/** The packing progress in a log ("  40% (1.20 / 3.00 GB)"), 0–1, or null. */
export function packPercent(lines: readonly string[]): number | null {
  for (let i = lines.length - 1; i >= 0; i--) {
    const m = lines[i].match(/^\s+(\d{1,3})% \(/);
    if (m) return Math.min(100, parseInt(m[1], 10)) / 100;
  }
  return null;
}

/** About how much llama.cpp (llama-server and its libraries in b12110) and the
 * launcher add to a package, unzipped, in MB: ~32 + 7 on macOS, ~90 + 8 on
 * Linux and Windows (the Vulkan builds carry a CPU backend per CPU family). */
export function runtimeMB(t: Pick<NativeTarget, "os"> | null | undefined): number {
  return t?.os === "macos" ? 40 : 100;
}

/** "macOS (Apple Silicon)" -> how to start it there, in one line. */
export function startHow(t: Pick<NativeTarget, "os">): string {
  switch (t.os) {
    case "macos":
      return "Unzip it and double-click Start.command. The first time, macOS asks you to allow it in System Settings → Privacy & Security.";
    case "windows":
      return "Unzip it (Extract All) and double-click Start.bat. SmartScreen may warn the first time: More info → Run anyway.";
    default:
      return "Unzip it and run ./start.sh in a terminal.";
  }
}
