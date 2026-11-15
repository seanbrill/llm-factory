<script lang="ts">
  // Phone tab bar (sheet frame f16): the four places you check from a phone,
  // plus More for the rest.
  import Icon from "../Icon.svelte";
  import { PAGES, route, navParent, href } from "../../lib/router";
  import { build } from "../../lib/build.svelte";
  import { counts } from "../../lib/jobs";
  import { ui, openPalette } from "../../lib/ui.svelte";

  const TABS = ["overview", "builds", "library", "catalog"];
  const active = $derived(navParent($route));
  const c = $derived(counts(build.jobs));
  const more = PAGES.filter((p) => !TABS.includes(p.id) && p.id !== "plan");
  const inMore = $derived(!TABS.includes(active));

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") ui.moreOpen = false;
  }
</script>

<svelte:window onkeydown={ui.moreOpen ? onKey : undefined} />

<nav class="tabs" aria-label="Main">
  {#each TABS as id (id)}
    {@const p = PAGES.find((x) => x.id === id)!}
    <a class="tab" class:on={active === id} href={href(id)} aria-current={active === id ? "page" : undefined}>
      <Icon name={p.icon} size={20} />{p.label}
      {#if id === "builds" && c.active}<em>{c.active}</em>{/if}
    </a>
  {/each}
  <button class="tab" class:on={inMore || ui.moreOpen} onclick={() => (ui.moreOpen = !ui.moreOpen)} aria-expanded={ui.moreOpen}>
    <Icon name="more" size={20} />More
  </button>
</nav>

{#if ui.moreOpen}
  <button class="scrim" aria-label="Close" onclick={() => (ui.moreOpen = false)}></button>
  <div class="sheet" role="dialog" aria-label="More pages">
    {#each more as p (p.id)}
      <a href={href(p.id)} class:on={active === p.id} onclick={() => (ui.moreOpen = false)}>
        <Icon name={p.icon} size={18} /><span><b>{p.label}</b><small>{p.blurb}</small></span>
      </a>
    {/each}
    <button class="srch" onclick={() => { ui.moreOpen = false; openPalette(); }}>
      <Icon name="find" size={18} /><span><b>Search and commands</b><small>Build a model, stop everything, copy a fix…</small></span>
    </button>
  </div>
{/if}

<style>
  .tabs { display: none; }
  .scrim, .sheet { display: none; }
  @media (max-width: 760px) {
    .tabs {
      position: fixed;
      left: 0;
      right: 0;
      bottom: 0;
      z-index: 40;
      display: grid;
      grid-template-columns: repeat(5, 1fr);
      padding: 8px 6px calc(8px + env(safe-area-inset-bottom, 0px));
      border-top: 1px solid var(--ln);
      background: var(--p1);
    }
    .tab {
      display: flex;
      flex-direction: column;
      align-items: center;
      gap: 3px;
      font-size: 11px;
      color: var(--fa);
      position: relative;
      text-decoration: none;
      background: none;
      border: 0;
      min-height: 0;
      padding: 2px 0;
      font-weight: 500;
    }
    .tab:hover { background: none; color: var(--mu); }
    .tab.on { color: var(--pr); }
    .tab em {
      position: absolute;
      top: -4px;
      left: 56%;
      font-style: normal;
      font-size: 10px;
      font-weight: 700;
      background: var(--pr);
      color: var(--onpr);
      border-radius: 99px;
      padding: 0 5px;
    }
    .scrim {
      display: block;
      position: fixed;
      inset: 0;
      z-index: 38;
      background: color-mix(in srgb, var(--cv) 70%, transparent);
      border: 0;
      border-radius: 0;
      min-height: 0;
      padding: 0;
    }
    .sheet {
      display: flex;
      flex-direction: column;
      gap: 2px;
      position: fixed;
      left: 8px;
      right: 8px;
      bottom: calc(68px + env(safe-area-inset-bottom, 0px));
      z-index: 39;
      background: var(--p2);
      border: 1px solid var(--ln2);
      border-radius: 18px;
      padding: 8px;
      box-shadow: var(--shadow);
    }
    .sheet a, .sheet button {
      display: flex;
      align-items: center;
      gap: 12px;
      padding: 10px;
      border-radius: 12px;
      color: var(--ink);
      text-decoration: none;
      background: none;
      border: 0;
      text-align: left;
      justify-content: flex-start;
      white-space: normal;
      min-height: 0;
      font-weight: 400;
    }
    .sheet a.on { background: var(--p3); }
    .sheet a :global(.ico), .sheet button :global(.ico) { color: var(--mu); }
    .sheet span { display: flex; flex-direction: column; min-width: 0; }
    .sheet b { font-weight: 600; font-size: 14px; }
    .sheet small { color: var(--mu); font-size: 12px; }
  }
</style>
