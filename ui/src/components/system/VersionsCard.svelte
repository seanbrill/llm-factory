<script lang="ts">
  // Versions (sheet frame f13): the factory build this page came from, whether
  // a newer one is running now (then reload), the engines' versions from
  // /readyz, and what new builds default to on this machine.
  import Icon from "../Icon.svelte";
  import { appVersion, sysinfo, toast } from "../../lib/stores";
  import { recheckVersion, stale, ui, verLabel } from "../../lib/ui.svelte";
  import { computeName, engineName } from "../../lib/format";
  import { engineList, engineVer, loadEngineVersions } from "../../lib/system.svelte";

  const engines = $derived(engineList($sysinfo));
  const isStale = $derived(ui.latest ? stale() : false);
  let checking = $state(false);

  async function check() {
    checking = true;
    try {
      await Promise.all([recheckVersion(), loadEngineVersions()]);
      toast(stale() ? "A newer factory is running. Reload to use it." : "Up to date: this page matches the factory that's running.");
    } finally {
      checking = false;
    }
  }

  function engineVersion(engine: string, reachable: boolean): string {
    const v = engineVer.map?.[engine];
    if (v) return v;
    if (!reachable || (engineVer.map && v === "")) return "not running";
    return "—";
  }
</script>

<div class="hd">
  <h2>Versions</h2>
  <div class="sp">
    <button class="small" onclick={check} disabled={checking} title="Compare this page with the factory that's running now">
      {#if checking}<span class="spin"></span>{/if}Check again
    </button>
  </div>
</div>

{#if isStale && ui.latest}
  <div class="callout ok upd">
    <Icon name="sparkle" size={16} />
    <span>
      <b>The factory was updated to {verLabel(ui.latest)}.</b>
      <p>This page still runs the UI it loaded with. <button class="linkish" onclick={() => location.reload()}>Reload to use the new one</button>.</p>
    </span>
  </div>
{/if}

<div class="ver">
  <span>LLM Factory</span>
  <span>{verLabel($appVersion) || "—"}</span>
  {#each engines as e (e.engine)}
    <span>{e.engine === "docker" ? "Docker Engine" : engineName(e.engine)}</span>
    <span class:fa={engineVersion(e.engine, e.reachable) === "not running"}>{engineVersion(e.engine, e.reachable)}</span>
  {/each}
  {#if $sysinfo?.default_engine}
    <span>New builds default to</span>
    <span>{engineName($sysinfo.default_engine)} · {computeName($sysinfo.default_compute)}</span>
  {/if}
</div>

<style>
  .upd { margin-bottom: 12px; }
  .linkish {
    display: inline;
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--pr);
    font: inherit;
    font-weight: 600;
    cursor: pointer;
  }
  .linkish:hover { background: none; text-decoration: underline; }
  .ver { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 8px 12px; font-size: 13px; }
  .ver > span:nth-child(odd) { color: var(--mu); }
  .ver > span:nth-child(even) { font-family: var(--font-mono); font-size: 12.5px; text-align: right; }
  .ver .fa { color: var(--fa); }
  .spin { width: 13px; height: 13px; }
</style>
