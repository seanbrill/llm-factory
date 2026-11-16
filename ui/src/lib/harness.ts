// Agent harness API (/api/harness/*): types and calls. See docs/HARNESS.md.
import { api, post, put, del } from "./api";

export interface GatewayModel {
  id: string;
  name: string;
  base_id: string;
  port: number;
  ref: string;
  modality: string;
  engine: string;
  container: string;
  persona?: string;
  created: number;
}

export interface GatewayStatus {
  base_url: string; // as Open WebUI reaches it
  local_url: string; // from this computer
  port: number;
  key: string;
  models: GatewayModel[];
  modalities: string[];
  error?: string;
}

export interface TerminalVariant {
  id: string;
  label: string;
  size: string;
  desc: string;
  memory_mb: number;
}

export interface HelperContainer {
  engine: string;
  exists: boolean;
  running: boolean;
  status: string;
  image: string;
  started_at?: string;
  exit_code: number;
  oom_killed: boolean;
}

export interface TerminalOp {
  active: boolean;
  phase: string; // starting | pulling | creating | waiting | running | error | canceled
  message: string;
  lines: string[];
  error?: string;
  detail?: string;
  elapsed_ms: number;
}

export interface Mount {
  host: string;
  container: string;
  mode: string;
}

export interface TerminalStatus {
  variant: string;
  port: number;
  workspace: string; // as configured ("" = default)
  share_claude_skills: boolean;
  memory_mb: number;
  variants: TerminalVariant[];
  image: string;
  image_present: boolean;
  engine: string;
  container: HelperContainer | null;
  healthy: boolean;
  restart_required: boolean;
  spec_error?: string;
  op: TerminalOp;
  url: string;
  local_url: string;
  key: string;
  workspace_path: string;
  default_workspace: string;
  skills_path: string;
  claude_skills_path: string;
  error?: string;
  mounts: Mount[] | null;
}

export interface Skill {
  name: string;
  description: string;
  dir: string;
  lines: number;
  listed: boolean;
  warnings?: string[];
}

export interface SkillsStatus {
  path: string;
  items: Skill[];
  error?: string;
}

export interface OpenWebUIPrefs {
  url: string;
  default_url: string;
  reach_host: string;
}

export interface HarnessStatus {
  config_path: string;
  config_error?: string;
  in_container: boolean;
  gateway: GatewayStatus;
  terminal: TerminalStatus;
  skills: SkillsStatus;
  openwebui: OpenWebUIPrefs;
}

export type ItemStatus = "ok" | "added" | "updated" | "missing" | "outdated" | "error" | "skipped" | "info";

export interface ReportItem {
  area: string; // openwebui | models | terminal | web_search
  status: ItemStatus;
  detail: string;
}

export interface WebSearchState {
  enabled: boolean;
  engine: string;
}

export interface ConnectReport {
  reachable: boolean;
  admin: boolean;
  version?: string;
  changed: boolean;
  items: ReportItem[];
  web_search?: WebSearchState;
}

export interface ConnectResult {
  report: ConnectReport;
  url: string;
  gateway_url: string;
  terminal_url: string;
}

export interface TerminalSettings {
  variant?: string;
  port?: number;
  workspace?: string;
  share_claude_skills?: boolean;
  memory_mb?: number;
}

export interface OpenWebUIRequest {
  url: string;
  api_key: string; // sent for this call only; the factory never stores it
  reach_host?: string;
  models?: boolean;
  terminal?: boolean;
  web_search?: boolean;
}

export const getHarnessStatus = () => api<HarnessStatus>("/api/harness/status");
export const saveTerminalSettings = (s: TerminalSettings) =>
  post<{ status: string; workspace_path: string }>("/api/harness/terminal/config", s);
export const startTerminal = (update = false) => post<TerminalOp>("/api/harness/terminal/start", { update });
export const stopTerminal = (remove = false) => post<{ status: string }>("/api/harness/terminal/stop", { remove });
export const terminalLogs = (tail = 300) =>
  api<{ logs: string; note?: string }>(`/api/harness/terminal/logs?tail=${tail}`);
export const regenerateGatewayKey = () => post<{ key: string; note: string }>("/api/harness/gateway/key", {});
export const regenerateTerminalKey = () => post<{ key: string; note: string }>("/api/harness/terminal/key", {});
export const checkOpenWebUI = (r: OpenWebUIRequest) => post<ConnectResult>("/api/harness/openwebui/check", r);
export const connectOpenWebUI = (r: OpenWebUIRequest) => post<ConnectResult>("/api/harness/openwebui/connect", r);

// ---- MCP servers (/api/harness/mcp) ---------------------------------------------
// Tool servers a model can use when the factory runs it. The factory keeps each
// server's URL and headers and only ever sends them back masked (the URL's path
// and the header values are secrets). Every allowed tool's name, description and
// input schema go with each request, so each one costs context.

export interface McpTool {
  name: string;
  description: string;
  /** Estimated schema cost: bytes of name + description + inputSchema / 4. */
  tokens: number;
}

export interface McpServer {
  /** [a-z0-9_-], 1–32 characters. */
  name: string;
  /** Masked for display, e.g. "http://127.0.0.1:64182/…" (the path is a secret). */
  url: string;
  /** Header names -> masked values ("••••••"). */
  headers: Record<string, string>;
  /** The allowlist: null = every tool the server offers; [] = none picked yet. */
  tools: string[] | null;
  /** The user has edited the allowlist. */
  tools_chosen: boolean;
  /** The server's tools as of the last successful test ([] = never tested). */
  catalog: McpTool[];
  catalog_tokens: number;
  /** Tokens of the allowed tools, sent in full. */
  selected_tokens: number;
  /** How the tools reach the model: auto (on demand above MCP_ON_DEMAND_OVER tokens), full or on_demand. */
  load: McpLoad;
  /** Tools always sent in full, even on demand. */
  pinned: string[];
  /** The ticked tools reach the model as a one-line catalog (tool_info, call_tool). */
  on_demand: boolean;
  /** What the ticked tools cost each request (catalog lines and pinned schemas when on demand). */
  request_tokens: number;
  /** RFC3339 time of the last test. */
  tested_at?: string;
  /** The last test's error. */
  error?: string;
  /** The URL points at this computer (127.0.0.1 / localhost). */
  loopback: boolean;
}

export interface McpTest {
  ok: boolean;
  tools: McpTool[];
  total_tokens: number;
  error?: string;
  /** The server after the test (its catalog is refreshed). */
  server?: McpServer;
}

export interface McpList {
  servers: McpServer[];
  /** A new server with more tools than this starts with none picked. */
  many_tools: number;
}

export interface McpAdd {
  name: string;
  url: string;
  headers?: Record<string, string>;
}

/** A change: leave a field out to keep it. `headers` replaces the map; MCP_MASK keeps a value. */
export interface McpUpdate {
  url?: string;
  headers?: Record<string, string>;
  /** null = every tool, [] = none, a list = those. */
  tools?: string[] | null;
  load?: McpLoad;
  pinned?: string[];
}

export type McpLoad = "auto" | "full" | "on_demand";
/** An "auto" server goes on demand when its ticked tools' schemas cost more than this (internal/agent OnDemandOver). */
export const MCP_ON_DEMAND_OVER = 3000;
/** tool_info's and call_tool's schemas and the catalog heading (internal/agent OnDemandBaseTokens). */
export const MCP_ON_DEMAND_BASE = 227;

/** A tool's catalog line in tokens, as internal/agent CatalogLineTokens estimates it. */
export function catalogLineTokens(server: string, tool: string, desc: string): number {
  let d = (desc || "").split(/\s+/).filter(Boolean).join(" ");
  const i = d.indexOf(". ");
  if (i > 0 && i < 110) d = d.slice(0, i + 1);
  else if (new TextEncoder().encode(d).length > 110) d = d.slice(0, 110) + "…";
  const line = `- ${server}__${tool}${d ? ": " + d : ""}`;
  return Math.ceil((new TextEncoder().encode(line).length + 1) / 3);
}

/** What a server's ticked tools cost per request under a load mode and pins. */
export function mcpCost(server: string, tools: McpTool[], load: McpLoad, pinned: Set<string>): { onDemand: boolean; full: number; request: number } {
  const full = tools.reduce((n, t) => n + (t.tokens || 0), 0);
  const onDemand = load === "on_demand" || (load !== "full" && full > MCP_ON_DEMAND_OVER);
  if (!onDemand) return { onDemand, full, request: full };
  const request = tools.reduce((n, t) => n + (pinned.has(t.name) ? t.tokens || 0 : catalogLineTokens(server, t.name, t.description)), 0);
  return { onDemand, full, request };
}

/** A stored header value as the factory shows it. Sent back, it keeps that value. */
export const MCP_MASK = "••••••";
/** A server name: lowercase letters, digits, - and _, up to 32. */
export const MCP_NAME = /^[a-z0-9_-]{1,32}$/;
/** Tool schemas above this (a quarter of a 32K context) get a warning. */
export const MCP_TOKENS_WARN = 8192;

const mcpPath = (name: string) => `/api/harness/mcp/${encodeURIComponent(name)}`;
export const listMcp = () => api<McpList>("/api/harness/mcp");
/** Adds a server and tests it right away. */
export const addMcp = (s: McpAdd) => post<{ server: McpServer; test: McpTest }>("/api/harness/mcp", s);
export const updateMcp = (name: string, u: McpUpdate) => put<{ server: McpServer }>(mcpPath(name), u);
export const deleteMcp = (name: string) => del<{ status: string }>(mcpPath(name));
/** Asks the server for its tools; refreshes the stored catalog. */
export const testMcp = (name: string) => post<McpTest>(`${mcpPath(name)}/test`, {});

/** The catalog tools a server gives a model now (its allowlist; null = all). */
export function mcpAllowed(s: McpServer): McpTool[] {
  const cat = s.catalog ?? [];
  if (s.tools == null) return cat;
  const on = new Set(s.tools);
  return cat.filter((t) => on.has(t.name));
}

/** A server's last test for its chip: "12 tools", "Test failed" or "Not tested". */
export function mcpState(s: McpServer): { label: string; cls: "ok" | "er" | "" } {
  if (s.error) return { label: "Test failed", cls: "er" };
  if (s.tested_at) {
    const n = s.catalog?.length ?? 0;
    return { label: `${n} tool${n === 1 ? "" : "s"}`, cls: "ok" };
  }
  return { label: "Not tested", cls: "" };
}

/** Estimated tokens as "≈1,240". */
export function tokText(n: number): string {
  return `≈${Math.round(n || 0).toLocaleString()}`;
}

export const AREA_LABEL: Record<string, string> = {
  openwebui: "Open WebUI",
  models: "Models",
  terminal: "Terminal",
  web_search: "Web search",
};

export type Tone = "ok" | "warn" | "err" | "idle" | "busy";

// terminalState summarizes the terminal for the status pill.
export function terminalState(t: TerminalStatus): { label: string; tone: Tone } {
  if (t.op.active) return { label: t.op.message || "Starting…", tone: "busy" };
  const c = t.container;
  if (!c || !c.exists) return { label: "Not created", tone: "idle" };
  if (c.running && t.healthy) return { label: "Running", tone: "ok" };
  if (c.running) return { label: "Running · not answering yet", tone: "warn" };
  if (c.oom_killed) return { label: "Stopped · out of memory", tone: "err" };
  return { label: c.status === "exited" ? "Stopped" : c.status || "Stopped", tone: "idle" };
}

export function itemTone(s: ItemStatus): Tone {
  switch (s) {
    case "ok":
    case "added":
    case "updated":
      return "ok";
    case "missing":
    case "outdated":
      return "warn";
    case "error":
      return "err";
    default:
      return "idle";
  }
}

export function maskKey(k: string): string {
  if (!k) return "";
  return k.length <= 12 ? "••••••" : `${k.slice(0, 8)}••••••••${k.slice(-4)}`;
}

// ---- Harness page helpers (redesign, sheet frame f12) --------------------------

/** Chip modifier class for a tone. */
export const TONE_CHIP: Record<Tone, string> = { ok: "ok", warn: "wa", err: "er", busy: "pr", idle: "" };

/** A start in progress, in a few words (TerminalOp.phase). */
const OP_PHASE: Record<string, string> = {
  starting: "Starting…",
  pulling: "Downloading the image…",
  creating: "Creating the container…",
  waiting: "Waiting for it to answer…",
};

/**
 * The terminal in a few words, for its chip and the setup step: what it's doing
 * now and whether that needs you. `hint` is the step's second line.
 */
export function terminalView(t: TerminalStatus): { label: string; tone: Tone; hint: string } {
  const c = t.container;
  if (t.op?.active) return { label: "Starting", tone: "busy", hint: OP_PHASE[t.op.phase] ?? "Starting…" };
  if (c?.running && t.healthy) return { label: "Running", tone: "ok", hint: `Running · port ${t.port}` };
  if (c?.running) return { label: "Not answering", tone: "warn", hint: "Running, but not answering yet" };
  if (t.op?.error) return { label: "Didn't start", tone: "err", hint: "Didn't start · see why below" };
  if (c?.oom_killed) return { label: "Out of memory", tone: "err", hint: "Stopped: it ran out of memory" };
  if (!c?.exists) {
    return { label: "Not created", tone: "idle", hint: t.image_present ? "Not created yet · start it to edit files" : "Not downloaded yet · start it to edit files" };
  }
  return { label: "Stopped", tone: "idle", hint: "Stopped · start it to edit files" };
}

/** What the start button says: the first start downloads the image. */
export function startLabel(t: TerminalStatus): string {
  if (t.container?.exists) return "Start the terminal";
  if (t.image_present) return "Create and start the terminal";
  const size = t.variants?.find((v) => v.id === t.variant)?.size;
  return size ? `Download (${size}) and start` : "Download and start the terminal";
}

/** The skills Open Terminal will list, and the ones that need a fix. */
export function skillCounts(s: SkillsStatus | undefined): { found: number; listed: number; fix: number } {
  const items = s?.items ?? [];
  return {
    found: items.length,
    listed: items.filter((i) => i.listed).length,
    fix: items.filter((i) => !i.listed || (i.warnings?.length ?? 0) > 0).length,
  };
}

/** A message from the factory as a sentence: "gave up after 5m0s" -> "Gave up after 5m0s." */
export function sentence(s: string | undefined): string {
  const t = (s || "").trim();
  if (!t) return "";
  const first = /^[a-z]/.test(t) && !/^[a-z]+:\/\//.test(t) && !/^[a-z0-9_.-]+\//.test(t) ? t[0].toUpperCase() + t.slice(1) : t;
  return /[.!?…]$/.test(first) ? first : first + ".";
}

/** "Reads files. Use it to…" -> "Reads files." (the description's first sentence). */
export function firstSentence(s: string): string {
  const m = (s || "").match(/^.+?[.!?](?=\s|$)/);
  return (m ? m[0] : s || "").trim();
}

/** The host home folder, read off ~/.claude/skills ("" when the factory doesn't know it). */
export function homeOf(t: TerminalStatus | undefined): string {
  const p = t?.claude_skills_path || "";
  return p.replace(/[\\/]\.claude[\\/]skills[\\/]?$/, "");
}

/** "/Users/you/llm-factory/workspace" -> "~/llm-factory/workspace" when under home. */
export function tildePath(p: string, home: string): string {
  if (!p) return "";
  if (home && (p === home || p.startsWith(home + "/") || p.startsWith(home + "\\"))) return "~" + p.slice(home.length);
  return p;
}

/** Megabytes as the settings show them: 2048 -> "2 GB", 1536 -> "1.5 GB", 900 -> "900 MB". */
export function mbText(mb: number): string {
  if (!mb) return "—";
  if (mb < 1024) return `${mb} MB`;
  const g = mb / 1024;
  return `${Number.isInteger(g) ? g : g.toFixed(1)} GB`;
}

/**
 * Open WebUI's address for a link in this browser. The factory reaches it through
 * host.docker.internal; from the browser it's the machine the factory runs on.
 */
export function browserURL(u: string, host = typeof location !== "undefined" ? location.hostname : "localhost"): string {
  try {
    const url = new URL(u);
    if (url.hostname === "host.docker.internal" || url.hostname === "host.containers.internal") url.hostname = host;
    return url.toString().replace(/\/$/, "");
  } catch {
    return "";
  }
}

/** Status of one area (models, terminal, web_search) in a connector report. */
export function areaTone(items: ReportItem[], area: string): Tone | null {
  const mine = items.filter((i) => i.area === area);
  if (!mine.length) return null;
  if (mine.some((i) => i.status === "error")) return "err";
  if (mine.some((i) => i.status === "missing" || i.status === "outdated")) return "warn";
  if (mine.some((i) => i.status === "ok" || i.status === "added" || i.status === "updated")) return "ok";
  return "idle"; // skipped / info only
}

export interface Verdict {
  tone: Tone;
  chip: string; // the card's chip
  line: string; // one sentence
}

/** One verdict for a whole Check/Connect report. */
export function reportVerdict(r: ConnectReport): Verdict {
  const errors = r.items.filter((i) => i.status === "error").length;
  const gaps = r.items.filter((i) => i.status === "missing" || i.status === "outdated").length;
  if (!r.reachable) return { tone: "err", chip: "Can't reach it", line: "The factory couldn't reach Open WebUI at that address." };
  if (!r.admin) return { tone: "err", chip: "Key refused", line: "Open WebUI answered, but didn't accept the key as an admin's." };
  if (errors) return { tone: "err", chip: "Needs a fix", line: `${errors} part${errors > 1 ? "s" : ""} failed. The list below says why.` };
  if (gaps) return { tone: "warn", chip: "Not set up", line: `${gaps} thing${gaps > 1 ? "s are" : " is"} missing. Connect adds ${gaps > 1 ? "them" : "it"}.` };
  return { tone: "ok", chip: "Set up", line: "Models, terminal and web search are set up." };
}

// The last Check/Connect result, kept in this browser so the page can say what
// it found next time (Open WebUI keeps no record the factory could read back).
// The admin key is never part of it.
export interface OwuiMemo {
  at: number; // unix ms
  kind: "check" | "connect";
  url: string;
  report: ConnectReport;
  fp: string; // fingerprint of the gateway/terminal URLs and keys it was set up with
}

const MEMO_KEY = "llmfactory.owui.v1";

export function loadOwuiMemo(): OwuiMemo | null {
  try {
    const raw = localStorage.getItem(MEMO_KEY);
    const m = raw ? JSON.parse(raw) : null;
    return m && typeof m.at === "number" && m.report && Array.isArray(m.report.items) ? (m as OwuiMemo) : null;
  } catch {
    return null;
  }
}

export function saveOwuiMemo(m: OwuiMemo | null) {
  try {
    if (m) localStorage.setItem(MEMO_KEY, JSON.stringify(m));
    else localStorage.removeItem(MEMO_KEY);
  } catch {
    /* private mode: the memo just won't survive a reload */
  }
}

/**
 * A short fingerprint (FNV-1a, not a secret) of what Open WebUI was given: when
 * a key is regenerated or a URL changes, the saved setup no longer matches.
 */
export function setupFingerprint(st: HarnessStatus): string {
  const s = [st.gateway?.base_url, st.gateway?.key, st.terminal?.url, st.terminal?.key].join("\u0000");
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0");
}

/** "today at 9:41 PM", "yesterday at 8:02 AM", "Nov 3 at 4:10 PM". */
export function whenText(ms: number, now = Date.now()): string {
  const d = new Date(ms);
  const time = d.toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
  const day = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(new Date(now)) - day(d)) / 86400000);
  if (diff === 0) return `today at ${time}`;
  if (diff === 1) return `yesterday at ${time}`;
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} at ${time}`;
}

// copyText copies to the clipboard, falling back to a hidden textarea where the
// async clipboard API isn't available (plain-http LAN access).
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through */
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  document.body.removeChild(ta);
  return ok;
}
