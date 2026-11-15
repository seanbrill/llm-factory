// App-wide UI state that isn't data: overlays and the factory's update check.
import { api } from "./api";
import { appVersion, loadSysInfo } from "./stores";
import { get } from "svelte/store";
import type { VersionInfo } from "./types";

export const ui = $state({
  paletteOpen: false,
  /** Text to start the palette with (e.g. "build "). */
  paletteSeed: "",
  /** Mobile "More" sheet. */
  moreOpen: false,
  /** The factory build currently running (vs. the one this page loaded from). */
  latest: null as VersionInfo | null,
});

export function openPalette(seed = "") {
  ui.paletteSeed = seed;
  ui.paletteOpen = true;
}

export function closePalette() {
  ui.paletteOpen = false;
  ui.paletteSeed = "";
}

/**
 * An open tab keeps running the UI it loaded, even after the factory restarts on
 * newer code. Re-check on focus and every minute; `stale()` offers a reload when
 * the running build no longer matches the one this page came from.
 */
export async function recheckVersion() {
  try {
    ui.latest = await api<VersionInfo>("/api/version");
  } catch {
    /* factory restarting */
  }
  loadSysInfo(); // engine machines can be resized or started meanwhile
}

export function stale(): boolean {
  const mine = get(appVersion);
  const l = ui.latest;
  return !!mine && !!l && (l.version !== mine.version || l.commit !== mine.commit);
}

export function verLabel(v: VersionInfo | null): string {
  if (!v) return "";
  return (v.version === "dev" ? "dev" : "v" + v.version) + (v.commit ? " · " + v.commit : "");
}

/** Copy text, with a fallback for plain-http LAN access (no clipboard API). */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through */
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.setAttribute("readonly", "");
  ta.style.cssText = "position:fixed;top:-1000px;opacity:0";
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand("copy");
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}
