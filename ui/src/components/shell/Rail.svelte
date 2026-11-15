<script lang="ts">
  // Desktop navigation rail (sheet frame f1): brand + build, grouped nav with the
  // live build count, and a footer for what needs a click (a newer factory build).
  import Icon from "../Icon.svelte";
  import Mark from "../Mark.svelte";
  import { PAGES, NAV_GROUPS, route, navParent, href } from "../../lib/router";
  import { appVersion } from "../../lib/stores";
  import { build } from "../../lib/build.svelte";
  import { counts } from "../../lib/jobs";
  import { ui, stale, verLabel } from "../../lib/ui.svelte";

  const active = $derived(navParent($route));
  const c = $derived(counts(build.jobs));
  const isStale = $derived(ui.latest && stale());
</script>

<aside class="rail" aria-label="Main navigation">
  <a class="brand" href={href("overview")} title="LLM Factory — {verLabel($appVersion)}">
    <Mark size={32} />
    <span class="bt">
      <b>LLM Factory</b>
      {#if $appVersion}<small>{verLabel($appVersion)}</small>{/if}
    </span>
  </a>

  <nav class="nav">
    {#each NAV_GROUPS as g (g.group)}
      {#if g.label}<p class="navlbl">{g.label}</p>{/if}
      {#each PAGES.filter((p) => p.group === g.group) as p (p.id)}
        <a href={href(p.id)} class:on={active === p.id} aria-current={active === p.id ? "page" : undefined} title={p.label}>
          <Icon name={p.icon} size={18} />
          <span class="nl">{p.label}</span>
          {#if p.id === "builds" && c.active}
            <span class="ct" title="{c.running} running, {c.queued} queued">{c.active}</span>
          {:else if p.id === "builds" && c.failed}
            <span class="ct er" title="{c.failed} failed">{c.failed}</span>
          {/if}
        </a>
      {/each}
    {/each}
  </nav>

  <div class="foot">
    {#if isStale && ui.latest}
      <button class="upd" onclick={() => location.reload()} title="The factory restarted on a newer build; this page is still the old one.">
        <Icon name="retry" size={15} /><span>Updated to {verLabel(ui.latest)} — reload</span>
      </button>
    {/if}
    <a class="help" href={href("help")} title="How the factory works">
      <Icon name="help" size={16} /><span>Help & concepts</span><span class="kbd">?</span>
    </a>
  </div>
</aside>

<style>
  .rail {
    background: var(--p1);
    border-right: 1px solid var(--ln);
    display: flex;
    flex-direction: column;
    padding: 16px 12px;
    min-height: 0;
    overflow-y: auto;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 2px 8px 20px;
    color: var(--ink);
    text-decoration: none;
  }
  .brand :global(svg) { flex: none; }
  .bt { display: flex; flex-direction: column; min-width: 0; }
  .bt b {
    font-family: var(--font-display);
    font-size: 17px;
    font-weight: 700;
    letter-spacing: -0.01em;
    line-height: 1.1;
  }
  .bt small {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fa);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .nav { display: flex; flex-direction: column; gap: 2px; }
  .nav a {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-radius: var(--rs);
    color: var(--mu);
    font-weight: 500;
    text-decoration: none;
  }
  .nav a:hover { background: var(--p2); color: var(--ink); }
  .nav a.on { background: var(--p3); color: var(--ink); }
  .nav a.on :global(.ico) { color: var(--pr); }
  .ct {
    margin-left: auto;
    font-family: var(--font-mono);
    font-size: 11px;
    background: var(--prs);
    color: var(--pr);
    padding: 1px 7px;
    border-radius: 99px;
  }
  .ct.er { background: var(--ers); color: var(--er); }
  .navlbl {
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    padding: 18px 10px 6px;
  }
  .foot { margin-top: auto; padding-top: 16px; display: flex; flex-direction: column; gap: 8px; }
  .upd {
    justify-content: flex-start;
    background: var(--prs);
    border-color: color-mix(in srgb, var(--pr) 45%, transparent);
    color: var(--ink);
    font-size: 12.5px;
    white-space: normal;
    text-align: left;
    padding: 8px 10px;
  }
  .help {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 8px 10px;
    border-radius: var(--rs);
    color: var(--fa);
    font-size: 13px;
    text-decoration: none;
  }
  .help:hover { background: var(--p2); color: var(--ink); }
  .help .kbd { margin-left: auto; }

  /* Medium widths: icons only. */
  @media (max-width: 1100px) {
    .rail { padding: 14px 8px; align-items: center; }
    .brand { padding: 2px 0 18px; }
    .bt, .nl, .navlbl, .help span, .upd span { display: none; }
    .nav a { justify-content: center; padding: 9px; position: relative; }
    .ct { position: absolute; top: 2px; right: 0; padding: 0 5px; font-size: 10px; }
    .help { justify-content: center; }
    .upd { justify-content: center; padding: 8px; }
  }
</style>
