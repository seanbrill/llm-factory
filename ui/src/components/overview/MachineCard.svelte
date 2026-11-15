<script lang="ts">
  // "This Mac" (sheet frame f1): how this machine routes builds, said from the
  // engines' GPU and defaults. The host's own memory isn't visible to the
  // factory; it shows only when the user entered it on System's memory plan.
  import Icon from "../Icon.svelte";
  import { sysinfo } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { computeName, engineName } from "../../lib/format";
  import { routingSentence } from "../../lib/overview";
  import { gpuEngine, hostMem, isMac } from "../../lib/system.svelte";
  import { gpuEngines } from "../../lib/catalog";

  let { state }: { state: "loading" | "ok" | "error" } = $props();

  const mac = $derived(isMac($sysinfo));
  const g = $derived(gpuEngine($sysinfo));
  // Both engines can give containers the GPU (a libkrun machine and Docker VMM).
  const through = $derived(
    gpuEngines($sysinfo, g?.gpu)
      .map((e) => engineName(e.engine))
      .join(" and "),
  );
  const sub = $derived.by(() => {
    const parts: string[] = [];
    if (hostMem.gb) parts.push(`${hostMem.gb} GB`);
    if ($sysinfo) parts.push(g ? `${computeName(g.gpu)} GPU through ${through}` : "No GPU for containers");
    return parts.join(" · ");
  });
</script>

<div class="card mc">
  <div class="t">
    <span class="ico"><Icon name="mac" size={18} /></span>
    <div class="tt">
      <b>{mac ? "This Mac" : "This machine"}</b>
      {#if sub}<div class="sub">{sub}</div>{/if}
    </div>
  </div>
  {#if $sysinfo}
    <p class="sub">{routingSentence($sysinfo)}</p>
  {:else if state === "error"}
    <p class="sub">
      Couldn't read the engines. Check that Docker is running, then <a href={href("system")}>look at System</a>.
    </p>
  {:else}
    <p class="sub load"><span class="spin"></span>Reading the engines…</p>
  {/if}
</div>

<style>
  .mc { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .t { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .tt { min-width: 0; }
  .t b { font-size: 15px; }
  .ico {
    width: 34px;
    height: 34px;
    border-radius: 9px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .load { display: flex; align-items: center; gap: 8px; }
</style>
