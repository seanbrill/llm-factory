<script lang="ts">
  import Icon from "./Icon.svelte";
  import { PAGES, route } from "../lib/router";
  import { sysinfo, appVersion, loadSysInfo } from "../lib/stores";
  import { api } from "../lib/api";
  import { engineBadge } from "../lib/util";
  import type { EngineResources, VersionInfo } from "../lib/types";

  let collapsed = $state(localStorage.getItem("llmfactory.nav.collapsed") === "1");
  function toggle() {
    collapsed = !collapsed;
    localStorage.setItem("llmfactory.nav.collapsed", collapsed ? "1" : "0");
  }

  // An open tab keeps running the UI it loaded, even after the factory restarts
  // on newer code. Re-check the running build on focus (and every minute) and
  // offer a reload when it no longer matches the one this page came from.
  let latest = $state<VersionInfo | null>(null);
  async function recheck() {
    try { latest = await api<VersionInfo>("/api/version"); } catch { /* factory restarting */ }
    loadSysInfo(); // engine machines can be resized or started meanwhile
  }
  $effect(() => {
    const timer = setInterval(recheck, 60_000);
    window.addEventListener("focus", recheck);
    return () => { clearInterval(timer); window.removeEventListener("focus", recheck); };
  });
  const stale = $derived(
    !!$appVersion && !!latest && (latest.version !== $appVersion.version || latest.commit !== $appVersion.commit),
  );
  const verLabel = (v: VersionInfo) => (v.version === "dev" ? "dev build" : "v" + v.version);

  let sysText = $derived.by(() => {
    const s = $sysinfo;
    if (!s || !s.mem_gb) return "Detecting system…";
    const gpu = s.gpu ? `${s.gpu} GPU${s.vram_gb ? " " + s.vram_gb + " GB" : ""}` : "CPU only";
    return `${s.mem_gb.toFixed(0)} GB · ${s.cpus} CPU · ${gpu}`;
  });

  // Each engine's machine on its own line — Docker Desktop's VM and the Podman
  // machine have separate memory and GPU access. Engines that didn't answer are
  // left out, except the GPU one (its absence matters).
  const engines = $derived(($sysinfo?.engines ?? []).filter((e) => e.reachable || e.gpu));
  function engText(e: EngineResources): string {
    if (!e.reachable) return "not running";
    const gpu = e.gpu ? `${e.gpu} GPU${e.gpu === "cuda" && $sysinfo?.vram_gb ? " " + $sysinfo.vram_gb + " GB" : ""}` : "CPU only";
    return `${e.mem_gb.toFixed(0)} GB · ${e.cpus} CPU · ${gpu}`;
  }
  function engTitle(e: EngineResources): string {
    const name = e.engine === "podman" ? "Podman machine" : "Docker";
    if (!e.reachable) return `${name}: not reachable — start it, then restart the factory so it reconnects.`;
    let t = `${name}: ${e.mem_gb.toFixed(1)} GB memory, ${e.cpus} CPUs${e.gpu ? `, ${e.gpu} GPU` : ", no GPU"}.`;
    if (e.low_memory) {
      const need = $sysinfo?.compile_min_gb?.[e.gpu] ?? 6;
      t += `\n⚠ Too little memory to compile GPU images (needs ${need} GB): GPU builds compile on the other engine and are transferred here, and only models that fit in ${e.mem_gb.toFixed(1)} GB can run on its GPU.`;
      t += `\nGive it more memory: ${e.fix}${e.engine === "podman" ? " (then restart the factory)" : ""}`;
    }
    return t;
  }
</script>

<aside class="sidebar" class:collapsed>
  <div class="brand">
    <span class="brand-mark">
      <Icon name="cpu" size={20} />
    </span>
    <span class="brand-text"><b>LLM</b> Factory</span>
  </div>
  <nav class="nav">
    {#each PAGES as p (p.id)}
      <a href={"#" + p.id} class:active={$route === p.id}>
        <Icon name={p.icon} />
        <span>{p.label}</span>
      </a>
    {/each}
  </nav>
  <div class="foot">
    {#if engines.length}
      <div class="sys-list">
        {#each engines as e (e.engine)}
          <div class="sys-pill" class:low={e.low_memory} class:down={!e.reachable} title={engTitle(e)}>
            <Icon name={e.gpu ? "gpu" : "box"} size={14} />
            <span><b>{engineBadge(e.engine)}</b> {engText(e)}</span>
            {#if e.low_memory}<span class="low-flag"><Icon name="alert" size={13} /></span>{/if}
          </div>
        {/each}
      </div>
    {:else}
      <div class="sys-pill"><Icon name="cpu" size={14} /> <span>{sysText}</span></div>
    {/if}
    {#if stale && latest}
      <button class="ver-new" onclick={() => location.reload()} title="The factory was restarted on a newer build; this page is still the old one.">
        <Icon name="refresh" size={13} /><span>Updated to {verLabel(latest)} — reload</span>
      </button>
    {/if}
    {#if $appVersion}
      <div class="ver" title="Factory build: compare this across machines to confirm they run the same code">
        {verLabel($appVersion)}<span class="ver-commit">{#if $appVersion.commit} · {$appVersion.commit}{/if}</span>
      </div>
    {/if}
    <button class="nav-toggle" onclick={toggle}>
      <Icon name="chevronsLeft" size={15} /><span>Collapse</span>
    </button>
  </div>
</aside>

<style>
  .sidebar {
    width: 234px;
    flex: none;
    background: #0c0e13;
    border-right: 1px solid var(--border);
    display: flex;
    flex-direction: column;
    position: sticky;
    top: 0;
    height: 100vh;
    transition: width 0.16s ease;
  }
  .sidebar.collapsed { width: 68px; }
  .brand { display: flex; align-items: center; gap: 10px; padding: 18px 16px 14px; }
  .sidebar.collapsed .brand { justify-content: center; }
  .brand-mark {
    width: 34px; height: 34px; border-radius: 9px; flex: none;
    display: inline-flex; align-items: center; justify-content: center; color: #fff;
    background: linear-gradient(135deg, #4f8cff, #8b5cf6);
    box-shadow: 0 4px 16px rgba(79, 140, 255, 0.4);
  }
  .brand-text { font-size: 16px; }
  .brand-text :global(b) {
    font-weight: 800;
    background: linear-gradient(90deg, #7aa2ff, #c08af0);
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }
  .sidebar.collapsed .brand-text { display: none; }
  .nav { display: flex; flex-direction: column; gap: 2px; padding: 6px 10px; flex: 1; }
  .nav a {
    display: flex; align-items: center; gap: 10px; padding: 9px 11px; border-radius: 9px;
    color: var(--muted); font-size: 14px; font-weight: 500;
  }
  .nav a:hover { background: #161b27; color: var(--text); }
  .nav a.active { background: #182338; color: #fff; }
  .nav a.active :global(.ico) { color: var(--accent); }
  .sidebar.collapsed .nav a { justify-content: center; }
  .sidebar.collapsed .nav a span { display: none; }
  .foot { padding: 12px; border-top: 1px solid var(--border); }
  .sys-pill {
    display: flex; align-items: center; gap: 7px; font-size: 11.5px; color: var(--muted);
    background: #12151c; border: 1px solid var(--border); border-radius: 8px; padding: 8px 9px;
  }
  .sys-pill :global(.ico) { color: var(--accent); }
  .sidebar.collapsed .sys-pill span { display: none; }
  .sys-list { display: flex; flex-direction: column; gap: 5px; }
  .sys-pill b { color: var(--text); font-weight: 600; }
  .sys-pill.down { opacity: 0.65; }
  .sys-pill.low { border-color: #4a3c15; }
  .sys-pill .low-flag { margin-left: auto; display: inline-flex; }
  .sys-pill .low-flag :global(.ico) { color: #e3b341; }
  .sidebar.collapsed .sys-pill .low-flag { display: inline-flex; }
  .ver { margin-top: 7px; text-align: center; font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .sidebar.collapsed .ver-commit { display: none; }
  .ver-new {
    margin-top: 8px; width: 100%; display: inline-flex; align-items: center; justify-content: center; gap: 6px;
    background: #1c2740; border: 1px solid #33508f; color: #cdd8f2; padding: 6px; border-radius: 8px; font-size: 11.5px;
  }
  .ver-new:hover { border-color: var(--accent); color: #fff; }
  .sidebar.collapsed .ver-new span { display: none; }
  .nav-toggle {
    margin: 8px 0 0; width: 100%; display: inline-flex; align-items: center; justify-content: center;
    gap: 6px; background: transparent; border: 1px solid var(--border); color: var(--muted);
    padding: 7px; border-radius: 8px; font-size: 12px;
  }
  .nav-toggle:hover { color: var(--text); border-color: var(--accent); }
  .sidebar.collapsed .nav-toggle span { display: none; }
  .sidebar.collapsed .nav-toggle :global(.ico) { transform: rotate(180deg); }
</style>
