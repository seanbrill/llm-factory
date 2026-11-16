<script lang="ts">
  // Tools baked into one planned build (its settings, for chat models): which
  // toolsets llmgate runs inside the image, the search engine, and the loop's
  // limits. The shell stays off unless turned on, with a warning. Edits write
  // straight into the plan item (saved by lib/plan.svelte.ts) and go out as
  // the build's `harness` (lib/baked.ts).
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { DEFAULT_TOOLS, LIMITS, TOOLSETS, harnessProblem, toolsText, type Toolset } from "../../lib/baked";
  import type { PlanItem } from "../../lib/plan.svelte";
  import type { Model } from "../../lib/types";

  let { it, m }: { it: PlanItem; m: Model | undefined } = $props();

  const uid = $props.id();
  const h = $derived(it.harness);
  const problem = $derived(harnessProblem(h, m));
  const usesWorkspace = $derived(h.tools.includes("files") || h.tools.includes("shell"));

  function setOn(on: boolean) {
    h.on = on;
    if (on && !h.tools.length) h.tools = [...DEFAULT_TOOLS];
  }

  function toggle(id: Toolset, on: boolean) {
    const next = new Set(h.tools);
    if (on) next.add(id);
    else next.delete(id);
    h.tools = TOOLSETS.map((t) => t.id).filter((t) => next.has(t));
  }

  const limitsNote = $derived(
    [
      `${h.max_steps} step${h.max_steps === 1 ? "" : "s"}`,
      `${h.max_calls_per_step} call${h.max_calls_per_step === 1 ? "" : "s"} each`,
      `${h.tool_timeout} s per call`,
      `${h.max_output_kb} KB per result`,
    ].join(", "),
  );
</script>

<div class="tools" class:on={h.on}>
  <div class="th">
    <span class="ti" aria-hidden="true"><Icon name="wrench" size={15} /></span>
    <span class="tt">
      <b id="{uid}-t">Tools</b>
      <small id="{uid}-d">
        {#if h.on}
          The image runs {toolsText(h.tools)} itself, for any OpenAI-compatible client. No Open WebUI needed.
        {:else}
          Off. Turn on to bake tools into the image: it can search the web, read pages and work with files, without Open WebUI.
        {/if}
      </small>
    </span>
    <input type="checkbox" class="tg" checked={h.on} onchange={(e) => setOn(e.currentTarget.checked)} aria-labelledby="{uid}-t" aria-describedby="{uid}-d" />
  </div>

  {#if h.on}
    <div class="sets" role="group" aria-label="Tools to bake in">
      {#each TOOLSETS as t (t.id)}
        {@const sel = h.tools.includes(t.id)}
        <label class="ts" class:sel class:sh={t.id === "shell"}>
          <input type="checkbox" checked={sel} onchange={(e) => toggle(t.id, e.currentTarget.checked)} />
          <span class="tsi" aria-hidden="true"><Icon name={t.icon} size={14} /></span>
          <span class="tst"><b>{t.label}</b><small>{t.hint}</small></span>
        </label>
      {/each}
    </div>

    {#if h.tools.includes("shell")}
      <div class="callout wa" role="note">
        <Icon name="warn" size={16} />
        <span>
          <b>The shell runs whatever command the model decides on.</b>
          <p>
            It runs inside the container as an unprivileged user, starts in /workspace and stops at the time limit. But it can
            reach the internet, read everything in the container and change any file in the workspace folder. A web page or
            file the model reads can talk it into running something. Turn it on only for models and inputs you trust.
          </p>
        </span>
      </div>
    {/if}

    {#if usesWorkspace}
      <p class="hint ws">
        <Icon name="folder" size={13} />
        <span>When the factory runs the image, <span class="mono">/workspace</span> is the <span class="mono">workspace/</span> folder in the factory folder. Without it, the files last only as long as the container.</span>
      </p>
    {/if}

    {#if h.tools.includes("web_search")}
      <div class="grid g2">
        <label class="f">
          <span>Search with</span>
          <select bind:value={h.search}>
            <option value="duckduckgo">DuckDuckGo (built in, no key)</option>
            <option value="searxng">SearXNG (your own)</option>
            {#if h.search === "brave"}<option value="brave">Brave (its key is set when it runs)</option>{/if}
          </select>
        </label>
        {#if h.search === "searxng"}
          <label class="f">
            <span>SearXNG address</span>
            <input bind:value={h.searxng_url} placeholder="http://host.docker.internal:8888" spellcheck="false" autocomplete="off" inputmode="url" aria-describedby="{uid}-sx" aria-invalid={!!problem && problem.includes("SearXNG")} />
          </label>
        {/if}
      </div>
      <p class="hint" id="{uid}-sx">
        {#if h.search === "searxng"}
          The container calls it, so use <span class="mono">host.docker.internal</span> for one on this computer. Its settings need <span class="mono">json</span> in <span class="mono">search.formats</span>.
        {:else if h.search === "brave"}
          Brave needs <span class="mono">HARNESS_BRAVE_API_KEY</span> when the container starts; keys are never baked in.
        {:else}
          DuckDuckGo needs no key, but can answer with a bot check under heavy use. SearXNG doesn't.
        {/if}
      </p>
    {/if}

    <details class="lim">
      <summary><Icon name="chevronRight" size={13} /><span>Limits</span><small>{limitsNote}</small></summary>
      <div class="grid g4">
        <label class="f">
          <span>{LIMITS.max_steps.label}</span>
          <input type="number" min={LIMITS.max_steps.min} max={LIMITS.max_steps.max} step="1" bind:value={h.max_steps} />
          <small>{LIMITS.max_steps.min} to {LIMITS.max_steps.max}. Then it has to answer.</small>
        </label>
        <label class="f">
          <span>{LIMITS.max_calls_per_step.label}</span>
          <input type="number" min={LIMITS.max_calls_per_step.min} max={LIMITS.max_calls_per_step.max} step="1" bind:value={h.max_calls_per_step} />
          <small>{LIMITS.max_calls_per_step.min} to {LIMITS.max_calls_per_step.max}</small>
        </label>
        <label class="f">
          <span>{LIMITS.tool_timeout.label}</span>
          <span class="sfxw"><input type="number" min={LIMITS.tool_timeout.min} max={LIMITS.tool_timeout.max} step="1" bind:value={h.tool_timeout} /><span class="sfx">s</span></span>
          <small>{LIMITS.tool_timeout.min} to {LIMITS.tool_timeout.max} seconds</small>
        </label>
        <label class="f">
          <span>{LIMITS.max_output_kb.label}</span>
          <span class="sfxw"><input type="number" min={LIMITS.max_output_kb.min} max={LIMITS.max_output_kb.max} step="0.5" bind:value={h.max_output_kb} /><span class="sfx">KB</span></span>
          <small>Longer results are cut, with a note</small>
        </label>
      </div>
      <p class="hint">These are the image's defaults. The Tune page can change them later for every run, without a rebuild.</p>
    </details>

    <div class="bk" role="note">
      <b>What's built in, and what's chosen when it runs</b>
      <ul>
        <li><b>Built into the image:</b> the tools ticked above (search, pages, files, skills, shell), the search engine, and the limits' defaults.</li>
        <li><b>Chosen when it runs:</b> MCP servers. The Run dialog picks which servers from the <a href={href("harness")}>Harness</a> page it connects to (and which of their tools), and a running model can add more from any chat with <span class="mono">/mcp</span>. None are built in, so changing them never needs a rebuild.</li>
        <li><b>Changeable later without a rebuild:</b> context, memory and speed settings, reasoning effort, sampling and the tool limits, on the Tune page.</li>
      </ul>
    </div>

    {#if problem}
      <p class="err" role="alert"><Icon name="errc" size={14} /><span>{problem}</span></p>
    {/if}
    <p class="hint more">How baked tools work: the <a href={href("harness", "baked")}>Harness</a> page and <span class="mono">docs/HARNESS.md</span>, “Baked harness”.</p>
  {/if}
</div>

<style>
  .tools { display: flex; flex-direction: column; gap: 12px; padding-top: 12px; border-top: 1px solid var(--ln); }
  .th { display: grid; grid-template-columns: 30px minmax(0, 1fr) auto; gap: 10px; align-items: center; }
  .ti { width: 30px; height: 30px; border-radius: 8px; display: grid; place-items: center; background: var(--p3); color: var(--mu); }
  .tools.on .ti { background: var(--prs); color: var(--pr); }
  .tt { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .tt b { font-size: 13px; font-weight: 600; color: var(--ink); }
  .tt small { font-size: 12px; color: var(--mu); line-height: 1.4; }

  .sets { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: 8px; }
  .ts {
    display: grid;
    grid-template-columns: auto 22px minmax(0, 1fr);
    gap: 8px;
    align-items: start;
    padding: 9px 10px;
    border: 1px solid var(--ln2);
    border-radius: var(--r);
    background: var(--p1);
    cursor: pointer;
  }
  .ts input { margin: 3px 0 0; }
  .ts.sel { border-color: color-mix(in srgb, var(--pr) 45%, var(--ln2)); }
  .ts.sh.sel { border-color: color-mix(in srgb, var(--wa) 55%, var(--ln2)); background: var(--was); }
  .tsi { width: 22px; height: 22px; border-radius: 6px; display: grid; place-items: center; background: var(--p3); color: var(--mu); }
  .ts.sel .tsi { color: var(--ink); }
  .tst { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .tst b { font-size: 12.5px; font-weight: 600; color: var(--ink); }
  .tst small { font-size: 11.5px; color: var(--mu); line-height: 1.35; }

  .grid { display: grid; gap: 12px; }
  .g2 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .g4 { grid-template-columns: repeat(4, minmax(0, 1fr)); }
  .f { display: flex; flex-direction: column; gap: 5px; font-size: 12.5px; color: var(--mu); min-width: 0; }
  .f > span:first-child { font-weight: 500; }
  .f small { color: var(--fa); font-size: 11.5px; }
  .f select, .f input { font-size: 13px; background: var(--p1); min-width: 0; }
  .sfxw { display: flex; align-items: stretch; }
  .sfxw input { border-top-right-radius: 0; border-bottom-right-radius: 0; flex: 1; }
  .sfx {
    display: flex;
    align-items: center;
    padding: 0 9px;
    border: 1px solid var(--ln2);
    border-left: 0;
    border-radius: 0 var(--r) var(--r) 0;
    background: var(--p3);
    color: var(--mu);
    font: 12px var(--font-mono);
  }
  .hint { font-size: 12px; color: var(--mu); line-height: 1.45; }
  .hint.ws { display: grid; grid-template-columns: 13px minmax(0, 1fr); gap: 8px; }
  .hint.ws :global(.ico) { margin-top: 2px; color: var(--fa); }
  .hint .mono { font-size: 11.5px; color: var(--ink); }

  .lim summary {
    display: flex;
    align-items: center;
    gap: 6px;
    cursor: pointer;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--ink);
    list-style: none;
  }
  .lim summary::-webkit-details-marker { display: none; }
  .lim summary :global(.ico) { color: var(--fa); transition: transform 0.15s; }
  .lim[open] summary :global(.ico) { transform: rotate(90deg); }
  .lim summary small { color: var(--mu); font-weight: 400; font-size: 12px; margin-left: 4px; }
  .lim .grid { margin-top: 10px; }

  .err { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; font-size: 12.5px; color: var(--ink); }
  .err :global(.ico) { margin-top: 2px; color: var(--er); }
  .more { color: var(--fa); }

  @media (max-width: 1100px) {
    .g4 { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  @media (max-width: 560px) {
    .g2, .g4 { grid-template-columns: minmax(0, 1fr); }
    .sets { grid-template-columns: minmax(0, 1fr); }
  }
  .bk { margin-top: 10px; padding: 10px 12px; border-radius: 10px; background: var(--p2); font-size: 12.5px; line-height: 1.5; color: var(--mu); }
  .bk > b { color: var(--ink); font-weight: 600; }
  .bk ul { margin: 6px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .bk li b { color: var(--ink); font-weight: 600; }
</style>
