// Small shared helpers ported from the vanilla app.js.
import type { ContainerInfo, ImageInfo } from "./types";
import { labelVal } from "./api";

export const COMPUTE_BADGE: Record<string, string> = { cpu: "CPU", cuda: "GPU (CUDA)", vulkan: "GPU (Vulkan)" };
export const computeBadge = (c?: string) => COMPUTE_BADGE[c ?? ""] ?? "CPU";
export const engineBadge = (e?: string) => (e === "podman" ? "Podman" : "Docker");
export const ctxDefaultFor = (compute: string) => (compute === "cuda" || compute === "vulkan" ? 8192 : 4096);
export const isStreamMod = (m: string) => m === "text" || m === "code" || m === "reasoning" || m === "vision";

export function containerRef(c: ContainerInfo): string {
  return labelVal(c.Labels, "local-llm.ref") || (c.Image ?? "");
}
export function containerPort(c: ContainerInfo): string {
  const m = (c.Names || "").match(/localllm-(\d+)/);
  if (m) return m[1];
  const pm = (c.Ports || "").match(/:(\d+)->/);
  return pm ? pm[1] : "";
}
export function isRunning(c: ContainerInfo): boolean {
  return (c.State || "").toLowerCase() === "running" || /^Up/.test(c.Status || "");
}

// imageRef derives the run/delete ref for an image row (repo:tag, else short id).
export function imageRef(im: ImageInfo): { ref: string; name: string; tag: string } {
  const repo = im.Repository && im.Repository !== "<none>" ? im.Repository : null;
  const tagged = im.Tag && im.Tag !== "<none>" ? im.Tag : null;
  const shortId = (im.ID || "").replace(/^sha256:/, "").slice(0, 12);
  const ref = repo && tagged ? `${repo}:${tagged}` : shortId;
  return { ref, name: repo || "<untagged>", tag: tagged || shortId };
}

export function fmtTokens(n: number): string {
  if (!n) return "";
  return n % 1024 === 0 ? `${n / 1024}K` : n.toLocaleString();
}
