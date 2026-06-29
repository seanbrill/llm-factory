// API shapes, mirroring the Go structs (internal/catalog, internal/builder,
// internal/server, internal/ensemble). Hand-maintained at this size.

export type Modality =
  | "text" | "code" | "reasoning" | "vision" | "embedding"
  | "image" | "video" | "audio-stt" | "tts";

export interface WeightFile { file: string; url: string; role?: string; }

export interface Model {
  id: string;
  name: string;
  repo: string;
  file: string;
  url: string;
  size_gb: number;
  tier: string;
  params: string;
  min_ram_gb: number;
  min_vram_gb: number;
  recommended: boolean;
  description: string;
  modality?: Modality;
  mmproj_file?: string;
  mmproj_url?: string;
  extra_files?: WeightFile[];
  runtime?: "cpp" | "python"; // default cpp (GGUF); python = PyTorch/ComfyUI
}

export interface SysInfo {
  mem_gb: number;
  cpus: number;
  gpu: string;     // "" | "cuda" | "vulkan"
  vram_gb: number; // 0 if unknown
  engine: string;
}

// docker/podman list records arrive with capitalised keys + a comma-joined
// Labels string (docker) or object (podman) — kept loose, read via labelVal().
export interface ContainerInfo {
  ID?: string;
  Names?: string;
  Image?: string;
  Engine?: string;
  Status?: string;
  State?: string;
  Ports?: string;
  Labels?: string | Record<string, string>;
}

export interface ImageInfo {
  ID?: string;
  Repository?: string;
  Tag?: string;
  Size?: string;
  Engine?: string;
  Compute?: string;
  Labels?: string | Record<string, string>;
}

export interface ModelFile {
  id: string;
  name: string;
  file: string;
  downloaded: boolean;
  on_disk_gb: number;
}

export interface Persona { id: string; name: string; prompt: string; }

export interface EnsembleMember {
  tool: string;
  modality: string;
  image: string;
  port?: number;
  vram_gb?: number;
}
export interface Ensemble {
  id: string;
  name: string;
  package_mode: string;  // orchestrated | embedded
  routing: string;       // heuristic | tool-calling
  conductor?: string;
  vram_budget_gb: number;
  engine?: string;
  compute?: string;
  members: EnsembleMember[];
}

export interface ChatMessage {
  role: "user" | "assistant" | "system";
  content: string;
}

export interface RunningModel {
  name: string;
  model_id: string;
  compute: string;
  vram_gb: number;
  ram_gb: number;
}
// Live resource picture from /api/resources. Committed = footprints of the
// containers the factory manages; usage outside the factory isn't visible.
export interface ResourceBudget {
  gpu: string;
  total_vram_gb: number;
  total_ram_gb: number;
  committed_vram_gb: number;
  committed_ram_gb: number;
  free_vram_gb: number;
  free_ram_gb: number;
  running: RunningModel[] | null;
}
