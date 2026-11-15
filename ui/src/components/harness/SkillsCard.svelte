<script lang="ts">
  // Skills (sheet frame f12): every skill in the factory's skills/ folder, which
  // the terminal shares read-only, with what's wrong with any that won't load.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { skillCounts, firstSentence, homeOf, tildePath, sentence, type HarnessStatus } from "../../lib/harness";

  let { st }: { st: HarnessStatus } = $props();

  const uid = $props.id();
  const sk = $derived(st.skills);
  const items = $derived(sk.items ?? []);
  const sc = $derived(skillCounts(sk));
  const home = $derived(homeOf(st.terminal));
  const folder = $derived(tildePath(sk.path, home));
  let howOpen = $state(false);
  let expanded = $state<Record<string, boolean>>({});
</script>

<section class="card" id="h-skills" tabindex="-1" aria-labelledby="{uid}-title">
  <div class="hd">
    <h2 id="{uid}-title">Skills</h2>
    {#if !sk.error}<span class="chip" title="{sc.listed} of {sc.found} load">{sc.found}</span>{/if}
    <div class="sp">
      {#if sk.path}<CopyButton text={sk.path} label="Copy path" title="Copy the skills folder's path" />{/if}
      <button class="small" onclick={() => (howOpen = !howOpen)} aria-expanded={howOpen} aria-controls="{uid}-how">
        <Icon name="plus" size={14} />Add one
      </button>
    </div>
  </div>

  {#if howOpen}
    <div class="how" id="{uid}-how">
      <p>
        Make a folder in <span class="mono">{folder || "skills/"}</span> with a <span class="mono">SKILL.md</span> inside:
        front matter with a <span class="mono">name</span> (lowercase-with-hyphens, the same as the folder) and a one-line
        <span class="mono">description</span>, then the instructions. It's there in the next chat, with no restart.
      </p>
      <p class="exl">For example, <span class="mono">summarize-pdf/SKILL.md</span>:</p>
      <pre class="ex">---
name: summarize-pdf
description: Summarize a PDF in the workspace.
---
# Summarize a PDF
…</pre>
    </div>
  {/if}

  {#if sk.error}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span><b>The terminal has no skills to share.</b><p>{sentence(sk.error)}</p></span>
    </div>
  {:else if !items.length}
    <p class="empty">No skills in <span class="mono">{folder || "skills/"}</span> yet. <b>Add one</b> shows how.</p>
  {:else}
    <ul>
      {#each items as s (s.dir)}
        {@const bad = !s.listed || (s.warnings?.length ?? 0) > 0}
        <li class="sk">
          <span class="nm mono">{s.name || s.dir}</span>
          {#if s.description}
            <button
              class="ds"
              class:open={expanded[s.dir]}
              onclick={() => (expanded[s.dir] = !expanded[s.dir])}
              aria-expanded={!!expanded[s.dir]}
              title={expanded[s.dir] ? "Show less" : "Show the whole description"}>
              {expanded[s.dir] ? s.description : firstSentence(s.description)}
            </button>
          {:else}
            <small class="ds none">No description, so the model won't see it.</small>
          {/if}
          {#if bad}
            <span class="chip wa" title="Open Terminal won't list it: see why below">Not listed</span>
          {:else}
            <span class="chip">Ready</span>
          {/if}
          {#each s.warnings ?? [] as w}
            <p class="warn"><Icon name="warn" size={13} />{sentence(w)}</p>
          {/each}
        </li>
      {/each}
    </ul>
  {/if}

  <p class="fine">
    In a chat with the terminal selected, the model sees each skill's name and description and loads the rest with
    <span class="mono">view_skill</span> when a task matches. You can also ask for one by name.
    {#if st.terminal.share_claude_skills && st.terminal.claude_skills_path}
      Your Claude skills in <span class="mono">{tildePath(st.terminal.claude_skills_path, home)}</span> are shared too,
      read-only; they aren't listed here.
    {/if}
  </p>
</section>

<style>
  .card { display: flex; flex-direction: column; min-width: 0; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .hd { margin-bottom: 10px; flex-wrap: wrap; }
  .hd .sp :global(.btn) { min-height: 28px; }
  ul { list-style: none; margin: 0; padding: 0; }
  .sk {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 2px 12px;
    padding: 9px 0;
    border-top: 1px solid var(--ln);
  }
  .sk:first-child { border-top: 0; padding-top: 0; }
  .nm { color: var(--ink); font-size: 12.5px; font-weight: 500; overflow-wrap: anywhere; }
  .ds {
    grid-column: 1;
    display: block;
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--mu);
    font: 400 12px/1.45 var(--font-body);
    text-align: left;
    white-space: normal;
    cursor: pointer;
    border-radius: 4px;
  }
  .ds:hover { background: none; color: var(--ink); }
  .ds.none { cursor: default; color: var(--fa); }
  .sk .chip { grid-row: 1 / 3; grid-column: 2; align-self: center; }
  .warn {
    grid-column: 1 / -1;
    display: flex;
    gap: 6px;
    align-items: flex-start;
    color: var(--wa);
    font-size: 12px;
    line-height: 1.45;
    margin-top: 4px;
  }
  .warn :global(.ico) { margin-top: 2px; }
  .empty { padding: 4px 0; font-size: 12.5px; line-height: 1.5; }
  .empty .mono { font-size: 11.5px; }
  .how {
    margin-bottom: 12px;
    padding: 11px 12px;
    border-radius: 10px;
    background: var(--p2);
    font-size: 12.5px;
    color: var(--mu);
    line-height: 1.5;
  }
  .how .mono { font-size: 11.5px; color: var(--ink); }
  .exl { margin-top: 8px; }
  .ex {
    margin: 4px 0 0;
    padding: 9px 11px;
    border-radius: 9px;
    background: var(--cv);
    border: 1px solid var(--ln);
    font: 11.5px/1.5 var(--font-mono);
    color: var(--mu);
    white-space: pre-wrap;
  }
  .fine { color: var(--fa); font-size: 12px; line-height: 1.5; margin-top: auto; padding-top: 12px; }
  .fine .mono { font-size: 11.5px; color: var(--mu); }
</style>
