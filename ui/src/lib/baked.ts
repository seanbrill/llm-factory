// The baked agent harness: tools a model image carries itself. llmgate, the
// image's front door, runs the tool loop, so any OpenAI-compatible client gets
// them without Open WebUI (docs/HARNESS.md, "Baked harness"). Here: the
// toolsets and their names, which catalog models can take one, the Build
// plan's per-build setting and the `harness` field it sends, and reading what
// an image has.
import { labelVal } from "./api";
import type { HarnessConfig, ImageHarness, ImageInfo, Model } from "./types";

export type Toolset = "web_search" | "fetch_url" | "files" | "skills" | "shell";

export interface ToolsetInfo {
  id: Toolset;
  label: string;  // as a setting: "Search the web"
  short: string;  // in a list: "web search"
  icon: string;
  hint: string;
}

export const TOOLSETS: ToolsetInfo[] = [
  { id: "web_search", label: "Search the web", short: "web search", icon: "globe", hint: "DuckDuckGo built in, or your own SearXNG." },
  { id: "fetch_url", label: "Read web pages", short: "web pages", icon: "doc", hint: "A page as text. Never your own network." },
  { id: "files", label: "Files in /workspace", short: "files", icon: "folder", hint: "List, read, write and search, inside /workspace only." },
  { id: "skills", label: "Skills", short: "skills", icon: "sparkle", hint: "The factory's skills/ folder, baked in read-only." },
  { id: "shell", label: "Run commands", short: "shell", icon: "terminal", hint: "Any command, run inside the container." },
];

const ORDER: Toolset[] = TOOLSETS.map((t) => t.id);

/** The toolsets on when tools are turned on (the shell never is). */
export const DEFAULT_TOOLS: Toolset[] = ["web_search", "fetch_url", "files", "skills"];

// The tool functions each toolset gives the model (internal/agent/config.go).
const SET_TOOLS: Record<Toolset, string[]> = {
  web_search: ["search_web"],
  fetch_url: ["fetch_url"],
  files: ["list_files", "read_file", "write_file", "replace_file_content", "glob_search", "grep_search"],
  skills: ["list_skills", "view_skill"],
  shell: ["run_command"],
};

/** Toolset or tool names -> the toolsets they turn on, in order. Unknown names are left out. */
export function toolsetsOf(list: readonly string[]): Toolset[] {
  const on = new Set<Toolset>();
  for (const name of list) {
    if ((ORDER as string[]).includes(name)) on.add(name as Toolset);
    else for (const t of ORDER) if (SET_TOOLS[t].includes(name)) on.add(t);
  }
  return ORDER.filter((t) => on.has(t));
}

export const toolsetInfo = (id: string): ToolsetInfo | undefined => TOOLSETS.find((t) => t.id === id);

/** ["web_search", "files", "shell"] -> "web search, files and shell". */
export function toolsText(tools: readonly string[]): string {
  const names = tools.map((t) => toolsetInfo(t)?.short ?? t);
  if (names.length <= 1) return names[0] ?? "no tools";
  return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

const SEARCH_NAME: Record<string, string> = { duckduckgo: "DuckDuckGo", searxng: "SearXNG", brave: "Brave" };
export const searchName = (backend: string | undefined): string => SEARCH_NAME[backend ?? ""] ?? backend ?? "";

// ---- which models ----------------------------------------------------------------

const CHAT = new Set(["text", "code", "reasoning", "vision"]);

/**
 * m can take a baked harness: a llama.cpp chat model (text, code, reasoning,
 * vision), not an embedding, image, video or audio model. The server says so
 * in /api/catalog (builder.CanHarness); this is the same rule for a catalog
 * that doesn't.
 */
export function canHarness(m: Model | null | undefined): boolean {
  if (!m) return false;
  if (typeof m.can_harness === "boolean") return m.can_harness;
  return (m.runtime ?? "cpp") === "cpp" && CHAT.has(m.modality ?? "text");
}

// ---- the Build plan's setting ------------------------------------------------------

export type SearchBackend = "duckduckgo" | "searxng" | "brave";

export interface PlanHarness {
  on: boolean;
  tools: Toolset[];
  search: SearchBackend;   // brave only comes from a cloned image (its key is set at run time)
  searxng_url: string;
  max_steps: number;
  max_calls_per_step: number;
  tool_timeout: number;    // seconds
  max_output_kb: number;   // KB of text per tool result
  base?: HarnessConfig;    // a cloned image's config: what the plan doesn't show is kept
}

/** The server's bounds (internal/agent/config.go), with the defaults. */
export const LIMITS = {
  max_steps: { def: 8, min: 1, max: 50, label: "Tool steps per answer" },
  max_calls_per_step: { def: 8, min: 1, max: 32, label: "Tool calls per step" },
  tool_timeout: { def: 60, min: 1, max: 1800, label: "Time limit per tool call" },
  max_output_kb: { def: 8, min: 0.5, max: 1024, label: "Text per tool result" },
} as const;

export function defaultHarness(on = false): PlanHarness {
  return {
    on,
    tools: [...DEFAULT_TOOLS],
    search: "duckduckgo",
    searxng_url: "",
    max_steps: LIMITS.max_steps.def,
    max_calls_per_step: LIMITS.max_calls_per_step.def,
    tool_timeout: LIMITS.tool_timeout.def,
    max_output_kb: LIMITS.max_output_kb.def,
  };
}

const num = (v: unknown, def: number) => (typeof v === "number" && isFinite(v) ? v : def);

/** A plan item's harness as saved in this browser (older plans have none). */
export function normHarness(v: unknown): PlanHarness {
  const d = defaultHarness(false);
  if (!v || typeof v !== "object") return d;
  const h = v as Partial<PlanHarness>;
  return {
    on: !!h.on,
    tools: Array.isArray(h.tools) ? toolsetsOf(h.tools.filter((t) => typeof t === "string")) : d.tools,
    search: h.search === "searxng" || h.search === "brave" ? h.search : "duckduckgo",
    searxng_url: typeof h.searxng_url === "string" ? h.searxng_url : "",
    max_steps: num(h.max_steps, d.max_steps),
    max_calls_per_step: num(h.max_calls_per_step, d.max_calls_per_step),
    tool_timeout: num(h.tool_timeout, d.tool_timeout),
    max_output_kb: num(h.max_output_kb, d.max_output_kb),
    base: h.base && typeof h.base === "object" ? h.base : undefined,
  };
}

/** "1m30s", "90s", "500ms", 90 -> seconds (Go durations, as the config label writes them). */
export function seconds(v: string | number | undefined): number {
  if (typeof v === "number") return v;
  if (!v) return 0;
  if (/^\s*[\d.]+\s*$/.test(v)) return parseFloat(v);
  const unit: Record<string, number> = { h: 3600, m: 60, s: 1, ms: 0.001, us: 1e-6, "µs": 1e-6, ns: 1e-9 };
  let total = 0;
  for (const [, n, u] of v.matchAll(/([\d.]+)(h|ms|m|s|us|µs|ns)/g)) total += parseFloat(n) * (unit[u] ?? 0);
  return Math.round(total * 1000) / 1000;
}

/** An image's baked config (GET /api/image/config "harness") as the plan's setting. */
export function harnessFromConfig(cfg: HarnessConfig | null | undefined): PlanHarness {
  if (!cfg) return defaultHarness(false);
  const d = defaultHarness(true);
  const l = cfg.limits ?? {};
  const b = cfg.search?.backend;
  return {
    on: true,
    tools: cfg.tools ? toolsetsOf(cfg.tools) : d.tools,
    search: b === "searxng" || b === "brave" ? b : "duckduckgo",
    searxng_url: cfg.search?.searxng_url ?? "",
    max_steps: l.max_steps || d.max_steps,
    max_calls_per_step: l.max_calls_per_step || d.max_calls_per_step,
    tool_timeout: seconds(l.tool_timeout) || d.tool_timeout,
    max_output_kb: l.max_output_bytes ? Math.round((l.max_output_bytes / 1024) * 100) / 100 : d.max_output_kb,
    base: cfg,
  };
}

const sameSet = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x) => b.includes(x));

export function validSearxng(url: string): boolean {
  try {
    const u = new URL(url.trim());
    return (u.protocol === "http:" || u.protocol === "https:") && !!u.host;
  } catch {
    return false;
  }
}

/** What would make the server refuse this setting ("" = nothing). */
export function harnessProblem(h: PlanHarness | undefined, m: Model | undefined): string {
  if (!h?.on || !canHarness(m)) return "";
  if (!h.tools.length) return "Tools are on, but none is picked. Pick at least one, or turn tools off.";
  if (h.tools.includes("web_search") && h.search === "searxng" && !validSearxng(h.searxng_url)) {
    return h.searxng_url.trim()
      ? "The SearXNG address has to start with http:// or https://."
      : "Add your SearXNG's address, or search with DuckDuckGo.";
  }
  for (const k of Object.keys(LIMITS) as (keyof typeof LIMITS)[]) {
    const v = h[k];
    const r = LIMITS[k];
    if (!(v >= r.min && v <= r.max)) return `${r.label} must be from ${r.min} to ${r.max}.`;
    if ((k === "max_steps" || k === "max_calls_per_step") && !Number.isInteger(v)) return `${r.label} must be a whole number.`;
  }
  return "";
}

/**
 * The `harness` field of POST /api/build: false for none, else the config to
 * bake. A cloned image's own config is the base, so fields the plan doesn't
 * show (instructions, shell settings, single tool names) survive the rebuild.
 */
export function harnessBody(h: PlanHarness | undefined, m: Model | undefined): HarnessConfig | false {
  if (!h?.on || !canHarness(m)) return false;
  const b = h.base ?? {};
  const tools = b.tools && sameSet(toolsetsOf(b.tools), h.tools) ? b.tools : ORDER.filter((t) => h.tools.includes(t));
  const url = h.search === "searxng" ? h.searxng_url.trim() : "";
  // Without web search the backend doesn't matter, but SearXNG still needs its address.
  const backend = h.search === "searxng" && !url ? "duckduckgo" : h.search;
  const search: HarnessConfig["search"] = { ...b.search, backend };
  if (url) search.searxng_url = url;
  else delete search.searxng_url;
  return {
    ...b,
    tools,
    limits: {
      ...b.limits,
      max_steps: h.max_steps,
      max_calls_per_step: h.max_calls_per_step,
      tool_timeout: `${h.tool_timeout}s`,
      max_output_bytes: Math.round(h.max_output_kb * 1024),
    },
    search,
  };
}

// ---- what an image has ---------------------------------------------------------------

const TOOLS_LABEL = /(?:^|,)local-llm\.harness_tools=((?:web_search|fetch_url|files|skills|shell)(?:,(?:web_search|fetch_url|files|skills|shell))*)/;

/**
 * The harness baked into an image: /api/images sends it as `Harness`. An
 * older factory only has the labels; the tools label holds commas, so it's
 * read by pattern from Docker's joined Labels string.
 */
export function imageHarness(im: ImageInfo): ImageHarness | null {
  if (im.Harness) return im.Harness;
  if (labelVal(im.Labels, "local-llm.harness") !== "1") return null;
  const raw = typeof im.Labels === "object" && im.Labels ? im.Labels["local-llm.harness_tools"] ?? "" : (String(im.Labels ?? "").match(TOOLS_LABEL)?.[1] ?? "");
  const tools = toolsetsOf(raw.split(","));
  return { tools, max_steps: 0, workspace: tools.includes("files") || tools.includes("shell") };
}
