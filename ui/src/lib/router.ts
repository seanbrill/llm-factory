// Hash router. A location looks like "#page" or "#page/arg" (e.g. "#builds/<job
// id>", "#playground/8081"). `route` is the page id, `routeArg` the rest; old
// page ids from the previous UI ("#build", "#images", …) still land on the page
// that took their place.
import { derived, readable } from "svelte/store";

export type NavGroup = "main" | "compose" | "machine" | "hidden";

export interface PageDef {
  id: string;
  label: string; // nav label
  title: string; // top bar title
  icon: string; // lib/icons name
  group: NavGroup;
  blurb: string; // one line on what the page is for (palette, help)
  key?: string; // single-key shortcut (no modifier) from anywhere
}

export const PAGES: PageDef[] = [
  { id: "overview", label: "Overview", title: "Overview", icon: "overview", group: "main", key: "o",
    blurb: "Your machine, the build queue, what's running and what needs you." },
  { id: "catalog", label: "Catalog", title: "Catalog", icon: "catalog", group: "main", key: "b",
    blurb: "Pick one or more models to build. Each says where it can run on this machine." },
  { id: "plan", label: "Build plan", title: "Build plan", icon: "builds", group: "hidden",
    blurb: "Review the picked models, their compute and route, then queue them all." },
  { id: "builds", label: "Builds", title: "Builds", icon: "builds", group: "main", key: "q",
    blurb: "The build queue: one lane per engine, live logs, failures explained." },
  { id: "library", label: "Library", title: "Library", icon: "library", group: "main", key: "l",
    blurb: "Every model on this machine: weights, images, running containers, and cleanup." },
  { id: "tune", label: "Tune", title: "Tune", icon: "gauge", group: "hidden",
    blurb: "Speed, context and memory settings for one model, and a benchmark on this machine." },
  { id: "harness", label: "Harness", title: "Harness", icon: "harness", group: "main",
    blurb: "Tools for Open WebUI: the gateway, a workspace terminal, skills and web search." },
  { id: "playground", label: "Playground", title: "Playground", icon: "playground", group: "main", key: "p",
    blurb: "Try a running model, or send one prompt to two models and compare." },
  { id: "ensembles", label: "Ensembles", title: "Ensembles", icon: "ensembles", group: "compose",
    blurb: "Combine built images into one multimodal model with a Conductor." },
  { id: "personas", label: "Personas", title: "Personas", icon: "personas", group: "compose",
    blurb: "Named system prompts to bake into a build or apply when running." },
  { id: "system", label: "System", title: "System", icon: "system", group: "machine", key: "s",
    blurb: "Engines, GPU access, memory across both engines, and build rules." },
  { id: "help", label: "Help", title: "Help & concepts", icon: "help", group: "hidden",
    blurb: "How the factory works, in plain language." },
];

// Page ids from the previous UI -> the page that replaced them.
export const ALIASES: Record<string, string> = {
  build: "catalog",
  images: "library",
  containers: "library",
  models: "library",
  chat: "playground",
  settings: "system",
};

// The nav item to highlight for a page that isn't in the nav itself.
const NAV_PARENT: Record<string, string> = { plan: "catalog", tune: "library", help: "" };

const ids = new Set(PAGES.map((p) => p.id));
const DEFAULT = "overview";

function parse(): { page: string; arg: string } {
  const h = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
  const slash = h.indexOf("/");
  let page = slash < 0 ? h : h.slice(0, slash);
  const arg = slash < 0 ? "" : h.slice(slash + 1);
  page = ALIASES[page] ?? page;
  return { page: ids.has(page) ? page : DEFAULT, arg };
}

const location$ = readable(parse(), (set) => {
  const on = () => set(parse());
  window.addEventListener("hashchange", on);
  return () => window.removeEventListener("hashchange", on);
});

/** Current page id. */
export const route = derived(location$, (l) => l.page);
/** Everything after "#page/", "" when absent. */
export const routeArg = derived(location$, (l) => l.arg);

/** Navigate to a page (and optional argument). */
export function go(page: string, arg = "") {
  const next = "#" + page + (arg ? "/" + encodeURIComponent(arg) : "");
  if (location.hash !== next) location.hash = next;
}

/** Link target for a page, for href attributes. */
export function href(page: string, arg = "") {
  return "#" + page + (arg ? "/" + encodeURIComponent(arg) : "");
}

export function pageDef(id: string): PageDef {
  return PAGES.find((p) => p.id === id) ?? PAGES[0];
}

export function navParent(id: string): string {
  return id in NAV_PARENT ? NAV_PARENT[id] : id;
}

export const NAV_GROUPS: { group: NavGroup; label: string }[] = [
  { group: "main", label: "" },
  { group: "compose", label: "Compose" },
  { group: "machine", label: "Machine" },
];
