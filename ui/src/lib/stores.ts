// Typed app stores — replace the vanilla globals (SYSINFO, CATALOG, …). Components
// subscribe instead of reading a module-level `let`.
import { writable } from "svelte/store";
import type { Model, SysInfo, Persona, Ensemble, ResourceBudget } from "./types";
import { api } from "./api";

export const sysinfo = writable<SysInfo | null>(null);
export const catalog = writable<Model[]>([]);
export const personas = writable<Persona[]>([]);
export const ensembles = writable<Ensemble[]>([]);
export const resources = writable<ResourceBudget | null>(null);

// Set by Images "use as template"; consumed by the Build page to clone an image.
export const buildTemplate = writable<{ ref: string; engine: string } | null>(null);

export async function loadSysInfo() {
  try {
    sysinfo.set(await api<SysInfo>("/api/sysinfo"));
  } catch {
    sysinfo.set(null);
  }
}
export async function loadCatalog() {
  try {
    catalog.set(await api<Model[]>("/api/catalog"));
  } catch {
    catalog.set([]);
  }
}
export async function loadPersonas() {
  try {
    personas.set((await api<{ personas: Persona[] }>("/api/personas")).personas ?? []);
  } catch {
    personas.set([]);
  }
}
export async function loadEnsembles() {
  try {
    ensembles.set((await api<{ ensembles: Ensemble[] }>("/api/ensembles")).ensembles ?? []);
  } catch {
    ensembles.set([]);
  }
}
export async function loadResources() {
  try {
    resources.set(await api<ResourceBudget>("/api/resources"));
  } catch {
    resources.set(null);
  }
}

// ---- toast -----------------------------------------------------------------
export const toastMsg = writable<string>("");
let toastTimer: ReturnType<typeof setTimeout> | undefined;
export function toast(msg: string) {
  toastMsg.set(msg);
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toastMsg.set(""), 2800);
}
