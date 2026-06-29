// Per-model CPU/GPU performance estimate, ported from the vanilla hwPerf().
import type { Model, SysInfo } from "./types";

const RANKS = ["impossible", "poor", "warning", "fair", "good", "excellent"];
const lvl = (r: number) => RANKS[Math.max(0, Math.min(5, r))];
const worse = (a: string, b: string) => (RANKS.indexOf(a) <= RANKS.indexOf(b) ? a : b);

export const PERF_LABEL: Record<string, string> = {
  excellent: "Excellent", good: "Good", fair: "Fair", warning: "Warning",
  poor: "Poor", impossible: "Won't run", na: "N/A",
};

function capByFit(level: string, need: number, capacity: number): string {
  if (!capacity || !need) return level;
  if (need > capacity + 0.01) return "impossible";
  const head = capacity - need;
  if (head < 1.5) return worse(level, "warning");
  if (head < 3) return worse(level, "fair");
  return level;
}

export function hwPerf(m: Model, sys: SysInfo | null): { cpu: string; gpu: string } {
  const mod = m.modality ?? "text";
  const gb = m.size_gb || 0;
  const cap = sys?.mem_gb || 0;
  const hasGPU = !!sys?.gpu;

  let cpu: string;
  if (mod === "tts" || mod === "embedding") cpu = "excellent";
  else if (mod === "audio-stt") cpu = gb > 1 ? "good" : "excellent";
  else if (mod === "image") cpu = gb > 3 ? "poor" : "warning";
  else if (mod === "video") cpu = "impossible";
  else {
    cpu = gb <= 1.5 ? "excellent" : gb <= 3 ? "good" : gb <= 6 ? "fair" : gb <= 10 ? "warning" : "poor";
    if (mod === "reasoning") cpu = lvl(Math.max(1, RANKS.indexOf(cpu) - 1));
    if (mod === "vision") cpu = lvl(Math.max(1, RANKS.indexOf(cpu) - 1));
  }
  cpu = capByFit(cpu, m.min_ram_gb || gb, cap);

  let gpu: string;
  if (mod === "tts") gpu = "na";
  else if (!hasGPU) gpu = "na";
  else {
    gpu = mod === "image" || mod === "video" ? (gb > 6 ? "good" : "excellent") : "excellent";
    const vcap = sys?.vram_gb || cap;
    gpu = capByFit(gpu, m.min_vram_gb || gb, vcap);
  }

  // Python/PyTorch (fp16) models have no GGUF quant and no clean CPU offload:
  // CPU is impractical except for tiny ones, and VRAM is a hard floor (capByFit
  // already returns "impossible" when it won't fit — no offload to lean on).
  if ((m.runtime ?? "cpp") === "python") {
    cpu = gb <= 2 ? worse(cpu, "poor") : "impossible";
  }
  return { cpu, gpu };
}
