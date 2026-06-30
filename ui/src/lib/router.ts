// Tiny hash router: a readable store of the current page id, plus the page list.
import { readable } from "svelte/store";

export interface PageDef { id: string; title: string; sub: string; icon: string; label: string; }

export const PAGES: PageDef[] = [
  { id: "build", title: "Build an image", sub: "Configure a model, build a self-contained image, run it, and chat.", icon: "wrench", label: "Build" },
  { id: "images", title: "Built images", sub: "Run, download or delete the images you've built.", icon: "box", label: "Images" },
  { id: "containers", title: "Running containers", sub: "Manage the models currently running.", icon: "layers", label: "Containers" },
  { id: "models", title: "Downloaded model files", sub: "Model weights cached on disk (re-downloaded on demand).", icon: "db", label: "Model files" },
  { id: "personas", title: "Personas", sub: "Named system prompts you can bake into a build or apply when running an image.", icon: "persona", label: "Personas" },
  { id: "ensembles", title: "Ensembles", sub: "Combine built images into one multimodal super-model with a Conductor that routes between them.", icon: "nodes", label: "Ensembles" },
  { id: "chat", title: "Chat", sub: "Talk to a running model in your browser.", icon: "chat", label: "Chat" },
  { id: "settings", title: "Settings", sub: "System resources and maintenance.", icon: "sliders", label: "Settings" },
  { id: "help", title: "Help & concepts", sub: "How the factory works, in plain language.", icon: "help", label: "Help" },
];

const ids = new Set(PAGES.map((p) => p.id));
function current(): string {
  const h = location.hash.replace(/^#/, "");
  return ids.has(h) ? h : "build";
}

export const route = readable<string>(current(), (set) => {
  const on = () => set(current());
  window.addEventListener("hashchange", on);
  return () => window.removeEventListener("hashchange", on);
});

export function pageDef(id: string): PageDef {
  return PAGES.find((p) => p.id === id) ?? PAGES[0];
}
