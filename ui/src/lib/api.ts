// Typed fetch client + SSE stream helper for the Go API. Mirrors the vanilla
// app's api()/streamChat() but with types and one place for error handling.
import type { MediaItem } from "./types";

// ApiError carries the HTTP status + parsed body so callers can react to
// structured responses (e.g. the run guardrail's 409 + { code, needs_force }).
export interface ApiError extends Error {
  status: number;
  data: any;
}

// Called when the factory answers 401 auth_required: the page lacks the
// factory's access token (see lib/auth.svelte.ts). Set by the auth module, so
// this file needn't import it.
let onAuthRequired: (() => void) | null = null;
export function setAuthRequiredHandler(fn: () => void) {
  onAuthRequired = fn;
}

export async function api<T = any>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, init);
  const text = await res.text();
  let data: any;
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { error: text };
  }
  if (res.status === 401 && data?.code === "auth_required") onAuthRequired?.();
  if (!res.ok) {
    const err = new Error(data?.error || res.statusText) as ApiError;
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data as T;
}

export function post<T = any>(path: string, body: unknown): Promise<T> {
  return api<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
}

export interface StreamOpts {
  temperature?: number;
  top_p?: number;
  seed?: number;
  max_tokens?: number;
  stop?: string[];
  [k: string]: unknown;
}

/** What an ensemble's stream carries besides tokens (cmd/ensemblegate). */
export interface StreamExtra {
  /** Generated images, clips or speech in the reply (delta.media). */
  onMedia?: (items: MediaItem[]) => void;
  /** A ": ensemble: <sentence>" comment while a member warms up or media generates. */
  onStatus?: (text: string) => void;
}

// streamChat POSTs to /api/chat/stream and calls onDelta for each token. Returns
// when the stream ends; throws on a non-OK response or an error event in the
// stream (data: {"error": {...}}).
export async function streamChat(
  port: number,
  messages: unknown[],
  onDelta: (s: string, kind?: "content" | "reasoning") => void,
  signal?: AbortSignal,
  opts?: StreamOpts,
  extra?: StreamExtra,
): Promise<void> {
  const body = { port, messages, max_tokens: 1024, temperature: 0.4, ...(opts ?? {}) };
  const res = await fetch("/api/chat/stream", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    signal,
  });
  if (!res.ok || !res.body) {
    let msg = res.statusText;
    try {
      const data = JSON.parse(await res.text());
      if (res.status === 401 && data?.code === "auth_required") onAuthRequired?.();
      msg = data.error || msg;
    } catch {
      /* keep statusText */
    }
    throw new Error(msg);
  }
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line.startsWith(":")) {
        // An SSE comment: keep-alive, or an ensemble saying what it's doing.
        const m = line.match(/^:\s*ensemble:\s*(.+)$/);
        if (m) extra?.onStatus?.(m[1].trim());
        continue;
      }
      if (!line.startsWith("data:")) continue;
      const d = line.slice(5).trim();
      if (d === "[DONE]") return;
      let obj: any;
      try {
        obj = JSON.parse(d);
      } catch {
        continue; /* ignore partial */
      }
      if (obj?.error) {
        const e = obj.error;
        throw new Error(typeof e === "string" ? e : e.message || "the model sent an error");
      }
      // Reasoning models (e.g. Qwen3 via llama.cpp with a reasoning format) stream
      // their thinking as delta.reasoning_content and the answer as delta.content —
      // handle both, or a reasoning model looks like it never replied.
      const delta = obj?.choices?.[0]?.delta;
      if (delta?.reasoning_content) onDelta(delta.reasoning_content, "reasoning");
      if (delta?.content) onDelta(delta.content, "content");
      if (Array.isArray(delta?.media) && delta.media.length) extra?.onMedia?.(delta.media as MediaItem[]);
    }
  }
}

// labelVal reads a key from a docker comma-joined "k=v,k=v" Labels string (or a
// podman object), matching the Go labelValue helper.
export function labelVal(labels: string | Record<string, string> | undefined, key: string): string {
  if (!labels) return "";
  if (typeof labels === "object") return labels[key] ?? "";
  for (const part of String(labels).split(",")) {
    const eq = part.indexOf("=");
    if (eq > 0 && part.slice(0, eq) === key) return part.slice(eq + 1);
  }
  return "";
}
