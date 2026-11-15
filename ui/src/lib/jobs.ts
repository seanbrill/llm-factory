// Build-queue helpers shared by the top bar, Overview and Builds.
import type { BuildJob } from "./types";
import { views } from "./build.svelte";
import { clock, dur, engineName } from "./format";

export const ENGINES = ["docker", "podman"] as const;

/** Jobs grouped by engine lane, each lane in run order (running first). */
export function lanes(jobs: BuildJob[]): Record<string, BuildJob[]> {
  const out: Record<string, BuildJob[]> = { docker: [], podman: [] };
  for (const j of jobs) {
    if (!j.active) continue;
    (out[j.engine] ??= []).push(j);
  }
  for (const k of Object.keys(out)) {
    out[k].sort((a, b) =>
      a.status === b.status ? a.position - b.position || a.created_at - b.created_at : a.status === "running" ? -1 : 1,
    );
  }
  return out;
}

/** Finished jobs, newest first. */
export function history(jobs: BuildJob[]): BuildJob[] {
  return jobs.filter((j) => !j.active).sort((a, b) => (b.finished_at || 0) - (a.finished_at || 0));
}

export function counts(jobs: BuildJob[]) {
  let running = 0, queued = 0, failed = 0;
  for (const j of jobs) {
    if (j.status === "running") running++;
    else if (j.status === "queued") queued++;
    else if (j.status === "error") failed++;
  }
  return { running, queued, failed, active: running + queued };
}

/**
 * Progress of a running job, 0..1: the streamed download percentage while it
 * downloads, else elapsed time against the last build's duration (capped at
 * 95% so an overrun doesn't read as done). null = no basis for a guess.
 */
export function progress(j: BuildJob): number | null {
  if (j.status === "done") return 1;
  if (j.status !== "running") return null;
  const v = views[j.id];
  if (v?.pct != null && /^Downloading/.test(v.phase)) return Math.min(1, v.pct / 100);
  if (j.eta_seconds > 0) return Math.min(0.95, j.elapsed_ms / 1000 / j.eta_seconds);
  return null;
}

/** What a running job is doing now, in a few words. */
export function phase(j: BuildJob): string {
  const v = views[j.id];
  const p = v?.phase || "";
  if (/^Downloading/.test(p)) return p.replace(/\s*\.\.\.$/, "");
  if (/^Transfer \d\/\d/.test(p)) return "Moving the image to " + engineName(j.engine);
  if (/^Memory check/.test(p)) return "Checking memory";
  if (/^Stag/.test(p)) return "Preparing the build";
  if (/cmake|Compiling with|\[\s*\d+%\]|cc1plus|\.cpp/i.test(p + " " + (j.last_line || ""))) return "Compiling llama.cpp";
  if (/^Building image/.test(p)) return "Building the image";
  if (/^Export|Saved /.test(p)) return "Exporting the image";
  return j.status === "queued" ? "Waiting" : "Building";
}

/** "about 3 min left" / "6:12 elapsed" for a running job; wait text for a queued one. */
export function timeText(j: BuildJob): string {
  if (j.status === "running") {
    if (j.eta_seconds > 0) {
      const left = j.eta_seconds - j.elapsed_ms / 1000;
      return left > 30 ? `about ${dur(left)} left` : "almost done";
    }
    return `${clock(j.elapsed_ms)} elapsed`;
  }
  if (j.status === "queued") {
    if (j.position === 0) return "Next";
    return j.wait_seconds > 0 ? `starts in about ${dur(j.wait_seconds)}` : `${j.position} ahead`;
  }
  return "";
}

/** "Docker → Podman" when a build compiles on one engine and runs on the other. */
export function routeText(buildEngine: string, engine: string): string {
  return buildEngine && buildEngine !== engine
    ? `${engineName(buildEngine)} → ${engineName(engine)}`
    : engineName(engine);
}

/** The compile engine a running/finished job used, read from its log ("…so it compiles on Docker…"). */
export function compiledOn(j: BuildJob): string {
  const v = views[j.id];
  const m = (v?.log || j.last_line || "").match(/so it compiles on (Docker|the Podman machine)/);
  if (!m) return j.engine;
  return m[1] === "Docker" ? "docker" : "podman";
}
