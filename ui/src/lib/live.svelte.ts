// Shared live data: running containers, built images, model files, disk usage
// and the resource budget, fetched once and shared by every component that
// shows them (top bar, Overview, Library, palette). Components ask for what
// they need with `watch(...)` inside an $effect; polling runs while at least
// one watcher wants a source and stops when none do.
//
//   $effect(() => watch("containers", "storage"));
//
// `refresh(...)` re-fetches now (after a start/stop/delete).
import { api } from "./api";
import { resources } from "./stores";
import type { ContainerInfo, ImageInfo, Model, ResourceBudget, StorageInfo } from "./types";

export type ModelFileRow = Model & { downloaded: boolean; on_disk_gb: number };

export const live = $state({
  containers: [] as ContainerInfo[],
  images: [] as ImageInfo[],
  files: [] as ModelFileRow[],
  storage: null as StorageInfo | null,
  /** Set once each source has answered at least once. */
  loaded: { containers: false, images: false, files: false, storage: false, resources: false },
  /** Last error per source ("" = fine). */
  errors: { containers: "", images: "", files: "", storage: "", resources: "" },
});

export type Source = "containers" | "images" | "files" | "storage" | "resources";

const SOURCES: Record<Source, { every: number; fetch: () => Promise<void> }> = {
  containers: {
    every: 8000,
    fetch: async () => { live.containers = (await api<ContainerInfo[]>("/api/containers")) ?? []; },
  },
  images: {
    every: 30000,
    fetch: async () => { live.images = (await api<ImageInfo[]>("/api/images")) ?? []; },
  },
  files: {
    every: 60000,
    fetch: async () => { live.files = (await api<ModelFileRow[]>("/api/models")) ?? []; },
  },
  storage: {
    every: 60000,
    fetch: async () => { live.storage = await api<StorageInfo>("/api/storage"); },
  },
  resources: {
    every: 10000,
    fetch: async () => { resources.set(await api<ResourceBudget>("/api/resources")); },
  },
};

const watchers: Record<Source, number> = { containers: 0, images: 0, files: 0, storage: 0, resources: 0 };
const timers: Partial<Record<Source, ReturnType<typeof setTimeout>>> = {};
const inflight: Partial<Record<Source, Promise<void>>> = {};

async function run(src: Source): Promise<void> {
  if (inflight[src]) return inflight[src];
  const p = (async () => {
    try {
      await SOURCES[src].fetch();
      live.errors[src] = "";
    } catch (e) {
      live.errors[src] = (e as Error).message || "unreachable";
    } finally {
      live.loaded[src] = true;
      inflight[src] = undefined;
    }
  })();
  inflight[src] = p;
  return p;
}

function schedule(src: Source) {
  clearTimeout(timers[src]);
  if (watchers[src] <= 0) return;
  // Poll slower while the tab is hidden.
  const every = SOURCES[src].every * (document.hidden ? 4 : 1);
  timers[src] = setTimeout(async () => {
    await run(src);
    schedule(src);
  }, every);
}

/** Start sharing `sources` for the calling component; returns the cleanup. */
export function watch(...sources: Source[]): () => void {
  for (const s of sources) {
    watchers[s]++;
    if (watchers[s] === 1) {
      run(s).then(() => schedule(s));
    }
  }
  return () => {
    for (const s of sources) {
      watchers[s] = Math.max(0, watchers[s] - 1);
      if (watchers[s] === 0) clearTimeout(timers[s]);
    }
  };
}

/** Fetch these sources now (e.g. after starting or stopping a model). */
export async function refresh(...sources: Source[]): Promise<void> {
  // A poll already in flight may predate the change: wait for it, then fetch again.
  await Promise.all(sources.map(async (s) => { await inflight[s]; await run(s); }));
  for (const s of sources) schedule(s);
}

if (typeof document !== "undefined") {
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) return;
    for (const s of Object.keys(watchers) as Source[]) {
      if (watchers[s] > 0) refresh(s);
    }
  });
}
