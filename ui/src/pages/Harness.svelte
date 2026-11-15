<script lang="ts">
  // Harness (sheet frame f12): tools for local models in Open WebUI. The model
  // gateway, the workspace terminal, Open WebUI setup, what a model can do in a
  // chat, skills, tools baked into an image, and the manual steps. Status comes
  // from live.harness.
  import Icon from "../components/Icon.svelte";
  import Hero from "../components/harness/Hero.svelte";
  import GatewayCard from "../components/harness/GatewayCard.svelte";
  import TerminalCard from "../components/harness/TerminalCard.svelte";
  import OpenWebUICard from "../components/harness/OpenWebUICard.svelte";
  import ToolsCard from "../components/harness/ToolsCard.svelte";
  import SkillsCard from "../components/harness/SkillsCard.svelte";
  import BakedCard from "../components/harness/BakedCard.svelte";
  import ManualSetup from "../components/harness/ManualSetup.svelte";
  import { live, watch, refresh } from "../lib/live.svelte";
  import { routeArg } from "../lib/router";
  import { jump } from "../components/harness/state.svelte";
  import { sentence } from "../lib/harness";

  $effect(() => watch("harness"));

  const st = $derived(live.harness);
  const err = $derived(live.errors.harness);

  // While the terminal starts, follow it closely.
  const starting = $derived(!!st?.terminal?.op?.active);
  $effect(() => {
    if (!starting) return;
    const t = setInterval(() => refresh("harness"), 1200);
    return () => clearInterval(t);
  });

  // config/harness.json couldn't be read: the status carries no gateway or terminal.
  const noStore = $derived(!!st?.config_error && !st.terminal?.variant);

  // "#harness/connect" opens on the Open WebUI setup, "#harness/baked" on the
  // tools inside an image.
  let jumped = false;
  $effect(() => {
    if (jumped || !st) return;
    if ($routeArg === "baked" || $routeArg === "tools") {
      jumped = true;
      queueMicrotask(() => jump("h-baked"));
    } else if (!noStore && ($routeArg === "connect" || $routeArg === "openwebui")) {
      jumped = true;
      queueMicrotask(() => jump("h-owui", "input[type=password]"));
    }
  });

  let retrying = $state(false);
  async function retry() {
    retrying = true;
    await refresh("harness");
    retrying = false;
  }
</script>

{#if !st && !live.loaded.harness}
  <div class="card state" aria-busy="true">
    <span class="spin" aria-hidden="true"></span>
    <span>Loading the gateway, terminal and skills…</span>
  </div>
{:else if !st}
  <div class="card state">
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span>
        <b>The harness status didn't load.</b>
        <p>{sentence(err) || "The factory didn't answer."} Check that the factory is running, then try again.</p>
        <button class="small" onclick={retry} disabled={retrying}>
          {#if retrying}<span class="spin sm" aria-hidden="true"></span>{:else}<Icon name="retry" size={14} />{/if}Try again
        </button>
      </span>
    </div>
  </div>
{:else}
  <div class="hp">
    {#if err}
      <div class="callout wa" role="status">
        <Icon name="warn" size={16} />
        <span>
          <b>Lost touch with the factory: this is what it said last.</b>
          <p>{sentence(err)}</p>
          <button class="small" onclick={retry} disabled={retrying}>Try again</button>
        </span>
      </div>
    {/if}

    {#if st.config_error}
      <div class="callout er" role="alert">
        <Icon name="errc" size={16} />
        <span>
          {#if noStore}
            <b>The harness settings can't be read.</b>
            <p>
              {sentence(st.config_error.replace(/;\s*fix it or delete it to start fresh\.?$/, ""))} Fix or delete it, then restart the
              factory: the gateway, the terminal and Open WebUI setup come back. Deleting it starts fresh, with new keys.
            </p>
          {:else}
            <b>Changes to the harness settings can't be saved.</b>
            <p>{sentence(st.config_error.replace(/^settings can't be saved: /, ""))}</p>
          {/if}
        </span>
      </div>
    {/if}

    {#if noStore}
      <SkillsCard {st} />
      <BakedCard />
    {:else}
      <Hero {st} />

      <div class="three">
        <GatewayCard {st} />
        <TerminalCard {st} />
        <OpenWebUICard {st} />
      </div>

      <div class="two">
        <ToolsCard {st} />
        <SkillsCard {st} />
      </div>

      <BakedCard />

      <ManualSetup {st} />
    {/if}
  </div>
{/if}

<style>
  .hp { display: flex; flex-direction: column; gap: 16px; }
  .three { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
  .two { display: grid; grid-template-columns: minmax(0, 1.25fr) minmax(0, 1fr); gap: 14px; }
  .state { display: flex; align-items: center; gap: 10px; color: var(--mu); font-size: 13px; }
  .state .callout { flex: 1; }
  .callout button { margin-top: 8px; }
  .spin.sm { width: 12px; height: 12px; }

  @media (max-width: 1240px) {
    .three { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .three > :global(:last-child) { grid-column: 1 / -1; }
  }
  @media (max-width: 900px) {
    .two { grid-template-columns: minmax(0, 1fr); }
  }
  @media (max-width: 760px) {
    .hp { gap: 12px; padding-top: 12px; }
    .three { grid-template-columns: minmax(0, 1fr); gap: 12px; }
    .two { gap: 12px; }
  }
</style>
