// Saved prompts for benchmarks (internal/server/prompts.go): the same prompt
// sent to different models, or one model built or tuned differently, with
// each answer's speed and text kept so they can be compared later. The
// Playground's Prompts menu inserts and saves them, and records a result when
// a saved prompt opens a conversation; the Prompts page runs a prompt on
// several models in turn.
import { api, post, put, del, streamChat, type StreamOpts } from "./api";
import type { Stats, Target } from "./chat.svelte";

export interface SavedPrompt {
  id: string;
  name: string;
  text: string;
  system?: string;
  tags?: string[];
  expect?: string;          // what a good answer has (for the reader, not checked)
  temperature?: number | null;
  top_p?: number | null;
  created: string;
  updated: string;
}

export type PromptDraft = Omit<SavedPrompt, "id" | "created" | "updated">;

export interface PromptResult {
  id: string;
  prompt_id: string;
  batch?: string;
  model: string;
  model_id?: string;
  ref?: string;
  engine?: string;
  compute?: string;
  temperature?: number | null;
  top_p?: number | null;
  ms: number;
  ttft?: number | null;
  tokens?: number;
  tps?: number | null;
  output?: string;
  output_bytes?: number;
  reasoning_bytes?: number;
  error?: string;
  source?: "playground" | "bench";
  at: string;
}

export type NewResult = Omit<PromptResult, "id" | "at">;

export const listPrompts = () => api<{ prompts: SavedPrompt[] }>("/api/prompts").then((r) => r.prompts);
export const createPrompt = (p: PromptDraft) => post<SavedPrompt>("/api/prompts", p);
export const importPrompts = (ps: Partial<SavedPrompt>[]) => post<{ prompts: SavedPrompt[] }>("/api/prompts", { prompts: ps }).then((r) => r.prompts);
export const updatePrompt = (id: string, p: PromptDraft) => put<SavedPrompt>(`/api/prompts/${encodeURIComponent(id)}`, p);
export const deletePrompt = (id: string) => del(`/api/prompts/${encodeURIComponent(id)}`);
export const listResults = (promptId?: string) =>
  api<{ results: PromptResult[] }>("/api/prompts/results" + (promptId ? `?prompt=${encodeURIComponent(promptId)}` : "")).then((r) => r.results);
export const recordResult = (r: NewResult) => post<PromptResult>("/api/prompts/results", r);
export const deleteResult = (id: string) => del(`/api/prompts/results/${encodeURIComponent(id)}`);

/** Models a text prompt can go to. */
export const CHAT_MODS = new Set(["text", "vision", "ensemble"]);

/** The sampling a saved prompt asks for, over the caller's defaults. */
export function promptSampling(p: SavedPrompt, base: { temperature: number; top_p: number; seed?: number | null }): StreamOpts {
  const o: StreamOpts = {
    temperature: p.temperature ?? base.temperature,
    top_p: p.top_p ?? base.top_p,
  };
  if (base.seed != null) o.seed = base.seed;
  return o;
}

/** The messages a saved prompt opens a conversation with. */
export function promptMessages(p: SavedPrompt, fallbackSystem = ""): unknown[] {
  const sys = (p.system ?? "").trim() || fallbackSystem.trim();
  const out: unknown[] = [];
  if (sys) out.push({ role: "system", content: sys });
  out.push({ role: "user", content: p.text });
  return out;
}

/** What one run of a prompt measured and answered. */
export interface RunOutcome {
  stats: Stats;
  output: string;
  reasoning: number;
  error?: string;
  stopped?: boolean;
}

/** Send one prompt to one model and measure it the way the Playground does. */
export async function runPrompt(
  t: Target,
  messages: unknown[],
  opts: StreamOpts,
  signal: AbortSignal,
  onProgress?: (o: RunOutcome) => void,
): Promise<RunOutcome> {
  const t0 = performance.now();
  let first = 0;
  let n = 0;
  const o: RunOutcome = { stats: { ms: 0 }, output: "", reasoning: 0 };
  try {
    await streamChat(
      t.port,
      messages,
      (d, kind) => {
        const now = performance.now();
        n++;
        if (!first) first = now;
        if (kind === "reasoning") o.reasoning += d.length;
        else o.output += d;
        o.stats = statsOf(t0, first, now, n);
        onProgress?.(o);
      },
      signal,
      opts,
    );
    if (!o.output && !o.reasoning) o.error = "an empty reply";
  } catch (e) {
    if ((e as Error).name === "AbortError") o.stopped = true;
    else o.error = (e as Error)?.message || "no answer";
  }
  if (!first) o.stats = { ms: performance.now() - t0 };
  return o;
}

function statsOf(t0: number, first: number, now: number, n: number): Stats {
  return { ms: now - t0, ttft: first - t0, tokens: n, tps: n > 1 && now > first ? ((n - 1) * 1000) / (now - first) : null };
}

/** The result to record for a run. */
export function resultOf(p: SavedPrompt, t: Target, opts: StreamOpts, o: RunOutcome, source: "playground" | "bench", batch?: string): NewResult {
  return {
    prompt_id: p.id,
    batch,
    model: t.name,
    model_id: t.model || undefined,
    ref: t.ref || undefined,
    engine: t.engine,
    compute: t.compute,
    temperature: typeof opts.temperature === "number" ? opts.temperature : null,
    top_p: typeof opts.top_p === "number" ? opts.top_p : null,
    ms: Math.round(o.stats.ms),
    ttft: o.stats.ttft != null ? Math.round(o.stats.ttft) : null,
    tokens: o.stats.tokens,
    tps: o.stats.tps != null ? Math.round(o.stats.tps * 10) / 10 : null,
    output: o.output,
    reasoning_bytes: o.reasoning || undefined,
    error: o.error,
    source,
  };
}

/** A model's label in results: the name, plus the image tag when it isn't "latest". */
export function modelLabel(r: Pick<PromptResult, "model" | "ref" | "compute">): string {
  const tag = r.ref?.includes(":") ? r.ref.split(":").pop() : "";
  return tag && tag !== "latest" ? `${r.model} (${tag})` : r.model;
}

/** Results per model: medians over their runs, fastest first. */
export interface ModelSummary {
  key: string;
  label: string;
  runs: number;
  failed: number;
  ttft: number | null;
  tps: number | null;
  ms: number | null;
  tokens: number | null;
  last: PromptResult;
}

const median = (xs: number[]) => {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};

export function summarize(results: PromptResult[]): ModelSummary[] {
  const by = new Map<string, PromptResult[]>();
  for (const r of results) {
    const k = `${r.ref || r.model}|${r.engine ?? ""}|${r.compute ?? ""}`;
    by.set(k, [...(by.get(k) ?? []), r]);
  }
  const out: ModelSummary[] = [];
  for (const [key, rs] of by) {
    const ok = rs.filter((r) => !r.error);
    const nums = (f: (r: PromptResult) => number | null | undefined) => ok.map(f).filter((x): x is number => typeof x === "number");
    const last = [...rs].sort((a, b) => (a.at < b.at ? 1 : -1))[0];
    out.push({
      key,
      label: modelLabel(last),
      runs: rs.length,
      failed: rs.length - ok.length,
      ttft: median(nums((r) => r.ttft)),
      tps: median(nums((r) => r.tps)),
      ms: median(nums((r) => r.ms)),
      tokens: median(nums((r) => r.tokens)),
      last,
    });
  }
  return out.sort((a, b) => (b.tps ?? -1) - (a.tps ?? -1));
}

/** Download prompts as a JSON file. */
export function exportPrompts(ps: SavedPrompt[]) {
  const blob = new Blob([JSON.stringify({ prompts: ps }, null, 2) + "\n"], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `prompts-${new Date().toISOString().slice(0, 10)}.json`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
}

/** Read an exported file: {prompts: [...]} or a bare list. */
export async function readPromptFile(f: File): Promise<Partial<SavedPrompt>[]> {
  const data = JSON.parse(await f.text());
  const list = Array.isArray(data) ? data : data?.prompts;
  if (!Array.isArray(list)) throw new Error("that file has no prompts in it");
  return list.filter((p) => p && typeof p.text === "string");
}

/** A shared, loaded-once list for the Playground menu and the Prompts page. */
export const promptState = $state({ list: [] as SavedPrompt[], loaded: false, error: "" });

export async function refreshPrompts() {
  try {
    promptState.list = await listPrompts();
    promptState.error = "";
  } catch (e) {
    promptState.error = (e as Error).message;
  }
  promptState.loaded = true;
}
