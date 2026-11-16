<script lang="ts">
  // Tune (sheet frames f20 desktop, f25 phone; route #tune/<engine>/<ref>):
  // llama-server settings for one image. Presets fitted to the engine it runs
  // on, each setting with its trade-off, saved for every run (no rebuild), a
  // benchmark on this machine, and where the memory goes. docs/TUNING.md.
  import { untrack } from "svelte";
  import Icon from "../components/Icon.svelte";
  import Seg from "../components/tune/Seg.svelte";
  import Bench from "../components/tune/Bench.svelte";
  import { routeArg, href, go } from "../lib/router";
  import { catalog, sysinfo, buildTemplate, toast } from "../lib/stores";
  import { live, watch } from "../lib/live.svelte";
  import { labelVal } from "../lib/api";
  import { engineName, computeName, computeChip, gb } from "../lib/format";
  import { imageHarness, toolsText } from "../lib/baked";
  import {
    PRESET_IDS, PRESET_LABEL, clean, clearTuning, ctxOptions, ctxText, ctxWords, estimate, getTuning,
    parseTuneArg, sameTuning, saveTuning, tuningChips, tuningLine,
    type EstimateResult, type KvCache, type Preset, type PresetOption, type Spec, type Switch, type Tuning, type TuningInfo,
  } from "../lib/tuning";

  $effect(() => watch("images", "containers"));

  const target = $derived(parseTuneArg($routeArg));
  let info = $state<TuningInfo | null>(null); // GET /api/tuning for the image

  // The image as the engine lists it (name, compute, tools).
  const image = $derived.by(() => {
    const { engine, ref } = target;
    const bare = ref.replace(/^localhost\//, "");
    return (
      live.images.find((im) => {
        const e = im.Engine === "podman" ? "podman" : "docker";
        const r = `${im.Repository}:${im.Tag}`.replace(/^localhost\//, "");
        return e === engine && r === bare;
      }) ?? null
    );
  });
  const modelId = $derived(image ? labelVal(image.Labels, "local-llm.model") : "");
  const model = $derived($catalog.find((m) => m.id === (modelId || info?.model?.id)));
  const compute = $derived((image && (labelVal(image.Labels, "local-llm.compute") || image.Compute)) || "cpu");
  const tools = $derived(image ? imageHarness(image) : null);
  const name = $derived(info?.model?.name || model?.name || target.ref.replace(/^localhost\//, "").replace(/:latest$/, ""));
  const running = $derived(
    live.containers.filter((c) => {
      const r = String(c.Image ?? "").replace(/^localhost\//, "");
      return (c.State === "running" || /^Up/.test(String(c.Status ?? ""))) && r === target.ref.replace(/^localhost\//, "");
    }),
  );

  // ---- load -----------------------------------------------------------------
  let loadErr = $state("");
  let loading = $state(false);
  let draft = $state<Tuning>({});

  async function load(keepDraft = false) {
    const { ref, engine } = target;
    if (!ref) return;
    loading = true;
    loadErr = "";
    try {
      const r = await getTuning(ref, engine);
      if (ref !== target.ref || engine !== target.engine) return;
      info = r;
      if (!keepDraft) draft = { ...(r.saved ?? r.effective ?? r.baked ?? {}) };
    } catch (e) {
      loadErr = (e as Error).message || "The factory didn't answer.";
      info = null;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    target.ref;
    target.engine;
    untrack(() => {
      info = null;
      draft = {};
      load();
    });
  });

  // ---- presets and edits ----------------------------------------------------
  const presets = $derived<PresetOption[]>(
    PRESET_IDS.map((id) => info?.presets?.find((p) => p.id === id)).filter((p): p is PresetOption => !!p),
  );
  const current = $derived<Preset>(
    (draft.preset && draft.preset !== "custom" && presets.find((p) => p.id === draft.preset && sameTuning(p.tuning, draft))?.id) ||
      presets.find((p) => sameTuning(p.tuning, draft))?.id ||
      "custom",
  );
  const recommended: Preset = "balanced";
  // What the Settings card is showing, in words.
  const label = $derived(
    current !== "custom" ? PRESET_LABEL[current] :
    info?.saved && sameTuning(draft, info.saved) ? "Saved" :
    info?.baked && sameTuning(draft, info.baked) ? "The image's" : "Custom",
  );

  function choose(p: PresetOption) {
    draft = { ...p.tuning, preset: p.id };
  }
  function set<K extends keyof Tuning>(k: K, v: Tuning[K]) {
    draft = { ...draft, [k]: v, preset: "custom" };
  }
  function num(v: string): number | undefined {
    const n = parseFloat(v);
    return v.trim() === "" || !isFinite(n) ? undefined : n;
  }

  const trained = $derived(info?.model?.trained_ctx ?? 0);
  const ctxList = $derived(ctxOptions(trained, draft.ctx_size ?? 0));
  const memGB = $derived(info?.engine?.mem_gb || $sysinfo?.engines?.find((e) => e.engine === target.engine)?.mem_gb || 0);

  // The draft model can only be used when it's baked into the image.
  const draftBaked = $derived(!!(info?.baked?.draft_model || info?.baked?.spec === "draft"));
  const draftTitle = $derived(
    draftBaked
      ? ""
      : info?.model?.draft
        ? "Needs a rebuild with its draft model: Bake into a new build."
        : `No draft model pairs with ${name}.`,
  );

  // ---- estimate for the draft (server-side, the same estimator as presets) --
  let est = $state<EstimateResult | null>(null);
  let estFailed = $state(false);
  $effect(() => {
    const t = clean($state.snapshot(draft));
    const { ref, engine } = target;
    if (!info) return;
    const asPreset = presets.find((p) => sameTuning(p.tuning, t));
    if (estFailed) {
      est = asPreset ? { tuning: t, estimate: asPreset.estimate, fits: asPreset.fits, warning: asPreset.warning } : null;
      return;
    }
    const h = setTimeout(() => {
      estimate({ ref, engine, tuning: t })
        .then((r) => {
          if (ref === target.ref) est = r;
        })
        .catch(() => {
          estFailed = true;
          est = asPreset ? { tuning: t, estimate: asPreset.estimate, fits: asPreset.fits, warning: asPreset.warning } : null;
        });
    }, 250);
    return () => clearTimeout(h);
  });

  const e = $derived(est?.estimate);
  const working = $derived(e ? Math.max(0, e.total_gb - e.weights_gb - e.kv_gb) : 0);
  const pct = (v: number) => (memGB ? Math.min(100, (v / memGB) * 100) : 0);

  // ---- save / reset / bake --------------------------------------------------
  const savedT = $derived(info?.saved ?? null);
  const dirty = $derived(!sameTuning(draft, savedT ?? info?.effective ?? info?.baked ?? {}));
  let saving = $state(false);

  async function save() {
    saving = true;
    try {
      await saveTuning(target.ref, target.engine, { ...draft, preset: current });
      toast(running.length ? "Saved. Stop the model and run it again to use these settings." : "Saved. It uses these settings the next time it starts.", 5000);
      await load();
    } catch (err) {
      toast(`Couldn't save: ${(err as Error).message}`, 7000);
    } finally {
      saving = false;
    }
  }

  async function reset() {
    try {
      await clearTuning(target.ref, target.engine);
      toast("Back to the settings baked into the image.", 4000);
      await load();
    } catch (err) {
      toast(`Couldn't reset: ${(err as Error).message}`, 7000);
    }
  }

  function bake() {
    buildTemplate.set({ ref: target.ref, engine: target.engine, tuning: clean({ ...draft, preset: current }) });
    go("plan");
  }

  // ---- benchmark candidates: what it runs now, then the presets that fit ----
  const nowT = $derived(info?.effective ?? info?.baked ?? null);
  const candidates = $derived.by(() => {
    const out: { label: string; tuning: Tuning }[] = [];
    if (nowT) out.push({ label: "Now", tuning: nowT });
    for (const p of presets) {
      if (p.fits === false) continue;
      if (nowT && sameTuning(p.tuning, nowT)) continue;
      out.push({ label: p.label || PRESET_LABEL[p.id], tuning: { ...p.tuning, preset: p.id } });
    }
    return out;
  });

  // ---- faster models worth a look (real catalog sizes only) -----------------
  const FAST = ["gpt-oss-20b", "qwen3-coder-30b-a3b"];
  const faster = $derived(FAST.map((id) => $catalog.find((m) => m.id === id)).filter((m) => !!m && m.id !== model?.id));

  const sourceText = $derived(
    info?.source === "saved" || (savedT && !info?.source) ? "Saved on the Tune page" : "Baked into the image when it was built",
  );
  const flashOpts: { value: Switch; label: string }[] = [
    { value: "auto", label: "Auto" },
    { value: "on", label: "On" },
    { value: "off", label: "Off" },
  ];
</script>

{#if !target.ref}
  <div class="card empty">
    <p>Pick a model to tune in the <a href={href("library")}>Library</a>: open its <b>More</b> menu, then <b>Tune</b>.</p>
  </div>
{:else}
  <div class="tune">
    <section class="who" aria-label="Image">
      <span class="ico"><Icon name="playground" size={20} /></span>
      <div class="whot">
        <h2>{name}</h2>
        <span class="mono">{target.ref}</span>
      </div>
      <span class="cs">
        <span class="chip">{engineName(target.engine)}</span>
        <span class="chip {computeChip(compute)}">{computeName(compute)}</span>
        {#if tools}<span class="chip">Tools{tools.tools.includes("shell") ? " · shell on" : ""}</span>{/if}
        {#if running.length}<span class="chip ok"><span class="dot"></span>Running</span>{/if}
      </span>
      {#if nowT}
        <div class="now">
          <span class="lbl">Runs now with</span>
          <span class="cs">{#each tuningChips(nowT) as c (c)}<span class="chip">{c}</span>{/each}</span>
          <small>{sourceText}</small>
        </div>
      {/if}
    </section>

    {#if loadErr}
      <div class="callout er" role="alert">
        <Icon name="errc" />
        <span>
          <b>Couldn't read this image's settings.</b>
          <p>{loadErr}</p>
          <p><button class="small" type="button" onclick={() => load()}>Try again</button> <a class="btn sm gh" href={href("library")}>Back to the Library</a></p>
        </span>
      </div>
    {:else if !info}
      <div class="card"><p class="sub"><span class="spin"></span> Reading the model's settings…</p></div>
    {:else}
      <div class="grid">
        <div class="col">
          <section class="card" aria-labelledby="pre-h">
            <div class="hd">
              <h2 id="pre-h">Start from a preset</h2>
              <span class="sub">Fitted to {engineName(target.engine)}{memGB ? ` · ${Math.floor(memGB)} GB` : ""} · {computeName(compute)}</span>
            </div>
            {#if presets.length}
              <div class="pgrid" role="radiogroup" aria-label="Presets">
                {#each presets as p (p.id)}
                  {@const tight = p.fits === false || !!p.warning}
                  <label class="pre" class:on={current === p.id}>
                    <span class="pt">
                      <input type="radio" name="preset" checked={current === p.id} onchange={() => choose(p)} />
                      <b>{p.label || PRESET_LABEL[p.id]}</b>
                      {#if p.id === recommended}<span class="chip pr">Recommended</span>{/if}
                    </span>
                    <p>{p.explain}</p>
                    <ul>{#each tuningChips(p.tuning) as c (c)}<li>{c}</li>{/each}</ul>
                    {#if p.estimate}
                      <span class="mem" class:tight>
                        <span class="bar" class:ok={!tight} class:wa={tight}><i style="width:{pct(p.estimate.total_gb)}%"></i></span>
                        <small>{gb(p.estimate.total_gb)}{memGB ? ` of ${Math.floor(memGB)} GB` : ""}{p.fits === false ? " · doesn't fit" : p.warning ? " · tight" : ""}</small>
                      </span>
                    {/if}
                  </label>
                {/each}
              </div>
            {:else}
              <p class="sub">No presets for this model: set it up below.</p>
            {/if}
          </section>

          <section class="card" aria-labelledby="set-h">
            <div class="hd">
              <h2 id="set-h">Settings</h2>
              <span class="chip pr">{label}</span>
              <div class="sp">{#if savedT}<button class="small ghost" type="button" onclick={reset}>Reset to the image's</button>{/if}</div>
            </div>
            <div class="set">
              <div class="row">
                <div class="k"><b>Context window</b><small>How much the model can hold at once: the chat, files and tool output.</small></div>
                <div class="v">
                  <select aria-label="Context window" value={draft.ctx_size ?? ""} onchange={(ev) => set("ctx_size", num(ev.currentTarget.value))}>
                    {#if !draft.ctx_size}<option value="">The model's default</option>{/if}
                    {#each ctxList as n (n)}<option value={n}>{ctxText(n)} tokens</option>{/each}
                  </select>
                  <small>{ctxWords(draft.ctx_size)}{draft.ctx_size && trained ? ". " : ""}{trained ? `${name} reads up to ${ctxText(trained)}.` : ""}{#if e}{" "}<b class="plus">Cache {gb(e.kv_gb)}</b>{/if}</small>
                </div>
              </div>
              <div class="row">
                <div class="k"><b>Memory cache</b><small>Smaller fits more context with little loss. Quarter-size can drift on long code.</small></div>
                <div class="v">
                  <Seg label="Memory cache" value={draft.kv_cache} onchange={(v: KvCache) => set("kv_cache", v)}
                    options={[{ value: "f16", label: "Full" }, { value: "q8_0", label: "Half" }, { value: "q4_0", label: "Quarter" }]} />
                </div>
              </div>
              <div class="row">
                <div class="k"><b>Flash attention</b><small>Faster and smaller on Vulkan, CUDA and Metal. On whenever the cache is smaller than full.</small></div>
                <div class="v"><Seg label="Flash attention" value={draft.flash_attn} onchange={(v: Switch) => set("flash_attn", v)} options={flashOpts} /></div>
              </div>
              <div class="row">
                <div class="k"><b>Look-ahead</b><small>Guesses the next words and checks the guesses in one pass. The answers are the same, faster when code or text repeats.</small></div>
                <div class="v">
                  <Seg label="Look-ahead" value={draft.spec} onchange={(v: Spec) => set("spec", v)}
                    options={[
                      { value: "none", label: "Off" },
                      { value: "ngram", label: "From the chat" },
                      { value: "draft", label: "Draft model", disabled: !draftBaked, title: draftTitle },
                    ]} />
                  {#if !draftBaked}<small>{draftTitle}</small>{/if}
                </div>
              </div>
              <div class="row">
                <div class="k"><b>Requests at once</b><small>Lets Open WebUI's titles and follow-ups run beside your chat instead of waiting behind it.</small></div>
                <div class="v">
                  <Seg label="Requests at once" value={draft.parallel} onchange={(v: number) => set("parallel", v)}
                    options={[{ value: 1, label: "1" }, { value: 2, label: "2" }, { value: 4, label: "4" }]} />
                  {#if (draft.parallel ?? 1) > 1}
                    <label class="tgl"><input type="checkbox" checked={!!draft.kv_unified} onchange={(ev) => set("kv_unified", ev.currentTarget.checked)} /><span>Share one context</span></label>
                    <small>{draft.kv_unified ? "Each request can use the whole window." : `Each request gets ${ctxText(Math.floor((draft.ctx_size ?? 0) / (draft.parallel ?? 1)) || undefined)}.`}</small>
                  {/if}
                </div>
              </div>
              <div class="row">
                <div class="k"><b>Thinking</b><small>Off answers sooner. On helps with multi-step coding. A limit caps how long it thinks.</small></div>
                <div class="v">
                  <Seg label="Thinking" value={draft.reasoning} onchange={(v: Switch) => set("reasoning", v)} options={flashOpts} />
                  {#if draft.reasoning !== "off"}
                    <label class="inl"><span>Limit</span><input type="number" min="0" step="256" placeholder="No limit" value={draft.reasoning_budget ?? ""} onchange={(ev) => set("reasoning_budget", num(ev.currentTarget.value))} /><span>tokens</span></label>
                  {/if}
                </div>
              </div>
              <details class="more">
                <summary>
                  <span class="k"><b>Sampling and memory</b><small>{[
                    draft.temperature != null && `temperature ${draft.temperature}`,
                    draft.top_p != null && `top-p ${draft.top_p}`,
                    draft.top_k != null && `top-k ${draft.top_k}`,
                    draft.batch && `batch ${draft.batch}${draft.ubatch ? " / " + draft.ubatch : ""}`,
                  ].filter(Boolean).join(" · ") || "The model's defaults"}</small></span>
                  <Icon name="chevronDown" size={15} />
                </summary>
                <div class="adv">
                  {#each [
                    ["temperature", "Temperature", "0.05", "Lower is steadier; code likes 0.2–0.7."],
                    ["top_p", "Top-p", "0.01", ""],
                    ["top_k", "Top-k", "1", ""],
                    ["min_p", "Min-p", "0.01", ""],
                    ["repeat_penalty", "Repeat penalty", "0.01", ""],
                    ["batch", "Batch", "128", "Prompt tokens per step. Larger reads faster, uses more memory."],
                    ["ubatch", "Micro-batch", "64", ""],
                    ["cache_ram_mb", "Prompt cache (MB)", "256", "Keeps recent prompts in memory so a repeat starts sooner."],
                    ["threads", "CPU threads", "1", ""],
                  ] as [k, label, step, hint] (k)}
                    {@const key = k as keyof Tuning}
                    <label class="af">
                      <span>{label}</span>
                      <input type="number" step={step} placeholder="Default" value={(draft[key] as number | undefined) ?? ""} onchange={(ev) => set(key, num(ev.currentTarget.value) as never)} />
                      {#if hint}<small>{hint}</small>{/if}
                    </label>
                  {/each}
                </div>
              </details>
            </div>
            {#if est?.notes?.length}
              <ul class="notes">{#each est.notes as n (n)}<li><Icon name="info" size={13} />{n}</li>{/each}</ul>
            {/if}
            {#if est?.fits === false || est?.warning}
              <div class="callout wa"><Icon name="warn" /><span><b>{est?.fits === false ? `This doesn't fit in ${engineName(target.engine)}'s memory.` : "It's a tight fit."}</b><p>{est?.warning || "Pick a smaller context or cache, or give the engine more memory on the System page."}</p></span></div>
            {/if}
            <div class="ft">
              <button class="primary" type="button" disabled={saving || !dirty} onclick={save}>{saving ? "Saving…" : "Save for every run"}</button>
              <button type="button" onclick={bake}>Bake into a new build…</button>
              <span class="sub">{running.length ? "It's running now: these apply after you stop it and run it again." : "Applies the next time it starts."}</span>
            </div>
          </section>
        </div>

        <div class="col">
          <Bench ref={target.ref} engine={target.engine} {compute} {candidates} saved={savedT} onapplied={() => load()} />

          <section class="card" aria-labelledby="mem-h">
            <div class="hd"><h2 id="mem-h">Memory on {engineName(target.engine)}</h2><span class="sub">{label}</span></div>
            {#if e && memGB}
              <div class="mstack" role="img" aria-label="Weights {gb(e.weights_gb)}, cache {gb(e.kv_gb)}, working {gb(working)}, free {gb(Math.max(0, memGB - e.total_gb))} of {gb(memGB)}">
                <i class="w" style="width:{pct(e.weights_gb)}%"></i><i class="c" style="width:{pct(e.kv_gb)}%"></i><i class="o" style="width:{pct(working)}%"></i>
              </div>
              <div class="leg">
                <span><i class="w"></i>Weights {gb(e.weights_gb)}</span>
                <span><i class="c"></i>Cache {gb(e.kv_gb)}</span>
                {#if working > 0.05}<span><i class="o"></i>Working {gb(working)}</span>{/if}
                <span><i></i>{e.total_gb > memGB ? `Over by ${gb(e.total_gb - memGB)}` : `Free ${gb(memGB - e.total_gb)}`}</span>
              </div>
            {:else}
              <p class="sub">—</p>
            {/if}
          </section>

          {#if faster.length && target.engine === "podman"}
            <div class="callout gpu">
              <Icon name="bolt" />
              <span>
                <b>Want it a lot faster?</b>
                <p>
                  {faster.map((m) => m!.name).join(" and ")} use a few billion of their parameters per word, so they write faster than a dense model their size
                  and are strong with tools. They need {faster.map((m) => gb(m!.size_gb, 0)).join(" and ")} for their weights: run one as a native package
                  with Metal, or give the Podman machine more memory.
                </p>
                <a class="btn sm" href={href("catalog")}>Compare in the Catalog</a>
              </span>
            </div>
          {/if}
        </div>
      </div>
    {/if}
    {#if loading && info}<span class="sr" aria-live="polite">Refreshing</span>{/if}
  </div>
{/if}

<style>
  .tune { display: flex; flex-direction: column; gap: 14px; }
  .who { display: flex; align-items: center; flex-wrap: wrap; gap: 14px; padding: 14px 18px; background: var(--p1); border: 1px solid var(--ln); border-radius: var(--rl); }
  .who h2 { font-family: var(--font-display); font-size: 19px; font-weight: 700; margin: 0; }
  .ico { width: 44px; height: 44px; border-radius: 11px; display: grid; place-items: center; color: var(--pr); background: var(--prs); flex: none; }
  .whot { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .whot .mono { color: var(--mu); overflow-wrap: anywhere; }
  .cs { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; }
  .now { margin-left: auto; display: flex; flex-direction: column; align-items: flex-end; gap: 5px; }
  .now small { color: var(--fa); font-size: 12px; }
  .grid { display: grid; grid-template-columns: minmax(0, 1fr) 440px; gap: 14px; align-items: start; }
  .col { display: flex; flex-direction: column; gap: 14px; min-width: 0; }
  .pgrid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; }
  .pre { display: flex; flex-direction: column; gap: 8px; padding: 12px; border: 1px solid var(--ln2); border-radius: 12px; background: var(--p2); cursor: pointer; }
  .pre.on { border-color: var(--pr); box-shadow: inset 0 0 0 1px var(--pr); background: color-mix(in srgb, var(--pr) 6%, var(--p2)); }
  .pre:focus-within { outline: 2px solid var(--pr); outline-offset: 2px; }
  .pt { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .pt b { font-size: 14px; }
  .pt input { accent-color: var(--pr); margin: 0; }
  .pre p { font-size: 12.5px; color: var(--mu); line-height: 1.4; margin: 0; }
  .pre ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; font-size: 12px; }
  .pre li::before { content: ""; display: inline-block; width: 4px; height: 4px; border-radius: 50%; background: var(--fa); margin: 0 7px 2px 0; }
  .mem { display: flex; flex-direction: column; gap: 4px; margin-top: auto; padding-top: 4px; }
  .mem small { font-family: var(--font-mono); font-size: 11.5px; color: var(--mu); }
  .mem.tight small { color: var(--wa); }
  .set { display: flex; flex-direction: column; }
  .row { display: grid; grid-template-columns: minmax(0, 1fr) 330px; gap: 20px; align-items: center; padding: 12px 0; border-top: 1px solid var(--ln); }
  .row:first-child { border-top: 0; padding-top: 2px; }
  .k b { display: block; font-size: 13.5px; font-weight: 600; }
  .k small { display: block; color: var(--mu); font-size: 12.5px; line-height: 1.4; margin-top: 2px; }
  .v { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; min-width: 0; }
  .v small { color: var(--mu); font-size: 12px; }
  .v select { width: auto; min-width: 200px; }
  .plus { font-family: var(--font-mono); font-weight: 500; color: var(--ink); }
  .tgl, .inl { display: flex; flex-direction: row; align-items: center; gap: 8px; font-size: 12.5px; color: var(--mu); }
  .tgl input { accent-color: var(--pr); }
  .inl input { width: 110px; }
  .more { border-top: 1px solid var(--ln); padding: 12px 0 0; }
  .more summary { display: flex; align-items: center; justify-content: space-between; gap: 12px; list-style: none; cursor: pointer; }
  .more summary::-webkit-details-marker { display: none; }
  .more summary :global(.ico) { color: var(--mu); transition: transform 0.15s; }
  .more[open] summary :global(.ico) { transform: rotate(180deg); }
  .adv { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 12px; padding-top: 12px; }
  .af { display: flex; flex-direction: column; gap: 5px; font-size: 12.5px; color: var(--mu); }
  .af small { font-size: 11.5px; color: var(--fa); }
  .notes { list-style: none; margin: 10px 0 0; padding: 0; display: flex; flex-direction: column; gap: 4px; font-size: 12.5px; color: var(--mu); }
  .notes li { display: flex; gap: 6px; align-items: flex-start; }
  .notes :global(.ico) { margin-top: 2px; color: var(--fa); }
  .callout { margin-top: 12px; }
  .col > .callout { margin-top: 0; }
  .ft { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin-top: 14px; padding-top: 14px; border-top: 1px solid var(--ln); }
  .mstack { display: flex; height: 12px; border-radius: 99px; overflow: hidden; background: var(--p3); margin-top: 2px; }
  .mstack i { display: block; height: 100%; }
  .mstack .w { background: var(--pr); }
  .mstack .c { background: var(--gpu); }
  .mstack .o { background: var(--ac); }
  .leg { display: flex; flex-wrap: wrap; gap: 6px 16px; margin-top: 10px; font-size: 12px; color: var(--mu); }
  .leg span { display: flex; align-items: center; gap: 6px; }
  .leg i { width: 9px; height: 9px; border-radius: 3px; background: var(--p3); display: inline-block; }
  .leg i.w { background: var(--pr); }
  .leg i.c { background: var(--gpu); }
  .leg i.o { background: var(--ac); }
  .callout p + p { margin-top: 8px; }
  @media (max-width: 1280px) {
    .pgrid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  @media (max-width: 1100px) {
    .grid { grid-template-columns: minmax(0, 1fr); }
    .row { grid-template-columns: minmax(0, 1fr) 300px; }
  }
  @media (max-width: 760px) {
    .now { margin-left: 0; align-items: flex-start; }
    .pgrid { grid-template-columns: minmax(0, 1fr); }
    .pre p, .pre ul { display: none; }
    .pre { flex-direction: row; align-items: center; flex-wrap: wrap; }
    .pre .mem { margin: 0 0 0 auto; padding: 0; }
    .pre .mem .bar { display: none; }
    .row { grid-template-columns: minmax(0, 1fr); gap: 8px; }
    .adv { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
</style>
