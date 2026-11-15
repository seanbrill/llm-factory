<script lang="ts">
  // Tools inside the image: the baked harness, next to the Open WebUI parts
  // above. llmgate in the image runs the tools itself, so they travel with the
  // image and work for any OpenAI-compatible client. It's a per-build setting
  // on the Build plan (lib/baked.ts); the Library shows which images have it.
  // "#harness/baked" opens here.
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { catalog } from "../../lib/stores";
  import { live, watch } from "../../lib/live.svelte";
  import { plan } from "../../lib/plan.svelte";
  import { imageRef } from "../../lib/util";
  import { TOOLSETS, canHarness, imageHarness } from "../../lib/baked";

  $effect(() => watch("images"));

  const uid = $props.id();

  // Images that already carry tools, newest first as the engines list them.
  const withTools = $derived(
    live.images
      .map((im) => ({ ref: imageRef(im).ref.replace(/^localhost\//, ""), h: imageHarness(im) }))
      .filter((x) => x.h && !x.ref.includes("<none>")),
  );
  const shown = $derived(withTools.slice(0, 6));

  // The setting lives in a build's settings: open the first planned chat model's.
  const planned = $derived(plan.items.find((it) => canHarness($catalog.find((m) => m.id === it.model_id))));
  const plannedName = $derived(planned ? ($catalog.find((m) => m.id === planned.model_id)?.name ?? planned.model_id) : "");
</script>

<section class="card" id="h-baked" tabindex="-1" aria-labelledby="{uid}-title">
  <div class="hd">
    <h2 id="{uid}-title">Tools inside the image</h2>
    {#if withTools.length}<span class="chip ok">{withTools.length} {withTools.length === 1 ? "image has" : "images have"} them</span>{/if}
  </div>

  <div class="body">
    <div class="tx">
      <p>
        A chat model's image can carry its own tools. Its front door runs them, so any OpenAI-compatible client gets a model
        that searches, reads pages and works with files from a plain chat request. They travel with the image: no Open WebUI,
        terminal or factory needed where it runs.
      </p>
      <p class="sub">
        Turn on <b>Tools</b> in a build's settings on the Build plan. When the factory runs the image, the file tools work in
        its <span class="mono">workspace/</span> folder. More in <span class="mono">docs/HARNESS.md</span>, “Baked harness”.
      </p>
      <div class="acts">
        {#if planned}
          <a class="btn sm pri" href={href("plan", planned.model_id)}><Icon name="wrench" size={14} />Tools for {plannedName}</a>
        {:else}
          <a class="btn sm" href={href("catalog")}><Icon name="catalog" size={14} />Pick a chat model to build</a>
        {/if}
        {#if plan.items.length}<a class="btn sm gh" href={href("plan")}>Open the Build plan<Icon name="arrowRight" size={14} /></a>{/if}
      </div>
      {#if shown.length}
        <p class="imgs">
          <span class="lbl">Built with tools</span>
          {#each shown as x (x.ref)}<a class="chip" href={href("library", x.ref)} title="Open {x.ref} in the Library">{x.ref.replace(/:latest$/, "")}</a>{/each}
          {#if withTools.length > shown.length}<span class="more">+{withTools.length - shown.length} more</span>{/if}
        </p>
      {/if}
    </div>

    <ul class="sets" aria-label="Tools an image can carry">
      {#each TOOLSETS as t (t.id)}
        <li>
          <span class="ti" aria-hidden="true"><Icon name={t.icon} size={14} /></span>
          <span><b>{t.label}</b>{#if t.id === "shell"}<span class="chip wa">Off by default</span>{/if}<small>{t.hint}</small></span>
        </li>
      {/each}
    </ul>
  </div>
</section>

<style>
  .card { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .body { display: grid; grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr); gap: 22px; align-items: start; }
  .tx { display: flex; flex-direction: column; gap: 10px; min-width: 0; font-size: 13.5px; line-height: 1.5; }
  .tx .sub { font-size: 12.5px; line-height: 1.5; }
  .tx b { color: var(--ink); font-weight: 600; }
  .mono { font-size: 12px; color: var(--ink); }
  .acts { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 2px; }
  .imgs { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; font-size: 12px; color: var(--mu); }
  .imgs .lbl { margin-right: 2px; }
  .imgs a.chip { text-decoration: none; font-family: var(--font-mono); font-weight: 500; }
  .imgs a.chip:hover { color: var(--ink); }

  .sets { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .sets li {
    display: grid;
    grid-template-columns: 28px minmax(0, 1fr);
    gap: 10px;
    align-items: center;
    padding: 7px 0;
    border-top: 1px solid var(--ln);
  }
  .sets li:first-child { border-top: 0; padding-top: 0; }
  .ti { width: 28px; height: 28px; border-radius: 8px; display: grid; place-items: center; background: var(--p3); color: var(--mu); }
  .sets b { font-size: 13px; font-weight: 600; color: var(--ink); }
  .sets .chip { height: 18px; padding: 0 6px; font-size: 10.5px; margin-left: 8px; vertical-align: 1px; }
  .sets small { display: block; color: var(--mu); font-size: 12px; }

  @media (max-width: 900px) {
    .body { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  }
</style>
