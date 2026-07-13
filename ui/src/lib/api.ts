// Typed fetch client + SSE stream helper for the Go API. Mirrors the vanilla
// app's api()/streamChat() but with types and one place for error handling.

// ApiError carries the HTTP status + parsed body so callers can react to
// structured responses (e.g. the run guardrail's 409 + { code, needs_force }).
export interface ApiError extends Error {
  status: number;
  data: any;
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

// streamChat POSTs to /api/chat/stream and calls onDelta for each token. Returns
// when the stream ends; throws on a non-OK response.
export async function streamChat(
  port: number,
  messages: unknown[],
  onDelta: (s: string, kind?: "content" | "reasoning") => void,
  signal?: AbortSignal,
  opts?: StreamOpts,
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
      msg = JSON.parse(await res.text()).error || msg;
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
      if (!line.startsWith("data:")) continue;
      const d = line.slice(5).trim();
      if (d === "[DONE]") return;
      try {
        // Reasoning models (e.g. Qwen3 via llama.cpp with a reasoning format) stream
        // their thinking as delta.reasoning_content and the answer as delta.content —
        // handle both, or a reasoning model looks like it never replied.
        const delta = JSON.parse(d).choices?.[0]?.delta;
        if (delta?.reasoning_content) onDelta(delta.reasoning_content, "reasoning");
        if (delta?.content) onDelta(delta.content, "content");
      } catch {
        /* ignore partial */
      }
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
