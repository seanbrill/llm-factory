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
