<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "../components/Icon.svelte";
  import ModalityBadge from "../components/ModalityBadge.svelte";
  import { api } from "../lib/api";
  import { catalog, sysinfo, personas, buildTemplate } from "../lib/stores";
  import { modMeta } from "../lib/icons";
  import { hwPerf, PERF_LABEL } from "../lib/perf";
  import { ctxDefaultFor, fmtTokens } from "../lib/util";
  import { META, TAG_LIST, dimsFor, catIcon, tip, contextFor, visualSpec } from "../lib/meta";
  import { build, startBuild, attachIfRunning } from "../lib/build.svelte";
  import type { Model } from "../lib/types";

  // Page state lives in the `build` store (lib/build.svelte.ts) so it — and the
  // running build's poll loop — survive navigating to other pages. Only the
  // DOM ref for log auto-scroll is component-local.
  let logEl = $state<HTMLPreElement>();

  const mods = $derived([...new Set($catalog.map((m) => m.modality ?? "text"))]);
  // Only show strength chips that some catalog model actually carries.
  const tagsAvail = $derived(
    TAG_LIST.filter((t) => $catalog.some((m) => (META[m.id]?.strengths ?? []).includes(t))),
  );
  const filtered = $derived(
    $catalog.filter((m) => {
      const mod = m.modality ?? "text";
      if (build.filterMod && mod !== build.filterMod) return false;
      if (build.maxSize && (m.size_gb || 0) > build.maxSize) return false;
      const tags = META[m.id]?.strengths ?? [];
      if (build.filterTags.length && !build.filterTags.some((t) => tags.includes(t))) return false;
      if (build.q) {
        const hay = `${m.name} ${m.id} ${m.description ?? ""} ${tags.join(" ")}`.toLowerCase();
        if (!hay.includes(build.q.toLowerCase())) return false;
      }
      return true;
    }),
  );

  function pick(m: Model) {
    build.selected = m;
    if (!build.nameTouched) build.imageName = "local-llm/" + m.id;
    build.pickerOpen = false;
  }
  function toggleTag(t: string) {
    build.filterTags = build.filterTags.includes(t)
      ? build.filterTags.filter((x) => x !== t)
      : [...build.filterTags, t];
  }
  const barTier = (v: number) => (v >= 80 ? "hi" : v >= 55 ? "mid" : v >= 35 ? "lo" : "min");
  const clamp = (v: number) => Math.max(0, Math.min(100, v || 0));

  // Default-select the recommended model once the catalog has loaded (skipped
  // when a selection already survives in the store from a prior visit).
  $effect(() => {
    if (!build.selected && $catalog.length) {
      const r = $catalog.find((m) => m.recommended) ?? $catalog[0];
      if (r) pick(r);
    }
  });

  // On (re)mount, reconnect to a build still running on the server — covers a
  // full page refresh; a no-op on plain page navigation (store still tracks it).
  let attached = false;
  $effect(() => {
    if (!attached && $catalog.length) { attached = true; attachIfRunning($catalog); }
  });

  const ctxAutoLabel = $derived(`Auto — recommended (${fmtTokens(ctxDefaultFor(build.compute))})`);
  const memMaxLabel = $derived(
    $sysinfo?.mem_gb ? `Max for your system (${Math.floor($sysinfo.mem_gb)} GB)` : "Max for your system",
  );

  // VRAM right-size warning
  const vramWarn = $derived.by(() => {
    const s = $sysinfo;
    const sel = build.selected;
    if (!sel || !s?.vram_gb || (build.compute !== "cuda" && build.compute !== "vulkan")) return "";
    const need = sel.min_vram_gb || sel.size_gb || 0;
    if (need > s.vram_gb + 0.5)
      return `⚠ ${sel.name} wants ~${need} GB VRAM but you have ${s.vram_gb} GB — it'll offload to CPU and run slowly. A smaller model/quant fits better.`;
    if (need > s.vram_gb - 1.5)
      return `Heads up: ${sel.name} (~${need} GB) is close to your ${s.vram_gb} GB VRAM — a large context may not fit.`;
    return "";
  });

  // Keep the log scrolled to the newest line.
  $effect(() => {
    build.log;
    if (logEl) logEl.scrollTop = logEl.scrollHeight;
  });

  // Clone-an-image template handoff from the Images page.
  $effect(() => {
    const t = $buildTemplate;
    if (!t) return;
    buildTemplate.set(null);
    api<any>(`/api/image/config?ref=${encodeURIComponent(t.ref)}&engine=${encodeURIComponent(t.engine)}`)
      .then((cfg) => {
        const m = $catalog.find((x) => x.id === cfg.model_id);
        if (m) pick(m);
        build.engine = cfg.engine || build.engine;
        build.compute = cfg.compute || build.compute;
        build.injectMode = cfg.inject_mode || "missing";
        build.systemPrompt = cfg.system_prompt || "";
        build.nameTouched = true;
        build.imageName = cfg.repository || build.imageName;
        build.tag = cfg.tag || "latest";
        build.route = cfg.route || "";
        build.autostart = !!cfg.autostart;
      })
      .catch(() => {});
  });
</script>

<div class="card">
  <!-- Model picker (collapsible combobox) -->
  <div class="field">
    <span class="lbl">Model</span>
    <div class="picker" class:open={build.pickerOpen}>
      <button type="button" class="trigger" onclick={() => (build.pickerOpen = !build.pickerOpen)} aria-expanded={build.pickerOpen}>
        {#if build.selected}
          {@const s = build.selected}
          {@const tp = hwPerf(s, $sysinfo)}
          <span class="opt-ic"><Icon name={modMeta(s.modality).icon} size={16} /></span>
          <span class="trig-main">
            <span class="trig-name">{s.name}{#if s.recommended}<span class="rec">★</span>{/if}</span>
            <span class="trig-sub">{s.params} · {s.size_gb} GB · {modMeta(s.modality).label}</span>
          </span>
          <span class="opt-perf">
            <span class="perf perf-{tp.cpu}" title="CPU: {PERF_LABEL[tp.cpu]}"><Icon name="cpu" size={13} /></span>
            <span class="perf perf-{tp.gpu}" title="GPU: {PERF_LABEL[tp.gpu]}"><Icon name="gpu" size={13} /></span>
          </span>
        {:else}
          <span class="trig-ph">Select a model…</span>
        {/if}
        <span class="caret" class:up={build.pickerOpen}><Icon name="chevron" size={16} /></span>
      </button>

      {#if build.pickerOpen}
        <button type="button" class="backdrop" onclick={() => (build.pickerOpen = false)} aria-label="Close model list"></button>
        <div class="panel">
          <div class="search">
            <Icon name="search" />
            <!-- svelte-ignore a11y_autofocus -->
            <input placeholder={`Search ${$catalog.length} models…`} bind:value={build.q} autofocus />
          </div>
          <div class="chips">
            <button class="chip" class:on={!build.filterMod} onclick={() => (build.filterMod = "")}>All</button>
            {#each mods as m (m)}
              <button class="chip" class:on={build.filterMod === m} title={tip("mod:" + m)} onclick={() => (build.filterMod = build.filterMod === m ? "" : m)}>
                <Icon name={modMeta(m).icon} size={13} /> {modMeta(m).label}
                <span class="fn">{$catalog.filter((x) => (x.modality ?? "text") === m).length}</span>
              </button>
            {/each}
          </div>
          {#if tagsAvail.length}
            <div class="chips tags">
              {#each tagsAvail as t (t)}
                <button class="chip sm" class:on={build.filterTags.includes(t)} title={tip("tag:" + t)} onclick={() => toggleTag(t)}>{t}</button>
              {/each}
            </div>
          {/if}
          <div class="sizerow">
            <span class="szlabel">Max size</span>
            {#each [0, 2, 4, 8, 16] as sz (sz)}
              <button class="chip sm" class:on={build.maxSize === sz} onclick={() => (build.maxSize = sz)}>{sz === 0 ? "any" : `≤ ${sz} GB`}</button>
            {/each}
            <span class="count">{filtered.length} of {$catalog.length}</span>
          </div>
          <ul class="list">
            {#each filtered as m (m.id)}
              {@const perf = hwPerf(m, $sysinfo)}
              {@const tags = (META[m.id]?.strengths ?? []).slice(0, 3)}
              <li class:sel={build.selected?.id === m.id} onclick={() => pick(m)} onkeydown={(e) => e.key === "Enter" && pick(m)} role="option" aria-selected={build.selected?.id === m.id} tabindex="0">
                <span class="opt-ic"><Icon name={modMeta(m.modality).icon} size={16} /></span>
                <span class="opt-main">
                  <span class="opt-name">{m.name} {#if m.recommended}<span class="rec">★</span>{/if}</span>
                  <span class="opt-tags">{#each tags as t (t)}<span class="tg">{t}</span>{/each}</span>
                </span>
                <span class="opt-size">{m.size_gb} GB</span>
                <span class="opt-perf">
                  <span class="perf perf-{perf.cpu}" title="CPU: {PERF_LABEL[perf.cpu]}"><Icon name="cpu" size={13} /></span>
                  <span class="perf perf-{perf.gpu}" title="GPU: {PERF_LABEL[perf.gpu]}"><Icon name="gpu" size={13} /></span>
                </span>
              </li>
            {/each}
            {#if !filtered.length}<li class="none">No models match these filters.</li>{/if}
          </ul>
        </div>
      {/if}
    </div>
  </div>

  {#if build.selected}
    {@const s = build.selected}
    {@const meta = META[s.id]}
    {@const perf = hwPerf(s, $sysinfo)}
    {@const ctx = contextFor(s.id)}
    {@const vs = visualSpec(s.id, s.modality)}
    {@const dims = meta?.ratings ? dimsFor(s.modality).filter((d) => d in meta.ratings) : []}
    <div class="detail">
      <div class="dhead">
        <ModalityBadge mod={s.modality ?? "text"} />
        {#if s.recommended}<span class="badge rec-b"><Icon name="star" size={12} /> Recommended</span>{/if}
        <span class="dperf">
          <span class="perf lbl-perf perf-{perf.cpu}" title={`CPU — ${PERF_LABEL[perf.cpu]}: ${tip("perf:" + perf.cpu)}`}><Icon name="cpu" size={13} /> CPU</span>
          <span class="perf lbl-perf perf-{perf.gpu}" title={`GPU — ${PERF_LABEL[perf.gpu]}: ${tip("perf:" + perf.gpu)}`}><Icon name="gpu" size={13} /> GPU</span>
        </span>
      </div>
      <div class="dspec">
        {s.params} · {s.size_gb} GB · min RAM {s.min_ram_gb} GB{#if s.min_vram_gb} · ~{s.min_vram_gb} GB VRAM{/if}{#if ctx} · context {ctx}{/if}
      </div>
      {#if vs}
        <div class="dvis">
          {#if vs.res}<span class="vchip"><Icon name="image" size={12} /> {vs.res}</span>{/if}
          {#if vs.length}<span class="vchip"><Icon name="film" size={12} /> {vs.length}</span>{/if}
          {#if vs.note}<span class="vnote">{vs.note}</span>{/if}
        </div>
      {/if}
      {#if s.description}<div class="ddesc">{s.description}</div>{/if}
      {#if meta?.summary}<div class="dsum"><b>Best at</b> {meta.summary}</div>{/if}
      {#if meta?.weakness}<div class="dweak"><b>Watch-outs</b> {meta.weakness}</div>{/if}
      {#if meta?.strengths?.length}
        <div class="dtags">{#each meta.strengths as t (t)}<span class="tag" title={tip("tag:" + t)}>{t}</span>{/each}</div>
      {/if}
      {#if dims.length}
        <div class="caps">
          {#each dims as d (d)}
            {@const v = clamp(meta.ratings[d])}
            <div class="cap">
              <span class="cap-l" title={tip("cat:" + d)}>{#if catIcon(d)}<Icon name={catIcon(d)} size={13} />{/if}<span>{d}</span></span>
              <span class="cap-track"><span class="cap-fill t-{barTier(v)}" style="width:{v}%"></span></span>
              <span class="cap-v">{v}</span>
            </div>
          {/each}
        </div>
        <div class="capnote">Performance levels estimate your hardware; capability scores (0–100) are rough guidance, calibrated within local open models.</div>
      {:else}
        <div class="capnote">Capability ratings unavailable for this model.</div>
      {/if}
    </div>
  {/if}

  <div class="grid">
    <label>Engine
      <select bind:value={build.engine}><option value="docker">Docker</option><option value="podman">Podman</option></select>
    </label>
    <label>Compute
      <select bind:value={build.compute}>
        <option value="cpu">CPU (portable)</option>
        <option value="cuda">NVIDIA GPU (CUDA)</option>
        <option value="vulkan">GPU — Vulkan / Apple Metal</option>
      </select>
    </label>
    <label>Image name
      <input bind:value={build.imageName} oninput={() => (build.nameTouched = true)} placeholder="myorg/qwen-analyzer" />
    </label>
    <label>Tag <input bind:value={build.tag} /></label>
  </div>
  {#if vramWarn}<p class="hint warn">{vramWarn}</p>{/if}

  <label class="lbl-blk">Initialization prompt (optional) — baked in to specialize startup
    <textarea rows="3" bind:value={build.systemPrompt} placeholder="You are FinBot, a terse financial-data analyst. Always answer with strict JSON."></textarea>
  </label>
  <div class="grid">
    <label>When to apply it
      <select bind:value={build.injectMode}>
        <option value="missing">Default — only if the client sends no system message</option>
        <option value="always">Always — enforce it, overriding the client</option>
      </select>
    </label>
    <label>Fill from persona
      <select onchange={(e) => { const p = $personas.find((x) => x.id === (e.currentTarget as HTMLSelectElement).value); if (p) build.systemPrompt = p.prompt; (e.currentTarget as HTMLSelectElement).value = ""; }}>
        <option value="">Persona…</option>
        {#each $personas as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </label>
  </div>

  <h3 class="subhead">Resources &amp; deploy</h3>
  <div class="grid">
    <label>Context window
      <select bind:value={build.ctxTier}>
        <option value="auto">{ctxAutoLabel}</option>
        <option value="2048">2K</option><option value="4096">4K</option>
        <option value="8192">8K</option><option value="16384">16K</option>
        <option value="32768">32K</option><option value="max">Max for this model</option>
      </select>
    </label>
    <label>Memory limit
      <select bind:value={build.memTier}>
        <option value="0">Auto — unlimited</option>
        <option value="4">4 GB</option><option value="8">8 GB</option>
        <option value="12">12 GB</option><option value="16">16 GB</option>
      </select>
    </label>
    <label>Local URL (optional) <input bind:value={build.route} placeholder="ai" /></label>
    <label class="check"><input type="checkbox" bind:checked={build.autostart} /> <span>Start on desktop start</span></label>
  </div>

  <button class="primary build-btn" onclick={startBuild} disabled={build.building}>Build &amp; export .tar</button>

  {#if build.building || build.status}
    <div class="progress">
      <div class="bar" class:active={build.building && build.pct === null}>
        <div class="fill" class:determinate={build.pct !== null} style={build.pct !== null ? `width:${build.pct}%` : ""}></div>
      </div>
      <div class="row-between">
        <span class="status">{build.status}{#if build.phase} — {build.phase}{/if}{#if build.pct !== null} ({build.pct}%){/if}</span>
        <button class="ghost" onclick={() => (build.showLog = !build.showLog)}>{build.showLog ? "Hide" : "Show"} log</button>
      </div>
      {#if build.showLog}<pre class="log" bind:this={logEl}>{build.log}</pre>{/if}
    </div>
  {/if}
</div>

<style>
  .field { display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px; }
  .lbl { font-size: 13px; color: var(--muted); }

  /* Collapsible combobox */
  .picker { position: relative; }
  .trigger { width: 100%; display: flex; align-items: center; gap: 11px; padding: 9px 11px; background: #0c0e13; border: 1px solid var(--border); border-radius: 10px; cursor: pointer; text-align: left; color: var(--text); }
  .picker.open .trigger { border-color: #4b6ad6; }
  .trig-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .trig-name { font-size: 13.5px; display: inline-flex; align-items: center; gap: 5px; }
  .trig-sub { font-size: 12px; color: var(--muted); }
  .trig-ph { flex: 1; color: var(--muted); font-size: 13.5px; }
  .caret { color: var(--muted); display: inline-flex; transition: transform 0.15s; }
  .caret.up { transform: rotate(180deg); }

  .backdrop { position: fixed; inset: 0; z-index: 30; background: transparent; border: 0; cursor: default; }
  .panel { position: absolute; z-index: 31; top: calc(100% + 6px); left: 0; right: 0; background: #11141b; border: 1px solid #33405e; border-radius: 12px; box-shadow: 0 16px 44px rgba(0,0,0,0.55); padding: 10px; display: flex; flex-direction: column; gap: 8px; }

  .search { display: flex; align-items: center; gap: 8px; padding: 0 11px; background: #0c0e13; border: 1px solid var(--border); border-radius: 9px; }
  .search :global(.ico) { color: var(--muted); }
  .search input { border: none; background: transparent; padding: 9px 0; width: 100%; }
  .chips { display: flex; flex-wrap: wrap; gap: 5px; }
  .chips.tags { gap: 4px; }
  .chip { display: inline-flex; align-items: center; gap: 5px; padding: 4px 10px; border-radius: 999px; background: #1a2030; border: 1px solid var(--border); color: #cbd3e1; font-size: 12.5px; cursor: pointer; }
  .chip.sm { padding: 3px 9px; font-size: 11.5px; }
  .chip.on { background: #2a3550; border-color: #4b6ad6; color: #fff; }
  .chip .fn { color: var(--muted); font-size: 11px; }
  .chip.on .fn { color: #cdd8f2; }
  .sizerow { display: flex; align-items: center; gap: 5px; flex-wrap: wrap; }
  .szlabel { font-size: 11.5px; color: var(--muted); margin-right: 2px; }
  .count { margin-left: auto; font-size: 11.5px; color: var(--muted); }

  .list { list-style: none; margin: 0; padding: 0; max-height: 320px; overflow: auto; display: flex; flex-direction: column; gap: 2px; }
  .list li { display: flex; align-items: center; gap: 11px; padding: 7px 9px; cursor: pointer; border: 1px solid transparent; border-radius: 8px; }
  .list li:hover { background: #1a2030; }
  .list li.sel { background: #1c2740; border-color: #33508f; }
  .list li.none { color: var(--muted); justify-content: center; padding: 14px; cursor: default; }
  .opt-ic { width: 28px; height: 28px; flex: none; border-radius: 8px; display: inline-flex; align-items: center; justify-content: center; background: #161b27; border: 1px solid var(--border); }
  .opt-main { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .opt-name { font-size: 13.5px; display: inline-flex; align-items: center; gap: 5px; }
  .opt-tags { display: inline-flex; flex-wrap: wrap; gap: 4px; }
  .tg { font-size: 10.5px; color: #9fb0cc; background: #1b2233; border: 1px solid var(--border); border-radius: 4px; padding: 0 5px; line-height: 16px; }
  .rec { color: #e3b341; }
  .opt-size { font-size: 12px; color: var(--muted); flex: none; }
  .opt-perf { display: inline-flex; gap: 4px; flex: none; }

  .perf { display: inline-flex; align-items: center; gap: 4px; padding: 3px; border-radius: 6px; border: 1px solid; }
  .perf.lbl-perf { padding: 3px 7px; font-size: 11.5px; font-weight: 600; }
  .perf-excellent { color: #46c969; border-color: #1d4427; background: #11261a; }
  .perf-good { color: #74c365; border-color: #2c4a23; background: #152415; }
  .perf-fair { color: #e3b341; border-color: #4a3c15; background: #241f10; }
  .perf-warning { color: #e0832f; border-color: #4a3115; background: #241809; }
  .perf-poor { color: #f0683c; border-color: #4a2415; background: #240f09; }
  .perf-impossible { color: #f85149; border-color: #4a1715; background: #240b0b; }
  .perf-na { color: #7b8494; border-color: #2a2f3a; background: #161922; }

  /* Detail card */
  .detail { margin: 0 0 10px; padding: 14px 16px; border: 1px solid var(--border); border-radius: 12px; background: #12151c; }
  .dhead { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
  .badge.rec-b { display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: #e3b341; border: 1px solid #4a3c15; background: #241f10; border-radius: 999px; padding: 2px 9px; }
  .dperf { margin-left: auto; display: inline-flex; gap: 6px; }
  .dspec { color: var(--text); font-size: 13px; }
  .dvis { display: flex; align-items: center; flex-wrap: wrap; gap: 7px; margin-top: 7px; }
  .vchip { display: inline-flex; align-items: center; gap: 5px; font-size: 12px; color: #b9c6e0; background: #182236; border: 1px solid #2c3a57; border-radius: 7px; padding: 3px 9px; }
  .vnote { font-size: 12px; color: var(--muted); }
  .ddesc { color: var(--muted); font-size: 13px; margin-top: 7px; }
  .dsum { font-size: 12.5px; margin-top: 7px; color: #cdd6e6; }
  .dweak { font-size: 12.5px; margin-top: 4px; color: #cdd6e6; }
  .dsum b, .dweak b { color: var(--muted); font-weight: 600; margin-right: 4px; }
  .dtags { display: flex; flex-wrap: wrap; gap: 5px; margin-top: 9px; }
  .tag { font-size: 11px; color: #9fb0cc; background: #1b2233; border: 1px solid var(--border); border-radius: 5px; padding: 1px 7px; }

  .caps { display: flex; flex-direction: column; gap: 5px; margin-top: 12px; }
  .cap { display: grid; grid-template-columns: 150px 1fr 28px; align-items: center; gap: 10px; }
  .cap-l { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: #c2ccdc; }
  .cap-l :global(.ico) { color: var(--muted); flex: none; }
  .cap-track { height: 7px; background: #0c0e13; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .cap-fill { display: block; height: 100%; border-radius: 999px; }
  .cap-fill.t-hi { background: #46c969; }
  .cap-fill.t-mid { background: #74c365; }
  .cap-fill.t-lo { background: #e3b341; }
  .cap-fill.t-min { background: #e0832f; }
  .cap-v { font-size: 11.5px; color: var(--muted); text-align: right; font-variant-numeric: tabular-nums; }
  .capnote { font-size: 11px; color: var(--muted); margin-top: 10px; line-height: 1.5; }

  .grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; margin: 10px 0; }
  .grid label, .lbl-blk { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--muted); }
  .check { flex-direction: row; align-items: center; gap: 8px; align-self: end; padding-bottom: 9px; }
  .check input { width: auto; }
  .subhead { margin: 18px 0 4px; font-size: 13px; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: 0.4px; }
  .warn { color: #e0b341; }
  .build-btn { margin-top: 14px; }

  /* Build loader */
  .progress { margin-top: 14px; }
  .bar { height: 10px; background: #0c0e13; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .fill { height: 100%; width: 0; background: linear-gradient(90deg, var(--accent-2), var(--accent)); }
  .fill.determinate { transition: width 0.3s ease; }
  .bar.active .fill { width: 35%; animation: sweep 1.3s ease-in-out infinite; }
  @keyframes sweep { 0% { margin-left: -35%; } 100% { margin-left: 100%; } }
  .row-between { display: flex; align-items: center; justify-content: space-between; gap: 10px; margin-top: 8px; }
  .status { color: var(--muted); font-size: 13px; }
  .log { margin-top: 10px; background: #06080c; border: 1px solid var(--border); border-radius: 8px; padding: 12px; max-height: 320px; overflow: auto; white-space: pre-wrap; font: 12.5px/1.5 ui-monospace, monospace; color: #cbd3e1; }
</style>
