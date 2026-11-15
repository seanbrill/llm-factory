// Display formatting shared by every page.

/** "5.7 GB", "18 GB", "—" for unknown. */
export function gb(n: number | null | undefined, digits = 1): string {
  if (n == null || !isFinite(n)) return "—";
  if (n > 0 && n < 0.1) return `${Math.round(n * 1000)} MB`;
  const s = n >= 100 ? n.toFixed(0) : n.toFixed(digits);
  return `${s.replace(/\.0+$/, "")} GB`;
}

/** Seconds as "45 s", "3 min", "1 h 14 m". */
export function dur(secs: number): string {
  secs = Math.max(0, Math.round(secs));
  if (secs < 60) return `${secs} s`;
  const m = Math.round(secs / 60);
  if (m < 60) return `${m} min`;
  const h = Math.floor(secs / 3600);
  const rm = Math.round((secs % 3600) / 60);
  return rm ? `${h} h ${rm} m` : `${h} h`;
}

/** Milliseconds as a stopwatch: "6:12", "1:02:09". */
export function clock(ms: number): string {
  const t = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(t / 3600);
  const m = Math.floor((t % 3600) / 60);
  const s = t % 60;
  const ss = String(s).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

/** A unix-ms time as "just now", "4 min ago", "2 h ago", "yesterday", or a date. */
export function ago(ms: number, now = Date.now()): string {
  if (!ms) return "";
  const s = (now - ms) / 1000;
  if (s < 45) return "just now";
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  if (s < 172800) return "yesterday";
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** "1 build", "3 builds". */
export function plural(n: number, one: string, many = one + "s"): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** Engine id -> display name. */
export function engineName(e: string | undefined): string {
  return e === "podman" ? "Podman" : "Docker";
}

/** Compute id -> chip label. */
export function computeName(c: string | undefined): string {
  return c === "vulkan" ? "Vulkan" : c === "cuda" ? "CUDA" : "CPU";
}

/** Chip class for a compute: GPU computes get the GPU colour. */
export function computeChip(c: string | undefined): string {
  return c === "vulkan" || c === "cuda" ? "chip gpu" : "chip";
}

/** Docker's "Up 2 hours (healthy)" -> "up 2 h". */
export function upFor(status: string | undefined): string {
  const m = (status || "").match(/^Up\s+(.+?)(\s+\(.*\))?$/i);
  if (!m) return "";
  return "up " + m[1]
    .replace(/About an? /i, "1 ")
    .replace(/\s*hours?/i, " h")
    .replace(/\s*minutes?/i, " min")
    .replace(/\s*seconds?/i, " s")
    .replace(/\s*days?/i, " d")
    .replace(/\s*weeks?/i, " wk");
}
