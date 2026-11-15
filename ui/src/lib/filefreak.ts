// The FileFreak archive (System page): types matching internal/filefreak
// Status and JobView, and the calls to /api/filefreak/*. The upload key goes
// in through setKey and never comes back: the status carries only a hint.
// See docs/FILEFREAK.md.
import { api, post } from "./api";

export type ArchiveSource = "env" | "key" | "setting" | "built-in" | "none";
export type JobState = "queued" | "checking" | "uploading" | "done" | "skipped" | "failed";

export interface KeyFolder {
  id: string;
  name: string;
  username: string;
  url: string;
  public: boolean;
  checked_at: string;
}

export interface ArchiveJob {
  id: string;
  label: string;
  repo: string;
  revision: string;
  file: string;
  size: number;
  sent: number;
  state: JobState;
  reason: string;
  license: string;
  attempts: number;
  next_try?: string;
  url: string;
  queued_at: string;
  updated_at: string;
}

export interface ArchiveStatus {
  read_from_archive: boolean;
  key: { set: boolean; hint: string };
  archive: { url: string; source: ArchiveSource };
  archive_setting: string;
  env_url: string;
  default_url: string;
  key_folder: KeyFolder | null;
  helper: { ok: boolean; node: string; error: string };
  worker: { active: boolean; current: string };
  queue: ArchiveJob[];
  licenses: string[];
}

export interface ArchiveAnswer {
  status: ArchiveStatus;
  folder_error?: string;
}

export const archiveStatus = () => api<ArchiveAnswer>("/api/filefreak/status");
export const saveArchiveSettings = (s: { read_from_archive?: boolean; archive_url?: string }) =>
  post<ArchiveAnswer>("/api/filefreak/settings", s);
export const setArchiveKey = (key: string) => post<ArchiveAnswer>("/api/filefreak/key", { key });
export const removeArchiveKey = () => post<ArchiveAnswer>("/api/filefreak/key/remove", {});
export const checkArchiveFolder = () => post<ArchiveAnswer>("/api/filefreak/folder/check", {});
export const retryUpload = (id: string) => post<ArchiveAnswer>("/api/filefreak/queue/retry", { id });
export const removeUpload = (id: string) => post<ArchiveAnswer>("/api/filefreak/queue/remove", { id });

/** Where the archive location in use came from, in words. */
export const SOURCE_TEXT: Record<ArchiveSource, string> = {
  env: "from FILEFREAK_ARCHIVE_URL",
  key: "your upload key's folder",
  setting: "set here",
  "built-in": "built in",
  none: "",
};

/** A queue entry's state as a chip. */
export function jobChip(j: ArchiveJob): { text: string; cls: string } {
  switch (j.state) {
    case "uploading":
      return { text: "Uploading", cls: "pr" };
    case "checking":
      return { text: "Checking", cls: "pr" };
    case "done":
      return { text: "Archived", cls: "ok" };
    case "skipped":
      return { text: "Not archived", cls: "" };
    case "failed":
      return { text: "Failed", cls: "er" };
    default:
      return j.next_try && j.reason ? { text: "Will retry", cls: "" } : { text: "Queued", cls: "" };
  }
}

/** Whether Retry makes sense for an entry: it failed, or it waits for a later
 * try. A skipped one (license, gated, gone) would only be skipped again. */
export function canRetry(j: ArchiveJob): boolean {
  return j.state === "failed" || (j.state === "queued" && !!j.next_try && !!j.reason);
}

/** A server reason as a sentence: capitalized, with a full stop. */
export function sentence(s: string): string {
  s = s.trim();
  if (!s) return s;
  s = s[0].toUpperCase() + s.slice(1);
  return /[.!?]$/.test(s) ? s : s + ".";
}

/** "1.2 of 4.8 GB" for an upload in flight. */
export function sentText(j: ArchiveJob): string {
  const g = (n: number) => (n / 1e9).toFixed(n >= 1e10 ? 0 : 1);
  return j.size > 0 ? `${g(j.sent)} of ${g(j.size)} GB` : "";
}

/** The time of a later try, as "at 14:05" (today) or a date and time. */
export function whenText(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return "";
  const same = d.toDateString() === new Date().toDateString();
  return same
    ? `at ${d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`
    : d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
