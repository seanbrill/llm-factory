<script lang="ts">
  // "Space nothing is using" (sheet frame f11): leftover build folders, which the
  // factory removes in one go (POST /api/storage/cleanup, refused while a build
  // runs), files no model uses and untagged images. Below, folded away, what
  // could also go: weights already inside an image, spare .tar copies. Each of
  // those is removed on its own after a confirm, or with a command when the
  // factory can't remove it for you.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { post } from "../../lib/api";
  import { refresh } from "../../lib/live.svelte";
  import { toast } from "../../lib/stores";
  import { gb, plural } from "../../lib/format";
  import { OPTIONAL, isOptional, reclaimable, type Candidate } from "../../lib/library";
  import type { StorageInfo } from "../../lib/types";

  let {
    storage,
    loaded,
    error = "",
    cands,
    onaction,
  }: {
    storage: StorageInfo | null;
    loaded: boolean;
    error?: string;
    cands: Candidate[];
    onaction: (c: Candidate) => void;
  } = $props();

  let busy = $state(false);
  let heading: HTMLHeadingElement | undefined = $state();

  const left = $derived(storage?.leftovers ?? { count: 0, gb: 0, items: [] });
  const unused = $derived(cands.filter((c) => !isOptional(c)));
  const total = $derived(reclaimable(storage, cands));
  const groups = $derived(
    OPTIONAL.map((g) => {
      const items = cands.filter((c) => c.group === g.id);
      return { ...g, items, gb: items.reduce((s, c) => s + (c.gb ?? 0), 0) };
    }).filter((g) => g.items.length),
  );

  async function cleanup() {
    busy = true;
    try {
      const r = await post<{ removed: string[]; freed_gb: number }>("/api/storage/cleanup", {});
      toast(r.removed.length ? `Removed ${plural(r.removed.length, "leftover")}, freed ${gb(r.freed_gb)}.` : "Nothing left to remove.");
    } catch (e) {
      toast((e as Error).message || "The factory didn't answer. Try again in a moment.", 7000);
    } finally {
      busy = false;
      refresh("storage");
    }
  }

  /** Bring the card into view and put focus on it (the toolbar's Clean up button). */
  export function reveal() {
    heading?.scrollIntoView({ behavior: "smooth", block: "center" });
    heading?.focus({ preventScroll: true });
  }
</script>

{#snippet item(c: Candidate)}
  <div class="cl">
    <span class="t">{c.title}</span>
    <small>{c.why}</small>
    <span class="r">
      <span class="mono">{gb(c.gb)}</span>
      {#if c.action}
        <button class="small danger" type="button" onclick={() => onaction(c)} aria-label="Delete {c.title}"><Icon name="trash" size={14} />Delete</button>
      {:else if c.cmd}
        <CopyButton text={c.cmd} label="Copy command" title={c.cmd} />
      {/if}
    </span>
    {#if c.cmd}<code class="cmd" title={c.cmd}>{c.cmd}</code>{/if}
  </div>
{/snippet}

<section class="card" aria-labelledby="cleanup-h">
  <div class="hd">
    <h2 id="cleanup-h" tabindex="-1" bind:this={heading}>Space nothing is using</h2>
    {#if loaded && !error}<span class="sub" title={storage?.busy ? "Leftovers are counted once the build queue is idle" : ""}>{storage?.busy && total === 0 ? "—" : gb(total)}</span>{/if}
    <div class="sp">
      <button
        class="small primary"
        type="button"
        disabled={busy || !left.count || storage?.busy}
        title={storage?.busy ? "A build is running; leftovers can go once the queue is idle" : left.count ? "Delete the leftover build folders" : "No leftover build folders"}
        onclick={cleanup}>
        {#if busy}<span class="spin"></span>{/if}Clean up leftovers
      </button>
    </div>
  </div>

  {#if error}
    <div class="callout er">
      <Icon name="errc" />
      <span><b>Couldn't read what's on disk.</b><p>{error}. The Library refreshes on its own; try again in a minute.</p></span>
    </div>
  {:else if !loaded}
    <p class="empty"><span class="spin"></span> Measuring the factory's folders…</p>
  {:else}
    {#if storage?.busy}
      <div class="cl">
        <span class="t">Leftover build folders</span>
        <small>A build is running and uses its folder, so leftovers are listed once the queue is idle.</small>
        <span class="r"><span class="mono">—</span></span>
      </div>
    {:else if left.count}
      <div class="cl">
        <span class="t">{plural(left.count, "leftover build folder")}</span>
        <small>From builds that stopped before finishing. Safe to delete.</small>
        <span class="r"><span class="mono">{gb(left.gb)}</span></span>
        {#if left.items.length}
          <details class="what">
            <summary>What's in it</summary>
            <ul>{#each left.items as it (it.name)}<li><span class="mono">{it.name}</span><span class="mono">{gb(it.gb)}</span></li>{/each}</ul>
          </details>
        {/if}
      </div>
    {/if}

    {#each unused as c (c.key)}{@render item(c)}{/each}

    {#if !storage?.busy && !left.count && !unused.length}
      <p class="empty ok"><Icon name="check" size={15} /> Nothing here is unused: every file belongs to a model you have.</p>
    {/if}

    {#if groups.length}
      <div class="opt">
        <h3 class="lbl">Could also go</h3>
        {#each groups as g (g.id)}
          <details class="grp">
            <summary>
              <Icon name="chevronRight" size={14} />
              <span class="gt"><b>{g.title}</b><small>{plural(g.items.length, "item")} · {g.why}</small></span>
              <span class="mono">{gb(g.gb)}</span>
            </summary>
            <div class="gi">{#each g.items as c (c.key)}{@render item(c)}{/each}</div>
          </details>
        {/each}
      </div>
    {/if}

    {#if storage && storage.media.count}
      <p class="foot">The Playground's generated images, video and audio take {gb(storage.media.gb)} in media/ ({plural(storage.media.count, "file")}); they're kept so chats reload with them.</p>
    {/if}
  {/if}
</section>

<style>
  .card { padding: 16px 18px; }
  .hd { margin-bottom: 10px; }
  .hd h2:focus { outline: none; }
  .hd h2:focus-visible { outline: 2px solid var(--pr); outline-offset: 3px; border-radius: 4px; }
  .cl {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 2px 14px;
    padding: 10px 0;
    border-top: 1px solid var(--ln);
    font-size: 13px;
    align-items: center;
  }
  .t { grid-column: 1; min-width: 0; overflow-wrap: anywhere; }
  .cl > small { color: var(--mu); font-size: 12px; grid-column: 1; overflow-wrap: anywhere; }
  .cl > .r { grid-row: 1 / 3; grid-column: 2; }
  .cl .mono { color: var(--ink); font-size: 12.5px; }
  .r { display: flex; align-items: center; gap: 10px; justify-content: flex-end; }
  .cmd {
    grid-column: 1;
    font: 11.5px var(--font-mono);
    color: var(--fa);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .what { grid-column: 1 / -1; margin-top: 4px; font-size: 12.5px; }
  .what summary { cursor: pointer; color: var(--mu); width: fit-content; }
  ul { list-style: none; margin: 6px 0 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
  li { display: flex; justify-content: space-between; gap: 12px; color: var(--mu); }
  li .mono { font-size: 12px; overflow-wrap: anywhere; }
  .empty { display: flex; align-items: center; gap: 8px; padding: 10px 0 4px; border-top: 1px solid var(--ln); }
  .empty.ok :global(.ico) { color: var(--ok); flex: none; }

  .opt { margin-top: 12px; display: flex; flex-direction: column; gap: 6px; }
  .opt h3 { margin: 0 0 2px; }
  .grp { border: 1px solid var(--ln); border-radius: var(--r); background: var(--p1); }
  .grp summary {
    list-style: none;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 12px;
    cursor: pointer;
    border-radius: var(--r);
  }
  .grp summary::-webkit-details-marker { display: none; }
  .grp summary:hover { background: var(--p2); }
  .grp summary:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .grp summary :global(.ico) { color: var(--fa); flex: none; transition: transform 0.15s; }
  .grp[open] summary :global(.ico) { transform: rotate(90deg); }
  .gt { display: flex; flex-direction: column; gap: 1px; min-width: 0; flex: 1; }
  .gt b { font-size: 13px; font-weight: 600; }
  .gt small { color: var(--mu); font-size: 12px; }
  .grp summary > .mono { font-size: 12.5px; color: var(--ink); white-space: nowrap; }
  .gi { padding: 0 12px 2px 36px; }
  .foot { color: var(--fa); font-size: 12px; margin-top: 12px; }
  .hd .sub { white-space: nowrap; }
  @media (max-width: 760px) {
    .hd { flex-wrap: wrap; row-gap: 8px; }
    .cl { grid-template-columns: 1fr; }
    .cl > .r { grid-row: auto; grid-column: 1; justify-content: flex-start; }
    .gi { padding-left: 12px; }
  }
</style>
