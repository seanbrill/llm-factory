<script lang="ts">
  // One model in the Catalog grid (sheet frame f8): pick it with the checkbox
  // (or anywhere on the card), open its details from the name. The two lines
  // say where it can run on this machine now — CPU and GPU — and why not.
  import Icon from "../Icon.svelte";
  import { modMeta } from "../../lib/icons";
  import { gb, ago } from "../../lib/format";
  import { kindChip, needLabel, type Fit, type LibEntry, type PathFit } from "../../lib/catalog";
  import type { Model } from "../../lib/types";

  let {
    m,
    fit,
    lib,
    selected,
    ontoggle,
    onopen,
  }: {
    m: Model;
    fit: Fit;
    lib: LibEntry;
    selected: boolean;
    ontoggle: () => void;
    onopen: () => void;
  } = $props();

  // One chip: what's happening with it now, else its kind.
  const chip = $derived.by((): { t: string; c: string; title?: string } | null => {
    if (lib.active) return lib.active.status === "running" ? { t: "Building", c: "pr" } : { t: "Queued", c: "" };
    if (lib.running.length) return { t: "Running", c: "ok" };
    if (lib.failed) {
      const when = ago(lib.failed.finished_at);
      return { t: when ? `Failed ${when}` : "Failed", c: "er", title: lib.failed.error || "Its last build failed." };
    }
    const k = kindChip(m);
    return k ? { t: k, c: "" } : null;
  });

  const unknown = $derived(fit.cpu.state === "unknown" && fit.gpu.state === "unknown");
  const dim = $derived(!fit.runsNow && !unknown);
  const uid = $derived("pick-" + m.id.replace(/[^a-zA-Z0-9_-]/g, "_"));
  const specs = $derived([m.params, m.size_gb ? gb(m.size_gb) : "", needLabel(m) ? `needs ${needLabel(m)} GB` : ""].filter(Boolean).join(" · "));
</script>

{#snippet line(p: PathFit)}
  <div class="fit {p.tone}"><Icon name={p.icon} size={14} /><span>{p.text}</span></div>
{/snippet}

<article class="mcard" class:sel={selected} class:dim>
  <!-- The whole card toggles the checkbox; the name and the checkbox sit above it. -->
  <label class="hit" for={uid}><span class="sr">Pick {m.name}</span></label>
  <div class="mt">
    <span class="kico"><Icon name={modMeta(m.modality).icon} size={16} /></span>
    <span class="tags">
      {#if chip}<span class="chip {chip.c}" title={chip.title}>{chip.t}</span>{/if}
    </span>
    <input id={uid} class="cb" type="checkbox" checked={selected} onchange={ontoggle} aria-describedby="{uid}-fit" />
  </div>
  <button class="nm" type="button" onclick={onopen} aria-haspopup="dialog" title="Details for {m.name}">
    <span>{m.name}</span>{#if m.recommended}<span class="rec" title="Recommended"><Icon name="star" size={12} label="Recommended" /></span>{/if}
  </button>
  <div class="specs">{specs}</div>
  {#if m.voice_clone || m.commercial_use === false}
    <div class="flags">
      {#if m.voice_clone}<span class="chip"><Icon name="mic" size={12} />Clones voices</span>{/if}
      {#if m.commercial_use === false}<span class="chip wa"><Icon name="warn" size={12} />Non-commercial license</span>{/if}
    </div>
  {/if}
  <div class="fits" id="{uid}-fit">
    {@render line(fit.cpu)}
    {@render line(fit.gpu)}
  </div>
</article>

<style>
  .mcard {
    position: relative;
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--rl);
    padding: 14px;
    display: flex;
    flex-direction: column;
    gap: 7px;
    min-width: 0;
    transition: border-color 0.12s ease, background 0.12s ease;
  }
  .mcard:hover { border-color: var(--ln2); }
  .mcard.sel {
    border-color: var(--pr);
    box-shadow: inset 0 0 0 1px var(--pr);
    background: color-mix(in srgb, var(--pr) 7%, var(--p1));
  }
  .mcard.dim > :not(.hit):not(.mt) { opacity: 0.62; }
  .mcard.dim .kico, .mcard.dim .tags { opacity: 0.62; }
  .hit {
    position: absolute;
    inset: 0;
    border-radius: inherit;
    cursor: pointer;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  .mt { display: flex; align-items: center; gap: 8px; margin-bottom: 2px; }
  .kico {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .tags { display: flex; gap: 5px; margin-left: auto; margin-right: 2px; min-width: 0; }
  .tags .chip { max-width: 100%; overflow: hidden; text-overflow: ellipsis; }
  .cb {
    appearance: none;
    -webkit-appearance: none;
    position: relative;
    z-index: 1;
    width: 20px;
    height: 20px;
    min-height: 0;
    margin: 0;
    padding: 0;
    border-radius: 6px;
    border: 1.5px solid var(--ln2);
    background: transparent;
    cursor: pointer;
    flex: none;
  }
  .cb:hover { border-color: var(--mu); }
  .cb:checked { background: var(--pr); border-color: var(--pr); }
  .cb:checked::after {
    content: "";
    position: absolute;
    left: 5.5px;
    top: 2px;
    width: 5px;
    height: 10px;
    border: solid var(--onpr);
    border-width: 0 2.2px 2.2px 0;
    transform: rotate(45deg);
  }
  .cb:focus { box-shadow: none; }
  .cb:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .nm {
    position: relative;
    z-index: 1;
    align-self: flex-start;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--ink);
    font: 600 14.5px/1.25 var(--font-body);
    text-align: left;
    white-space: normal;
    justify-content: flex-start;
  }
  .nm:hover { background: none; text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--fa); }
  .rec { color: var(--ac); display: inline-flex; }
  .specs { color: var(--mu); font-size: 12px; font-family: var(--font-mono); }
  .flags { display: flex; flex-wrap: wrap; gap: 5px; }
  .fits { display: flex; flex-direction: column; gap: 7px; }
  .fit { display: flex; align-items: flex-start; gap: 6px; font-size: 12.5px; color: var(--mu); line-height: 1.35; }
  .fit :global(.ico) { margin-top: 1px; }
  .fit.ok :global(.ico) { color: var(--ok); }
  .fit.wa :global(.ico) { color: var(--wa); }
  .fit.er :global(.ico) { color: var(--er); }
  .fit.er { color: var(--ink); }
  .fit.gpu :global(.ico) { color: var(--gpu); }
  .fit.mu :global(.ico) { color: var(--fa); }
</style>
