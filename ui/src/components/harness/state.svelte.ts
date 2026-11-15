// Harness page state shared by its cards: the terminal action in flight (the
// hero and the terminal card both start it), the last error of one, and the
// Open WebUI result (the hero, the Open WebUI card and "What models can do" all
// read it). Data itself comes from live.harness (lib/live.svelte.ts).
import { refresh } from "../../lib/live.svelte";
import { toast } from "../../lib/stores";
import {
  startTerminal,
  stopTerminal,
  regenerateGatewayKey,
  regenerateTerminalKey,
  saveTerminalSettings,
  checkOpenWebUI,
  connectOpenWebUI,
  loadOwuiMemo,
  saveOwuiMemo,
  setupFingerprint,
  type HarnessStatus,
  type OpenWebUIRequest,
  type OwuiMemo,
  type TerminalSettings,
} from "../../lib/harness";

export type Busy = "" | "start" | "update" | "stop" | "remove" | "gwkey" | "termkey" | "settings";

export const hs = $state({
  /** The terminal/key action in flight. */
  busy: "" as Busy,
  /** The last failed terminal or key action, as a sentence ("" = none). */
  error: "",
  /** Which action that was, so the card it belongs to shows it. */
  errorFrom: "" as Busy,
  /** The last Check/Connect result (this browser), see lib/harness OwuiMemo. */
  memo: loadOwuiMemo() as OwuiMemo | null,
  /** Check/Connect in flight. */
  connecting: "" as "" | "check" | "connect",
  /** The connector request itself failed (bad JSON, the factory refused it). */
  connectError: "",
});

async function act(busy: Busy, fn: () => Promise<unknown>, done = ""): Promise<boolean> {
  hs.busy = busy;
  hs.error = "";
  hs.errorFrom = "";
  try {
    await fn();
    if (done) toast(done);
    return true;
  } catch (e) {
    hs.error = (e as Error).message || "The factory didn't answer.";
    hs.errorFrom = busy;
    return false;
  } finally {
    // Stay busy until the status shows the change (a start turns into op.active).
    await refresh("harness");
    hs.busy = "";
  }
}

export const start = (update = false) => act(update ? "update" : "start", () => startTerminal(update));

export const stop = () => act("stop", () => stopTerminal(false), "Terminal stopped.");

/**
 * Stop, then start: a start alone leaves a running terminal as it is unless its
 * settings changed (then it's recreated with them; workspace files are kept).
 */
export const restart = () =>
  act("start", async () => {
    await stopTerminal(false);
    await startTerminal(false);
  });

export function cancelStart() {
  return act("stop", () => stopTerminal(false), "Start canceled.");
}

export function remove() {
  if (!confirm("Remove the terminal container? Files in the workspace folder are kept; anything installed inside the container is lost.")) return;
  return act("remove", () => stopTerminal(true), "Terminal container removed.");
}

export function newGatewayKey() {
  if (!confirm("Make a new gateway key? The old key stops working right away, so run the Open WebUI setup again (or paste the new key into its connection).")) return;
  return act("gwkey", regenerateGatewayKey, "New gateway key made. Run the Open WebUI setup again to use it.");
}

export function newTerminalKey() {
  if (!confirm("Make a new terminal key? Restart the terminal, then run the Open WebUI setup again.")) return;
  return act("termkey", regenerateTerminalKey, "New terminal key made. Restart the terminal, then run the setup again.");
}

/** Save one terminal setting now. Returns the error sentence, "" when saved. */
export async function saveSetting(s: TerminalSettings): Promise<string> {
  hs.busy = "settings";
  try {
    await saveTerminalSettings(s);
    return "";
  } catch (e) {
    return (e as Error).message || "The factory didn't save it.";
  } finally {
    await refresh("harness");
    hs.busy = "";
  }
}

/** Run Check or Connect; the result replaces this browser's memo. */
export async function runConnector(kind: "check" | "connect", req: OpenWebUIRequest, st: HarnessStatus): Promise<boolean> {
  hs.connecting = kind;
  hs.connectError = "";
  try {
    const res = kind === "check" ? await checkOpenWebUI(req) : await connectOpenWebUI(req);
    const memo: OwuiMemo = { at: Date.now(), kind, url: res.url, report: res.report, fp: setupFingerprint(st) };
    hs.memo = memo;
    saveOwuiMemo(memo);
    const r = res.report;
    if (kind === "connect" && r.admin && !r.items.some((i) => i.status === "error")) {
      toast(r.changed ? "Open WebUI is set up." : "Open WebUI was already set up.");
    }
    return true;
  } catch (e) {
    hs.connectError = (e as Error).message || "The factory didn't answer.";
    return false;
  } finally {
    hs.connecting = "";
    refresh("harness");
  }
}

/** True when the saved result was made with the keys and URLs in use now. */
export function memoCurrent(st: HarnessStatus | null): boolean {
  return !!hs.memo && !!st && hs.memo.fp === setupFingerprint(st);
}

/** Scroll a card into view and move focus to it (in-page jumps; the URL hash is the router's). */
export function jump(id: string, focusSel = "") {
  const el = document.getElementById(id);
  if (!el) return;
  el.scrollIntoView({ behavior: "smooth", block: "start" });
  const target = (focusSel && el.querySelector<HTMLElement>(focusSel)) || el;
  target.focus({ preventScroll: true });
}
