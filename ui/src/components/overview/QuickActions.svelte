<script lang="ts">
  // Quick actions (sheet frame f1). Shortcut hints are the app's real ones
  // (router PAGES keys, ⌘K).
  import { post } from "../../lib/api";
  import { toast } from "../../lib/stores";
  import { live, refresh } from "../../lib/live.svelte";
  import { href } from "../../lib/router";
  import { openPalette } from "../../lib/ui.svelte";
  import { isRunning } from "../../lib/util";
  import { plural } from "../../lib/format";

  const running = $derived(live.containers.filter(isRunning));
  let stopping = $state(false);
  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

  async function stopAll() {
    const n = running.length;
    if (!n || !confirm(`Stop ${plural(n, "running model")}? Their containers are removed; the images stay in your Library.`)) return;
    stopping = true;
    const results = await Promise.allSettled(running.map((c) => post("/api/stop", { id: c.ID || c.Names, engine: c.Engine || "docker" })));
    stopping = false;
    const failed = results.filter((r) => r.status === "rejected").length;
    toast(failed ? `Stopped ${n - failed}; ${failed} didn't stop — see the Library.` : `Stopped ${plural(n, "model")}.`, failed ? 7000 : 2800);
    refresh("containers", "resources", "harness");
  }
</script>

<div class="card">
  <div class="hd"><h2>Quick actions</h2></div>
  <div class="qa">
    <a class="btn" href={href("catalog")}>Build models<span class="kbd">B</span></a>
    <a class="btn" href={href("library")}>Run an image<span class="kbd">L</span></a>
    <a class="btn" href={href("system", "gpu")}>Check GPU access</a>
    <button onclick={stopAll} disabled={!running.length || stopping} title={running.length ? "" : "Nothing is running"}>
      {#if stopping}<span class="spin"></span>{/if}Stop all models
    </button>
    <button class="wide" onclick={() => openPalette()}>
      Search models or run a command<span class="kbd">{isMac ? "⌘K" : "Ctrl K"}</span>
    </button>
  </div>
</div>

<style>
  .qa { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .qa .btn, .qa button { justify-content: flex-start; min-height: 40px; padding: 0 12px; }
  .qa .kbd { margin-left: auto; }
  .qa .wide { grid-column: 1 / -1; color: var(--mu); font-weight: 400; }
  .spin { width: 13px; height: 13px; }
</style>
