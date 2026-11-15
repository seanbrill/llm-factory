// Typed app stores — replace the vanilla globals (SYSINFO, CATALOG, …). Components
// subscribe instead of reading a module-level `let`.
import { writable } from "svelte/store";
import type { Model, SysInfo, Persona, Ensemble, ResourceBudget, VersionInfo, VoiceClip } from "./types";
import { api } from "./api";

export const sysinfo = writable<SysInfo | null>(null);
// The factory build this page was loaded from (see Sidebar for the update check).
export const appVersion = writable<VersionInfo | null>(null);
export const catalog = writable<Model[]>([]);
export const personas = writable<Persona[]>([]);
export const ensembles = writable<Ensemble[]>([]);
// Saved voice clips (metadata only; the audio stays in config/voices).
export const voices = writable<VoiceClip[]>([]);
export const resources = writable<ResourceBudget | null>(null);

// Set by Images "use as template"; consumed by the Build page to clone an image.
export const buildTemplate = writable<{ ref: string; engine: string } | null>(null);

export async function loadSysInfo() {
  try {
    sysinfo.set(await api<SysInfo>("/api/sysinfo"));
  } catch {
    /* keep the last known picture (null until the first load) */
  }
}
export async function loadVersion() {
  try {
    appVersion.set(await api<VersionInfo>("/api/version"));
  } catch {
    appVersion.set(null);
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
/** Reload the voice clips; throws so an editor can say why the list is missing. */
export async function loadVoices(): Promise<VoiceClip[]> {
  const v = (await api<{ voices: VoiceClip[] }>("/api/voices")).voices ?? [];
  voices.set(v);
  return v;
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
export function toast(msg: string, ms = 2800) {
  toastMsg.set(msg);
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => toastMsg.set(""), ms);
}
