<script lang="ts">
  // Disk (sheet frame f1): what the factory keeps on disk — weights, saved
  // image tars, leftover build folders, generated media — and the free space.
  // Leftovers are safe to remove; when space is short the Library has the rest.
  import Icon from "../Icon.svelte";
  import { post } from "../../lib/api";
  import { toast } from "../../lib/stores";
  import { live, refresh } from "../../lib/live.svelte";
  import { href } from "../../lib/router";
  import { gb, plural } from "../../lib/format";
  import { diskParts, diskShort } from "../../lib/overview";

  const s = $derived(live.storage);
  const parts = $derived(s ? diskParts(s) : []);
  const total = $derived(parts.reduce((a, p) => a + p.gb, 0));
  const short = $derived(diskShort(s));
  let busy = $state(false);

  async function cleanup() {
    busy = true;
    try {
      const r = await post<{ removed: string[]; freed_gb: number }>("/api/storage/cleanup", {});
      toast(r.removed.length ? `Removed ${plural(r.removed.length, "leftover")}, freed ${gb(r.freed_gb)}.` : "Nothing to clean up.");
      refresh("storage");
    } catch (e) {
      toast((e as Error).message, 7000);
    } finally {
      busy = false;
    }
  }
</script>

<div class="card">
  <div class="hd">
    <h2>Disk</h2>
    {#if s}
      <span
        class="sub tot"
        title="The factory's files take {gb(total)}{s.disk_total_gb ? ` of a ${gb(s.disk_total_gb)} disk` : ''}{s.disk_free_gb != null ? `, which has ${gb(s.disk_free_gb)} free` : ''}.">
        {gb(total)} used{#if s.disk_free_gb != null}{" · "}<span class:wa={short}>{gb(s.disk_free_gb)} free</span>{/if}
      </span>
    {/if}
    {#if s && s.leftovers?.gb > 0}
      <div class="sp">
        <button class="small" onclick={cleanup} disabled={busy}>
          {#if busy}<span class="spin"></span>{/if}Clean up {gb(s.leftovers.gb)}
        </button>
      </div>
    {/if}
  </div>
  {#if !s && !live.loaded.storage}
    <p class="sub state"><span class="spin"></span>Measuring the disk…</p>
  {:else if !s}
    <p class="sub state">Couldn't measure the disk{live.errors.storage ? ` (${live.errors.storage})` : ""}. Try again in a moment.</p>
  {:else}
    {#if total > 0}
      <div class="segs" role="img" aria-label={parts.map((p) => `${p.label}: ${gb(p.gb)}`).join(", ")}>
        {#each parts as p (p.key)}
          {#if p.gb > 0}<i style="width:{((p.gb / total) * 100).toFixed(2)}%;background:{p.color}"></i>{/if}
        {/each}
      </div>
    {/if}
    <div class="list">
      {#each parts as p (p.key)}
        <div class="li"><span class="sq" style="background:{p.color}"></span><span class="lb">{p.label}</span><span class="r mono">{p.gb > 0 ? gb(p.gb) : "—"}</span></div>
      {/each}
    </div>
    {#if s.busy}
      <p class="note">Leftover build folders are counted again when no build is running.</p>
    {/if}
    {#if short}
      <div class="callout wa short">
        <Icon name="warn" size={16} />
        <span><b>Only {gb(s.disk_free_gb)} free.</b><p>The next build may not fit. <a href={href("library")}>Remove weights and images you don't use</a> in the Library.</p></span>
      </div>
    {/if}
  {/if}
</div>

<style>
  .tot { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .tot .wa { color: var(--wa); }
  .segs {
    display: flex;
    height: 10px;
    border-radius: 99px;
    overflow: hidden;
    background: var(--p3);
    margin: 4px 0 12px;
    gap: 2px;
  }
  .segs i { display: block; height: 100%; min-width: 3px; }
  .list { display: flex; flex-direction: column; gap: 9px; font-size: 13px; }
  .li { display: flex; align-items: center; gap: 9px; min-width: 0; }
  .lb { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .sq { width: 8px; height: 8px; border-radius: 2px; flex: none; }
  .r { margin-left: auto; color: var(--mu); white-space: nowrap; }
  .note { margin-top: 10px; font-size: 12px; color: var(--fa); }
  .short { margin-top: 12px; }
  .state { display: flex; align-items: center; gap: 8px; }
  .spin { width: 13px; height: 13px; }
</style>
