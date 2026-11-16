// Runtime tuning (docs/TUNING.md): llama-server settings baked into an image,
// saved per image (config/tuning.json) and overridable per run. The Tune page
// (#tune/<engine>/<ref>), the Run dialog and the Build plan use these.
import { api, post } from "./api";

export type Preset = "speed" | "balanced" | "long_context" | "quality" | "custom";
export type Switch = "auto" | "on" | "off";
export type KvCache = "f16" | "q8_0" | "q4_0";
export type Spec = "none" | "ngram" | "draft";

/** One set of settings. Every field is optional: unset means llama.cpp's default (or the layer below). */
export interface Tuning {
  preset?: Preset;
  ctx_size?: number;
  flash_attn?: Switch;
  kv_cache?: KvCache;
  parallel?: number;
  kv_unified?: boolean;
  batch?: number;
  ubatch?: number;
  spec?: Spec;
  draft_model?: string;
  draft_max?: number;
  draft_min?: number;
  reasoning?: Switch;
  reasoning_budget?: number;
  cache_ram_mb?: number;
  threads?: number;
  temperature?: number;
  top_p?: number;
  top_k?: number;
  min_p?: number;
  repeat_penalty?: number;
}

export interface Estimate { weights_gb: number; kv_gb: number; total_gb: number }

export interface PresetOption {
  id: Preset;
  label: string;
  explain: string;
  tuning: Tuning;
  estimate?: Estimate;
  fits?: boolean;
  warning?: string;
}

export interface TuningInfo {
  baked?: Tuning | null;
  saved?: Tuning | null;
  effective?: Tuning | null;
  source?: "baked" | "saved";
  presets: PresetOption[];
  model?: { id?: string; name?: string; trained_ctx?: number; vision?: boolean; draft?: string };
  engine?: { name?: string; mem_gb?: number };
}

export interface EstimateResult {
  tuning: Tuning;
  estimate?: Estimate;
  fits?: boolean;
  warning?: string;
  notes?: string[];
}

export interface BenchResult {
  label: string;
  tuning: Tuning;
  ok: boolean;
  error?: string;
  load_s?: number;
  prompt_tps?: number;
  gen_tps?: number;
  ttft_ms?: number;
  mem_gb?: number;
  draft_accept_rate?: number;
}

export interface BenchState {
  id?: string;
  state: "queued" | "running" | "done" | "error" | "canceled" | string;
  step?: number;
  total?: number;
  current?: string;
  error?: string;
  results: BenchResult[];
}

export const PRESET_IDS: Preset[] = ["speed", "balanced", "long_context", "quality"];
export const PRESET_LABEL: Record<Preset, string> = {
  speed: "Speed",
  balanced: "Balanced",
  long_context: "Long context",
  quality: "Quality",
  custom: "Custom",
};

/** Context windows offered in pickers, capped by the model's trained context. */
export const CTX_STEPS = [4096, 8192, 16384, 32768, 49152, 65536, 98304, 131072, 196608, 262144];
export function ctxOptions(trained = 0, current = 0): number[] {
  const list = CTX_STEPS.filter((n) => !trained || n <= trained);
  if (trained && !list.includes(trained) && trained > (list[list.length - 1] ?? 0)) list.push(trained);
  if (current && !list.includes(current)) list.push(current);
  return list.sort((a, b) => a - b);
}

/** 32768 → "32K", 131072 → "128K", 0/undefined → "—". */
export function ctxText(n: number | undefined): string {
  if (!n) return "—";
  return n % 1024 === 0 ? `${n / 1024}K` : n.toLocaleString();
}

/** Rough words for a token count (≈0.75 words per token). */
export function ctxWords(n: number | undefined): string {
  if (!n) return "";
  const w = Math.round((n * 0.75) / 1000) * 1000;
  return `About ${w.toLocaleString()} words`;
}

export const KV_LABEL: Record<KvCache, string> = { f16: "Full", q8_0: "Half", q4_0: "Quarter" };
export const SPEC_LABEL: Record<Spec, string> = { none: "Off", ngram: "From the chat", draft: "Draft model" };

/** Short phrases describing a tuning, for chips and preset cards. */
export function tuningChips(t: Tuning | null | undefined): string[] {
  if (!t) return [];
  const out: string[] = [];
  if (t.ctx_size) out.push(`${ctxText(t.ctx_size)} context`);
  if (t.kv_cache) out.push(t.kv_cache === "f16" ? "Full cache" : `${KV_LABEL[t.kv_cache]}-size cache`);
  if (t.spec) out.push(t.spec === "none" ? "No look-ahead" : t.spec === "draft" ? "Draft model look-ahead" : "Look-ahead on");
  if (t.parallel) out.push(t.parallel === 1 ? "1 request at a time" : `${t.parallel} requests${t.kv_unified ? " share it" : " at once"}`);
  if (t.reasoning === "on") out.push("Thinking on");
  else if (t.reasoning === "off") out.push("Thinking off");
  return out;
}

/** One line: "32K · half cache · look-ahead · 2 requests". */
export function tuningLine(t: Tuning | null | undefined): string {
  if (!t) return "";
  const out: string[] = [];
  if (t.ctx_size) out.push(ctxText(t.ctx_size));
  if (t.kv_cache) out.push(`${KV_LABEL[t.kv_cache].toLowerCase()} cache`);
  if (t.spec && t.spec !== "none") out.push("look-ahead");
  if (t.parallel && t.parallel > 1) out.push(`${t.parallel} requests`);
  if (t.reasoning === "on") out.push("thinking on");
  return out.join(" · ");
}

/** Drop unset fields so a saved tuning stays small and layers merge cleanly. */
export function clean(t: Tuning): Tuning {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(t)) {
    if (v === undefined || v === null || v === "" || (typeof v === "number" && !isFinite(v))) continue;
    out[k] = v;
  }
  return out as Tuning;
}

/** Same settings, ignoring the preset name. */
export function sameTuning(a: Tuning | null | undefined, b: Tuning | null | undefined): boolean {
  const strip = (t: Tuning | null | undefined) => {
    const { preset: _p, ...rest } = clean(t ?? {});
    return JSON.stringify(Object.keys(rest).sort().map((k) => [k, (rest as Record<string, unknown>)[k]]));
  };
  return strip(a) === strip(b);
}

/** "/tune" route argument for an image: "<engine>/<ref>". */
export function tuneArg(engine: string, ref: string): string {
  return `${engine === "podman" ? "podman" : "docker"}/${ref}`;
}
export function parseTuneArg(arg: string): { engine: string; ref: string } {
  const i = arg.indexOf("/");
  if (i < 0) return { engine: "docker", ref: arg };
  const engine = arg.slice(0, i);
  if (engine !== "docker" && engine !== "podman") return { engine: "docker", ref: arg };
  return { engine, ref: arg.slice(i + 1) };
}

const q = (o: Record<string, string>) => new URLSearchParams(o).toString();

export const getTuning = (ref: string, engine: string) => api<TuningInfo>(`/api/tuning?${q({ ref, engine })}`);
export const getPresets = (model: string, compute: string, engine: string) =>
  api<{ presets: PresetOption[]; model?: TuningInfo["model"]; engine?: TuningInfo["engine"] }>(`/api/tuning/presets?${q({ model, compute, engine })}`);
export const estimate = (body: { ref?: string; engine: string; model?: string; compute?: string; tuning: Tuning }) =>
  post<EstimateResult>("/api/tuning/estimate", body);
export const saveTuning = (ref: string, engine: string, tuning: Tuning) =>
  api<TuningInfo>("/api/tuning", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ref, engine, tuning: clean(tuning) }) });
export const clearTuning = (ref: string, engine: string) => api(`/api/tuning?${q({ ref, engine })}`, { method: "DELETE" });

export const startBench = (body: { ref: string; engine: string; compute: string; presets?: string[]; candidates?: Tuning[]; workload: "coding" | "chat" }) =>
  post<{ id: string }>("/api/tuning/benchmark", body);
export const benchState = (id: string) => api<BenchState>(`/api/tuning/benchmark/${encodeURIComponent(id)}`);
export const cancelBench = (id: string) => post(`/api/tuning/benchmark/${encodeURIComponent(id)}/cancel`, {});
export const applyBench = (id: string, index: number) => post(`/api/tuning/benchmark/${encodeURIComponent(id)}/apply`, { index });

/** Index of the fastest finished result by writing speed (then reading speed). -1 when none. */
export function fastest(results: BenchResult[]): number {
  let best = -1;
  results.forEach((r, i) => {
    if (!r.ok || r.gen_tps == null) return;
    const b = best >= 0 ? results[best] : null;
    if (!b || (r.gen_tps ?? 0) > (b.gen_tps ?? 0) || ((r.gen_tps ?? 0) === (b.gen_tps ?? 0) && (r.prompt_tps ?? 0) > (b.prompt_tps ?? 0))) best = i;
  });
  return best;
}
