<script lang="ts">
  // The selection tray (sheet frame f8): what's in the build plan, what it will
  // download and how long it should take, and the way to review it.
  import Icon from "../Icon.svelte";
  import { gb, dur } from "../../lib/format";
  import { href } from "../../lib/router";
  import { shortName } from "../../lib/catalog";
  import type { Model } from "../../lib/types";

  let {
    models,
    downloadGB,
    eta,
    checking,
    blocked,
    onclear,
  }: {
    models: Model[];
    downloadGB: number | null; // null = not known yet
    eta: number;               // seconds until the plan is built, 0 = unknown
    checking: boolean;
    blocked: number;           // builds that can't start as planned
    onclear: () => void;
  } = $props();

  const shown = $derived(models.slice(0, 3));
</script>

<div class="tray" role="region" aria-label="Your build plan">
  <span class="n">{models.length} selected</span>
  <span class="picked">
    {#each shown as m (m.id)}<span class="chip">{shortName(m)}</span>{/each}
    {#if models.length > shown.length}<span class="chip">+{models.length - shown.length}</span>{/if}
  </span>
  <span class="sep"></span>
  <span class="meta" aria-live="polite">
    {#if downloadGB == null}
      {checking ? "Checking…" : ""}
    {:else if downloadGB > 0}
      <b>{gb(downloadGB)} to download</b>
    {:else}
      <b>Weights already on disk</b>
    {/if}
    {#if eta > 0}<span>{" · about " + dur(eta)}</span>{/if}
    {#if blocked}<span class="wa">{` · ${blocked} can't build yet`}</span>{/if}
  </span>
  <button class="ghost" type="button" onclick={onclear}>Clear</button>
  <a class="btn pri" href={href("plan")}>Review build plan<Icon name="arrowRight" size={15} /></a>
</div>

<style>
  .tray {
    position: sticky;
    bottom: 20px;
    z-index: 20;
    margin: 18px auto 0;
    width: max-content;
    max-width: 100%;
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 9px 9px 9px 18px;
    background: var(--p2);
    border: 1px solid var(--ln2);
    border-radius: 16px;
    box-shadow: var(--shadow);
    white-space: nowrap;
  }
  .n { font-family: var(--font-display); font-weight: 700; font-size: 15px; }
  .picked { display: flex; gap: 6px; min-width: 0; overflow: hidden; }
  .meta { font-size: 13px; color: var(--mu); min-width: 0; overflow: hidden; text-overflow: ellipsis; }
  .meta b { color: var(--ink); font-weight: 600; }
  .meta .wa { color: var(--wa); }
  .sep { width: 1px; height: 24px; background: var(--ln2); flex: none; }
  @media (max-width: 1100px) {
    .picked { display: none; }
  }
  @media (max-width: 760px) {
    .tray {
      bottom: calc(80px + env(safe-area-inset-bottom, 0px));
      width: 100%;
      gap: 10px;
      padding: 8px 8px 8px 14px;
    }
    .sep, .ghost { display: none; }
    .meta { flex: 1; font-size: 12px; }
    .btn { padding: 0 12px; }
  }
</style>
