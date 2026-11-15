// Agent harness API (/api/harness/*): types and calls. See docs/HARNESS.md.
import { api, post } from "./api";

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
