<script lang="ts">
  import Sidebar from "./components/Sidebar.svelte";
  import Toast from "./components/Toast.svelte";
  import Help from "./pages/Help.svelte";
  import Containers from "./pages/Containers.svelte";
  import ModelFiles from "./pages/ModelFiles.svelte";
  import Images from "./pages/Images.svelte";
  import Personas from "./pages/Personas.svelte";
  import Ensembles from "./pages/Ensembles.svelte";
  import Build from "./pages/Build.svelte";
  import Chat from "./pages/Chat.svelte";
  import { route, pageDef } from "./lib/router";
  import { loadSysInfo, loadCatalog, loadPersonas, loadEnsembles } from "./lib/stores";

  // Initial loads (the API is unchanged from the vanilla app).
  loadSysInfo();
  loadCatalog();
  loadPersonas();
  loadEnsembles();

  const def = $derived(pageDef($route));
</script>

<div class="app">
  <Sidebar />
  <div class="content">
    <header class="topbar">
      <h1>{def.title}</h1>
      <p class="sub">{def.sub}</p>
    </header>
    <main class:wide={$route === "chat"}>
      {#if $route === "images"}
        <Images />
      {:else if $route === "containers"}
        <Containers />
      {:else if $route === "models"}
        <ModelFiles />
      {:else if $route === "personas"}
        <Personas />
      {:else if $route === "ensembles"}
        <Ensembles />
      {:else if $route === "chat"}
        <Chat />
      {:else if $route === "help"}
        <Help />
      {:else}
        <Build />
      {/if}
    </main>
  </div>
  <Toast />
</div>

<style>
  .app { display: flex; min-height: 100vh; }
  .content { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .topbar { padding: 22px 28px 12px; border-bottom: 1px solid var(--border); }
  .topbar h1 { font-size: 22px; }
  .sub { margin: 4px 0 0; color: var(--muted); }
  main { max-width: 1060px; width: 100%; margin: 0; padding: 22px 28px 48px; display: block; }
  main.wide { max-width: none; }
</style>
