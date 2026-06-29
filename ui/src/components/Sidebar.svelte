<script lang="ts">
  import Icon from "./Icon.svelte";
  import { PAGES, route } from "../lib/router";
  import { sysinfo } from "../lib/stores";

  let collapsed = $state(localStorage.getItem("llmfactory.nav.collapsed") === "1");
  function toggle() {
    collapsed = !collapsed;
    localStorage.setItem("llmfactory.nav.collapsed", collapsed ? "1" : "0");
  }

  let sysText = $derived.by(() => {
    const s = $sysinfo;
    if (!s || !s.mem_gb) return "Detecting system…";
    const gpu = s.gpu ? `${s.gpu} GPU${s.vram_gb ? " " + s.vram_gb + " GB" : ""}` : "CPU only";
    return `${s.mem_gb.toFixed(0)} GB · ${s.cpus} CPU · ${gpu}`;
  });
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
    <div class="sys-pill"><Icon name="cpu" size={14} /> <span>{sysText}</span></div>
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
  .nav-toggle {
    margin: 8px 0 0; width: 100%; display: inline-flex; align-items: center; justify-content: center;
    gap: 6px; background: transparent; border: 1px solid var(--border); color: var(--muted);
    padding: 7px; border-radius: 8px; font-size: 12px;
  }
  .nav-toggle:hover { color: var(--text); border-color: var(--accent); }
  .sidebar.collapsed .nav-toggle span { display: none; }
  .sidebar.collapsed .nav-toggle :global(.ico) { transform: rotate(180deg); }
</style>
