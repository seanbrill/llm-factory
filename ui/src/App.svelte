<script lang="ts">
  // App shell (knitted sheet ds_01M7EKYBPS2BQ57JNQHB): rail + top bar on a
  // desktop, brand header + tab bar on a phone, ⌘K palette everywhere.
  import Rail from "./components/shell/Rail.svelte";
  import Topbar from "./components/shell/Topbar.svelte";
  import TabBar from "./components/shell/TabBar.svelte";
  import CommandPalette from "./components/shell/CommandPalette.svelte";
  import LockScreen from "./components/shell/LockScreen.svelte";
  import Toast from "./components/Toast.svelte";
  import Overview from "./pages/Overview.svelte";
  import Catalog from "./pages/Catalog.svelte";
  import Plan from "./pages/Plan.svelte";
  import Builds from "./pages/Builds.svelte";
  import Library from "./pages/Library.svelte";
  import Harness from "./pages/Harness.svelte";
  import Tune from "./pages/Tune.svelte";
  import Playground from "./pages/Playground.svelte";
  import Ensembles from "./pages/Ensembles.svelte";
  import Personas from "./pages/Personas.svelte";
  import System from "./pages/System.svelte";
  import Help from "./pages/Help.svelte";
  import { route, PAGES, go } from "./lib/router";
  import { loadSysInfo, loadVersion, loadCatalog, loadPersonas, loadEnsembles } from "./lib/stores";
  import { watchQueue } from "./lib/build.svelte";
  import { ui, openPalette, closePalette, recheckVersion } from "./lib/ui.svelte";
  import { auth, checkAuth } from "./lib/auth.svelte";

  checkAuth();
  loadSysInfo();
  loadVersion();
  loadCatalog();
  loadPersonas();
  loadEnsembles();
  // The queue drives the top bar and nav counts on every page. watchQueue polls
  // every second while builds run; this slow tick notices builds started
  // elsewhere (another tab, ⌘K) while idle.
  watchQueue();

  // "?q=build qwen" opens the palette with that search (a shareable deep link).
  {
    const seed = new URLSearchParams(location.search).get("q");
    if (seed !== null) openPalette(seed);
  }

  $effect(() => {
    const versionTimer = setInterval(recheckVersion, 60_000);
    const queueTimer = setInterval(() => {
      if (!document.hidden) watchQueue();
    }, 15_000);
    window.addEventListener("focus", recheckVersion);
    return () => {
      clearInterval(versionTimer);
      clearInterval(queueTimer);
      window.removeEventListener("focus", recheckVersion);
    };
  });

  // ⌘K / Ctrl+K: palette from anywhere. Without a modifier, when not typing:
  // "/" palette, "?" help, and each page's letter (o, b, q, l, p, s).
  function onKey(e: KeyboardEvent) {
    if ((e.metaKey || e.ctrlKey) && !e.altKey && e.key.toLowerCase() === "k") {
      e.preventDefault();
      if (ui.paletteOpen) closePalette();
      else openPalette();
      return;
    }
    if (ui.paletteOpen || auth.locked || e.metaKey || e.ctrlKey || e.altKey || e.repeat) return;
    const t = e.target as HTMLElement | null;
    if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT|BUTTON)$/.test(t.tagName))) return;
    if (document.querySelector("[aria-modal='true'], .modal")) return;
    if (e.key === "/") {
      e.preventDefault();
      openPalette();
    } else if (e.key === "?") {
      go("help");
    } else {
      const p = PAGES.find((x) => x.key === e.key.toLowerCase());
      if (p) go(p.id);
    }
  }

  let pageEl: HTMLElement | undefined = $state();
  $effect(() => {
    $route;
    pageEl?.scrollTo({ top: 0 });
  });
</script>

<svelte:window onkeydown={onKey} />

<div class="app">
  <Rail />
  <div class="main">
    <Topbar />
    <main class="page" bind:this={pageEl}>
      <div class="inner" class:wide={$route === "playground"}>
        {#if $route === "catalog"}
          <Catalog />
        {:else if $route === "plan"}
          <Plan />
        {:else if $route === "builds"}
          <Builds />
        {:else if $route === "library"}
          <Library />
        {:else if $route === "tune"}
          <Tune />
        {:else if $route === "harness"}
          <Harness />
        {:else if $route === "playground"}
          <Playground />
        {:else if $route === "ensembles"}
          <Ensembles />
        {:else if $route === "personas"}
          <Personas />
        {:else if $route === "system"}
          <System />
        {:else if $route === "help"}
          <Help />
        {:else}
          <Overview />
        {/if}
      </div>
    </main>
  </div>
  <TabBar />
  {#if ui.paletteOpen}<CommandPalette />{/if}
  <Toast />
  {#if auth.locked}<LockScreen />{/if}
</div>

<style>
  .app {
    display: grid;
    grid-template-columns: 236px minmax(0, 1fr);
    height: 100vh;
    height: 100dvh;
    background: var(--cv);
    color: var(--ink);
  }
  .main { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .page { flex: 1; min-height: 0; overflow-y: auto; padding: 24px 28px 40px; }
  .inner { max-width: 1480px; margin: 0 auto; width: 100%; }
  .inner.wide { max-width: none; }
  @media (max-width: 1100px) {
    .app { grid-template-columns: 64px minmax(0, 1fr); }
    .page { padding: 20px 20px 36px; }
  }
  @media (max-width: 760px) {
    .app { display: block; height: auto; min-height: 100dvh; }
    .app > :global(.rail) { display: none; }
    .main { min-height: 100dvh; }
    .page { overflow: visible; padding: 0 16px calc(96px + env(safe-area-inset-bottom, 0px)); }
  }
</style>
