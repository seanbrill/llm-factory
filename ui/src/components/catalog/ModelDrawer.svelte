<script lang="ts">
  // A model's details (opened from a Catalog card, #catalog/<model id>): where
  // it runs on this machine and how to fix what stops it, what the library
  // already has of it, expected speed here, and the catalog's notes and ratings.
  import { onMount } from "svelte";
  import Icon from "../Icon.svelte";
  import ModalityBadge from "../ModalityBadge.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { modMeta } from "../../lib/icons";
  import { META, dimsFor, catIcon, tip, contextFor, visualSpec } from "../../lib/meta";
  import { PERF_LABEL } from "../../lib/perf";
  import { gb, ago, computeName, engineName } from "../../lib/format";
  import { href } from "../../lib/router";
  import { ui } from "../../lib/ui.svelte";
  import { needLabel, perfHere, perfTone, isMoE, type Fit, type LibEntry, type PathFit } from "../../lib/catalog";
  import { canHarness } from "../../lib/baked";
  import type { Model, SysInfo } from "../../lib/types";

  let {
    m,
    fit,
    lib,
    si,
    selected,
    ontoggle,
    onbuild,
    onclose,
  }: {
    m: Model;
    fit: Fit;
    lib: LibEntry;
    si: SysInfo | null;
    selected: boolean;
    ontoggle: () => void;
    onbuild: () => void;
    onclose: () => void;
  } = $props();

  let closeBtn: HTMLButtonElement | undefined = $state();
  onMount(() => {
    const opener = document.activeElement as HTMLElement | null;
    closeBtn?.focus();
    return () => opener?.focus?.();
  });

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape" && !ui.paletteOpen) {
      e.preventDefault();
      onclose();
    }
  }

  const meta = $derived(META[m.id]);
  const dims = $derived(meta?.ratings ? dimsFor(m.modality).filter((d) => d in meta.ratings) : []);
  const perf = $derived(perfHere(m, si));
  const ctx = $derived(contextFor(m.id));
  const vs = $derived(visualSpec(m.id, m.modality));
  const clamp = (v: number) => Math.max(0, Math.min(100, v || 0));
  const RUNTIME: Record<string, string> = { cpp: "llama.cpp family (GGUF)", python: "Python / PyTorch", audiocpp: "audio.cpp" };
</script>

<svelte:window onkeydown={onKey} />

{#snippet path(label: string, p: PathFit)}
  <div class="path {p.tone}">
    <Icon name={p.icon} size={16} />
    <div>
      <p class="pt"><span class="lbl">{label}</span> {p.text}</p>
      {#if p.detail}<p class="pd">{p.detail}</p>{/if}
      {#if p.fix}
        <p class="pd">{p.fix.text}</p>
        {#if p.fix.cmd}<CodeLine cmd={p.fix.cmd} />{/if}
      {/if}
    </div>
  </div>
{/snippet}

<button class="scrim" type="button" tabindex="-1" aria-label="Close details" onclick={onclose}></button>
<div class="drawer" role="dialog" aria-modal="true" aria-labelledby="md-title">
  <header class="dh">
    <span class="kico"><Icon name={modMeta(m.modality).icon} size={18} /></span>
    <div class="dt">
      <h2 id="md-title">{m.name}</h2>
      <p class="specs">{[m.params, m.size_gb ? gb(m.size_gb) : "", needLabel(m) ? `needs ${needLabel(m)} GB` : ""].filter(Boolean).join(" · ")}</p>
    </div>
    <button class="ghost icon" type="button" bind:this={closeBtn} onclick={onclose} aria-label="Close details"><Icon name="xmark" size={18} /></button>
  </header>

  <div class="body">
    <div class="badges">
      <ModalityBadge mod={m.modality ?? "text"} />
      {#if m.recommended}<span class="chip pr"><Icon name="star" size={12} />Recommended</span>{/if}
      {#if isMoE(m)}<span class="chip" title="Mixture of experts: only part of the model works on each token, so it runs faster than its size suggests.">MoE</span>{/if}
      {#if m.runtime === "python"}<span class="chip" title="Runs on the Python/PyTorch runtime: a bigger image, no quantization, and a hard memory floor (it can't page to the CPU like GGUF).">Python runtime</span>{/if}
      {#if m.legacy}<span class="chip" title="Superseded by a newer model in the catalog. Kept so images built from it and its downloaded weights still work.">Older model</span>{/if}
    </div>

    <section>
      <h3 class="lbl">On this machine</h3>
      {@render path("CPU", fit.cpu)}
      {@render path("GPU", fit.gpu)}
      <ul class="facts">
        {#each lib.running as r (r.port + r.engine)}
          <li><Icon name="check" size={14} /><span>Running on {engineName(r.engine)} ({computeName(r.compute)}){#if r.port} at :{r.port} — <a href={href("playground", r.port)}>try it in the Playground</a>{/if}</span></li>
        {/each}
        {#each lib.images as im (im.engine + im.ref)}
          <li><Icon name="library" size={14} /><span>Built: <span class="mono">{im.ref}</span> for {engineName(im.engine)} ({computeName(im.compute)}) — <a href={href("library", im.ref)}>open in the Library</a></span></li>
        {/each}
        {#if lib.onDisk}
          <li><Icon name="disk" size={14} /><span>Weights on disk: a build needs no download.</span></li>
        {:else if m.file}
          <li><Icon name="download" size={14} /><span>A build downloads {gb(m.size_gb)} of weights first.</span></li>
        {/if}
        {#if lib.active}
          <li><Icon name="builds" size={14} /><span>{lib.active.status === "running" ? "Building now" : "Queued"} for {engineName(lib.active.engine)} — <a href={href("builds", lib.active.id)}>open the build</a></span></li>
        {:else if lib.failed}
          <li class="er"><Icon name="errc" size={14} /><span>Its last build failed{#if lib.failed.finished_at} {ago(lib.failed.finished_at)}{/if}: {lib.failed.error || "see its log"} — <a href={href("builds", lib.failed.id)}>see why</a></span></li>
        {/if}
      </ul>
    </section>

    <section>
      <h3 class="lbl">Expected speed here</h3>
      <div class="perf">
        <span class="chip {perfTone(perf.cpu)}" title={`CPU — ${PERF_LABEL[perf.cpu]}: ${tip("perf:" + perf.cpu)}`}><Icon name="cpu" size={13} />CPU · {PERF_LABEL[perf.cpu]}</span>
        <span class="chip {perfTone(perf.gpu)}" title={`GPU — ${PERF_LABEL[perf.gpu]}: ${tip("perf:" + perf.gpu)}`}><Icon name="gpu" size={13} />GPU · {PERF_LABEL[perf.gpu]}</span>
      </div>
    </section>

    {#if m.description || meta?.summary || meta?.weakness}
      <section class="about">
        {#if m.description}<p>{m.description}</p>{/if}
        {#if meta?.summary}<p><b>Best at</b> {meta.summary}</p>{/if}
        {#if meta?.weakness}<p><b>Watch-outs</b> {meta.weakness}</p>{/if}
        {#if meta?.strengths?.length}
          <div class="strengths">{#each meta.strengths as t (t)}<span class="tag" title={tip("tag:" + t)}>{t}</span>{/each}</div>
        {/if}
      </section>
    {/if}

    {#if vs}
      <section>
        <h3 class="lbl">Output</h3>
        <div class="vis">
          {#if vs.res}<span class="chip"><Icon name="image" size={12} />{vs.res}</span>{/if}
          {#if vs.length}<span class="chip"><Icon name="film" size={12} />{vs.length}</span>{/if}
          {#if vs.note}<span class="sub">{vs.note}</span>{/if}
        </div>
      </section>
    {/if}

    <section>
      <h3 class="lbl">Capabilities</h3>
      {#if dims.length && meta}
        <div class="caps">
          {#each dims as d (d)}
            {@const v = clamp(meta.ratings[d])}
            <div class="cap">
              <span class="cl" title={tip("cat:" + d)}>{#if catIcon(d)}<Icon name={catIcon(d)} size={13} />{/if}{d}</span>
              <span class="ct"><i style="width:{v}%"></i></span>
              <span class="cv">{v}</span>
            </div>
          {/each}
        </div>
        <p class="note">Scores (0–100) are rough guidance, calibrated within local open models. Speed is an estimate for this machine.</p>
      {:else}
        <p class="note">No capability ratings for this model yet.</p>
      {/if}
    </section>

    <section>
      <h3 class="lbl">Details</h3>
      <dl class="dl">
        {#if m.params}<dt>Parameters</dt><dd>{m.params}</dd>{/if}
        {#if m.size_gb}<dt>Weights</dt><dd>{gb(m.size_gb)}</dd>{/if}
        {#if m.min_ram_gb}<dt>Minimum RAM</dt><dd>{m.min_ram_gb} GB</dd>{/if}
        {#if m.min_vram_gb}<dt>GPU memory</dt><dd>about {m.min_vram_gb} GB</dd>{/if}
        {#if ctx}<dt>Context</dt><dd>{ctx} tokens</dd>{/if}
        {#if m.computes?.length}<dt>Builds for</dt><dd>{m.computes.map(computeName).join(", ")}</dd>{/if}
        <dt>Runtime</dt><dd>{RUNTIME[m.runtime ?? "cpp"] ?? m.runtime}</dd>
        {#if canHarness(m)}
          <dt>Tools</dt>
          <dd>
            Can carry its own tools: web search, web pages, files and skills, for any client.
            {#if selected}<a href={href("plan", m.id)}>Turn them on in its build settings</a>{:else}Turn them on in its build settings once it's in the plan.{/if}
          </dd>
        {/if}
        {#if m.repo}<dt>Source</dt><dd><a href="https://huggingface.co/{m.repo}" target="_blank" rel="noreferrer noopener">{m.repo} <Icon name="external" size={12} /></a></dd>{/if}
      </dl>
    </section>
  </div>

  <footer class="df">
    <button class={selected ? "" : "primary"} type="button" onclick={ontoggle}>
      <Icon name={selected ? "check" : "plus"} size={15} />{selected ? "In your plan" : "Add to build plan"}
    </button>
    {#if selected}<a class="btn" href={href("plan")}>Review plan<Icon name="arrowRight" size={15} /></a>{/if}
    <button class="ghost" type="button" onclick={onbuild} title="Queue it now with the settings the factory picks">Build now</button>
  </footer>
</div>

<style>
  /* Show the whole command, wrapped, rather than a scrolling line. */
  .drawer :global(.code) { align-items: flex-start; }
  .drawer :global(.code > span) { white-space: normal; overflow-wrap: anywhere; }
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 70;
    background: color-mix(in srgb, var(--cv) 60%, transparent);
    border: 0;
    border-radius: 0;
    padding: 0;
    min-height: 0;
    cursor: default;
  }
  .scrim:hover { background: color-mix(in srgb, var(--cv) 60%, transparent); }
  .drawer {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    z-index: 71;
    width: min(480px, 100vw);
    display: flex;
    flex-direction: column;
    background: var(--p1);
    border-left: 1px solid var(--ln2);
    box-shadow: var(--shadow);
  }
  .dh {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 18px 18px 14px;
    border-bottom: 1px solid var(--ln);
  }
  .kico {
    width: 38px;
    height: 38px;
    border-radius: 10px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .dt { flex: 1; min-width: 0; }
  .dt h2 { margin: 0 0 3px; font-size: 18px; }
  .specs { color: var(--mu); font: 12px var(--font-mono); }
  .body { flex: 1; min-height: 0; overflow-y: auto; padding: 16px 18px 20px; display: flex; flex-direction: column; gap: 18px; }
  .badges { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  section { display: flex; flex-direction: column; gap: 8px; }
  h3.lbl { margin: 0; font-family: var(--font-body); font-size: 11px; }
  .path {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr);
    gap: 10px;
    padding: 10px 12px;
    border-radius: var(--r);
    background: var(--p2);
  }
  .path > :global(.ico) { margin-top: 2px; color: var(--fa); }
  .path.ok > :global(.ico) { color: var(--ok); }
  .path.wa { background: var(--was); }
  .path.wa > :global(.ico) { color: var(--wa); }
  .path.er { background: var(--ers); }
  .path.er > :global(.ico) { color: var(--er); }
  .path.gpu > :global(.ico) { color: var(--gpu); }
  .path > div { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .pt { font-size: 13.5px; font-weight: 600; }
  .pt .lbl { margin-right: 4px; }
  .pd { color: var(--mu); font-size: 12.5px; }
  .facts { list-style: none; margin: 2px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
  .facts li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; color: var(--mu); }
  .facts li :global(.ico) { margin-top: 3px; color: var(--fa); }
  .facts li.er :global(.ico) { color: var(--er); }
  .facts .mono { color: var(--ink); font-size: 12px; word-break: break-all; }
  .perf, .vis { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .about { gap: 8px; font-size: 13px; color: var(--mu); }
  .about b { color: var(--ink); font-weight: 600; margin-right: 4px; }
  .strengths { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 2px; }
  .tag {
    font-size: 11.5px;
    color: var(--mu);
    background: var(--p2);
    border: 1px solid var(--ln);
    border-radius: 6px;
    padding: 1px 7px;
  }
  .caps { display: flex; flex-direction: column; gap: 6px; }
  .cap { display: grid; grid-template-columns: 150px minmax(0, 1fr) 28px; align-items: center; gap: 10px; }
  .cl { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--mu); }
  .cl :global(.ico) { color: var(--fa); }
  .ct { height: 6px; border-radius: 99px; background: var(--p3); overflow: hidden; }
  .ct i { display: block; height: 100%; border-radius: 99px; background: var(--ac); }
  .cv { font: 11.5px var(--font-mono); color: var(--mu); text-align: right; }
  .note { font-size: 11.5px; color: var(--fa); }
  .dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 6px 16px; margin: 0; font-size: 13px; }
  .dl dt { color: var(--fa); }
  .dl dd { margin: 0; color: var(--ink); min-width: 0; overflow-wrap: anywhere; }
  .dl a { display: inline-flex; align-items: center; gap: 4px; }
  .df {
    display: flex;
    gap: 8px;
    padding: 12px 18px calc(12px + env(safe-area-inset-bottom, 0px));
    border-top: 1px solid var(--ln);
    background: var(--p1);
    flex-wrap: wrap;
  }
  .df .ghost { margin-left: auto; }
  @media (max-width: 760px) {
    .drawer { border-left: 0; }
    .cap { grid-template-columns: 120px minmax(0, 1fr) 26px; }
  }
</style>
