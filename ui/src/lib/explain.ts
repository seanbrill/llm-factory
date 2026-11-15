// Turns a failed build into one sentence on what happened, one on why, and the
// fixes, from the job's error and its log. The patterns match the messages the
// factory itself writes (internal/builder: oomHint, notEnoughMemory,
// ClassifyEngineError) as well as raw engine/compiler output.
import type { BuildJob } from "./types";
import { engineName } from "./format";

export type FixAction = "retry" | "cleanup" | "system" | "library";

export interface Fix {
  label: string;      // what to do, imperative
  cmd?: string;       // a command to copy and run on the host
  action?: FixAction; // something the UI can do for them
}

export interface Explanation {
  tone: "er" | "wa";
  title: string;  // what happened
  why?: string;   // why, in one sentence
  fixes: Fix[];
}

export const PODMAN_MEMORY_CMD = "podman machine stop && podman machine set --memory 12288 && podman machine start";
export const DOCKER_MEMORY_PATH = "Docker Desktop → Settings → Resources → Memory";

const RETRY: Fix = { label: "Build it again", action: "retry" };

/** Give an engine's machine more memory. */
export function memoryFix(engine: string): Fix {
  return engine === "podman"
    ? { label: "Give Podman 12 GB, then restart the factory so it reconnects", cmd: PODMAN_MEMORY_CMD }
    : { label: `Raise Docker's memory: ${DOCKER_MEMORY_PATH}` };
}

function has(text: string, ...subs: string[]) {
  return subs.some((s) => text.includes(s));
}

/** "Compiling with 7 parallel job(s): 7 CPUs, 1843 MiB RAM available" -> facts. */
function compileFacts(log: string): { jobs?: number; mib?: number } {
  const m = log.match(/Compiling with (\d+) parallel job\(s\): \d+ CPUs, (\d+) MiB/);
  return m ? { jobs: +m[1], mib: +m[2] } : {};
}

export function explainFailure(job: BuildJob, log = ""): Explanation | null {
  if (job.status === "canceled") {
    return { tone: "wa", title: "Canceled.", fixes: [RETRY] };
  }
  if (job.status !== "error") return null;
  const err = job.error || "";
  const all = `${err}\n${log}`;
  const t = all.toLowerCase();
  const eng = engineName(job.engine);

  if (has(t, "timed out after")) {
    return {
      tone: "er",
      title: "Stopped after 2 hours without finishing.",
      why: "The engine may be stuck, or the machine is too small for this build.",
      fixes: [RETRY, { label: "Check the engines on System", action: "system" }],
    };
  }

  if (has(t, "not enough memory to compile")) {
    return {
      tone: "er",
      title: "Neither engine has enough memory to compile this image.",
      why: err.split("\n")[0].replace(/^Not enough memory to compile this \w+ image: /, "").replace(/^\w/, (c) => c.toUpperCase()),
      fixes: [memoryFix("podman"), memoryFix("docker"), RETRY],
    };
  }

  if (has(t, "killed signal terminated program", "virtual memory exhausted", "out of memory allocating", "the compiler ran out of memory")) {
    const f = compileFacts(log);
    const onDocker = /ran out of memory on Docker/.test(all) || (!/Podman/.test(err) && job.engine === "docker");
    const where = onDocker ? "docker" : "podman";
    const facts = f.mib ? `It had ${(f.mib / 1024).toFixed(1)} GB free and compiled ${f.jobs} file${f.jobs === 1 ? "" : "s"} at once.` : undefined;
    return {
      tone: "er",
      title: `${engineName(where)} ran out of memory while compiling.`,
      why: facts ?? "The compiler was killed by the out-of-memory killer.",
      fixes: [
        memoryFix(where),
        { label: "Build again: the factory now sizes compile jobs to memory and compiles on the other engine when this one is too small", action: "retry" },
      ],
    };
  }

  if (has(t, "container engine unreachable", "cannot connect to the docker daemon", "cannot connect to podman", "connection refused", "isn't answering")) {
    return {
      tone: "er",
      title: `${eng} isn't running.`,
      why: "The factory couldn't reach the container engine.",
      fixes: [
        job.engine === "podman"
          ? { label: "Start the Podman machine, then restart the factory so it reconnects", cmd: "podman machine start" }
          : { label: "Start Docker Desktop" },
        RETRY,
      ],
    };
  }

  if (has(t, "isn't installed here")) {
    return { tone: "er", title: `${eng} isn't installed.`, fixes: [{ label: "Build it on the other engine from the Catalog" }] };
  }

  if (has(t, "no space left", "out of disk space")) {
    return {
      tone: "er",
      title: "Out of disk space.",
      why: "The engine or this machine has no room for the image.",
      fixes: [
        { label: "Free space: Library → Clean up", action: "cleanup" },
        { label: "Remove unused engine data", cmd: job.engine === "podman" ? "podman system prune" : "docker system prune" },
        RETRY,
      ],
    };
  }

  if (has(t, "/dev/dri")) {
    return {
      tone: "er",
      title: `${eng} can't give containers the GPU.`,
      why: "It has no GPU device (/dev/dri) on this machine.",
      fixes: [{ label: "Build GPU (Vulkan) images for Podman, which passes the Apple GPU through" }, { label: "Check GPU access on System", action: "system" }],
    };
  }

  if (has(t, "tls handshake timeout", "temporary failure in name resolution", "i/o timeout", "connection reset by peer", "no such host")) {
    return { tone: "er", title: "The network dropped during the build.", why: firstLine(err), fixes: [RETRY] };
  }

  if (/download/.test(t) && /\b(401|403|404|429)\b/.test(t)) {
    const gated = /\b(401|403)\b/.test(t);
    return {
      tone: "er",
      title: "A model download was refused.",
      why: gated ? "The file is gated: its Hugging Face page needs you to accept the license, or a token." : firstLine(err),
      fixes: gated ? [{ label: "Accept the model's license on Hugging Face and set HF_TOKEN for the factory" }, RETRY] : [RETRY],
    };
  }

  return {
    tone: "er",
    title: firstLine(err) || "The build failed.",
    why: lastUseful(log),
    fixes: [RETRY],
  };
}

function firstLine(s: string): string {
  return (s || "").split("\n").find((l) => l.trim())?.trim() ?? "";
}

/** The last log line that says something (skips DONE/ERROR markers and blanks). */
function lastUseful(log: string): string | undefined {
  const lines = log.split("\n").map((l) => l.trim()).filter(Boolean);
  for (let i = lines.length - 1; i >= 0; i--) {
    const l = lines[i];
    if (/^(ERROR:|detail:|DONE|CANCELED)/.test(l)) continue;
    return l.length > 220 ? l.slice(0, 220) + "…" : l;
  }
  return undefined;
}

/** Index of the first log line worth opening on: the error, or the compiler kill. */
export function focusLine(lines: string[]): number {
  const re = /(killed signal|error:|ERROR:|fatal|failed|no space left|out of memory|cannot connect)/i;
  const i = lines.findIndex((l) => re.test(l));
  return i;
}
