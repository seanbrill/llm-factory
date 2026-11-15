<script lang="ts">
  // Harness at a glance (sheet frame f1): the model gateway, the workspace
  // terminal and the skills, each with its state; Manage opens the Harness page.
  import Icon from "../Icon.svelte";
  import { live } from "../../lib/live.svelte";
  import { href } from "../../lib/router";
  import { harnessChip, harnessRows } from "../../lib/overview";
  import type { Tone } from "../../lib/harness";

  const h = $derived(live.harness);
  const rows = $derived(h ? harnessRows(h) : []);
  const chip = $derived(h ? harnessChip(h, rows) : null);

  const ICON: Record<Tone, string> = { ok: "check", warn: "warn", err: "errc", idle: "minus", busy: "clock" };
</script>

<div class="card">
  <div class="hd">
    <h2>Harness</h2>
    {#if chip}<span class={chip.cls}>{chip.text}</span>{/if}
    <div class="sp"><a class="btn sm" href={href("harness")}>Manage</a></div>
  </div>
  {#if !h && !live.loaded.harness}
    <p class="sub state"><span class="spin"></span>Reading the harness…</p>
  {:else if !h}
    <p class="sub state">Couldn't read the harness{live.errors.harness ? ` (${live.errors.harness})` : ""}. Open the Harness page to set it up.</p>
  {:else}
    <div class="list">
      {#each rows as r (r.key)}
        <div class="li {r.tone}">
          <Icon name={ICON[r.tone]} size={15} />
          <span class="lb">{r.label}</span>
          <span class="r" class:mono={r.mono}>{r.value}</span>
        </div>
      {/each}
    </div>
    {#if h.config_error}<p class="cfg">The harness config has an error: {h.config_error}</p>{/if}
  {/if}
</div>

<style>
  .list { display: flex; flex-direction: column; gap: 9px; font-size: 13px; }
  .li { display: flex; align-items: center; gap: 9px; min-width: 0; }
  .li :global(.ico) { color: var(--ok); }
  .li.warn :global(.ico) { color: var(--wa); }
  .li.err :global(.ico) { color: var(--er); }
  .li.idle :global(.ico), .li.busy :global(.ico) { color: var(--fa); }
  .lb { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .r { margin-left: auto; color: var(--mu); font-size: 12.5px; white-space: nowrap; }
  .li.warn .r { color: var(--wa); }
  .li.err .r { color: var(--er); }
  .state { display: flex; align-items: center; gap: 8px; }
  .cfg { margin-top: 10px; font-size: 12.5px; color: var(--er); }
</style>
