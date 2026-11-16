<script lang="ts">
  // Tuning for one planned build (sheet frame f23): a preset fitted to where
  // the model will run, with its memory, baked as the image's defaults. Only
  // the draft model is truly built in; everything else can change later on the
  // Tune page without a rebuild. Edits write into the plan item.
  import Icon from "../Icon.svelte";
  import { gb, engineName } from "../../lib/format";
  import { PRESET_IDS, PRESET_LABEL, ctxText, estimate, getPresets, tuningLine, type Estimate, type PresetOption, type Preset, type Tuning } from "../../lib/tuning";
  import type { PlanItem } from "../../lib/plan.svelte";
  import type { Model } from "../../lib/types";

  let { it, m, compute }: { it: PlanItem; m: Model | undefined; compute: string } = $props();

  let presets = $state<PresetOption[]>([]);
  let draftId = $state("");
  let memGB = $state(0);
  let failed = $state(false);
  // The model's chat template reads reasoning_effort (gpt-oss): offer it here.
  let effortModel = $state(false);

  $effect(() => {
    const id = it.model_id, eng = it.engine, c = compute;
    failed = false;
    const h = setTimeout(() => {
      getPresets(id, c, eng)
        .then((r) => {
          if (id !== it.model_id) return;
          presets = PRESET_IDS.map((p) => r.presets?.find((x) => x.id === p)).filter((x): x is PresetOption => !!x);
          draftId = r.model?.draft || "";
          effortModel = !!r.model?.arch?.reasoning_effort;
          memGB = r.engine?.mem_gb || 0;
        })
        .catch(() => {
          presets = [];
          failed = true;
        });
    }, 200);
    return () => clearTimeout(h);
  });

  const sel = $derived<Preset | "">(it.tuning?.preset && it.tuning.preset !== "custom" ? it.tuning.preset : it.tuning ? "custom" : "");
  const chosen = $derived(presets.find((p) => p.id === sel));

  function pick(v: string) {
    if (!v) {
      it.tuning = undefined;
      return;
    }
    const p = presets.find((x) => x.id === v);
    if (!p) return;
    const keepDraft = it.tuning?.spec === "draft" && !!draftId;
    it.tuning = { ...p.tuning, preset: p.id, ...(keepDraft ? { spec: "draft", draft_model: draftId } : {}) };
    it.ctx_tier = "auto"; // the preset's context applies
  }

  function look(v: "ngram" | "draft") {
    const base = it.tuning ?? { ...(chosen?.tuning ?? {}), preset: sel || "custom" };
    it.tuning = v === "draft" ? { ...base, spec: "draft", draft_model: draftId } : { ...base, spec: "ngram", draft_model: undefined };
  }

  // The context set below (not "From the tuning") replaces the preset's at
  // build time (buildBody), so show and price what will be built.
  const ctxOver = $derived(/^\d+$/.test(it.ctx_tier) ? parseInt(it.ctx_tier, 10) : 0);
  const eff = $derived<Tuning | undefined>(it.tuning && ctxOver ? { ...it.tuning, ctx_size: ctxOver } : it.tuning);
  const differs = $derived(!!eff && !!chosen && (eff.ctx_size !== chosen.tuning.ctx_size || eff.spec !== chosen.tuning.spec || eff.reasoning_effort !== chosen.tuning.reasoning_effort));
  let own = $state<{ estimate: Estimate; fits?: boolean; warning?: string } | null>(null);
  $effect(() => {
    const t = eff, id = it.model_id, eng = it.engine, c = compute;
    own = null;
    if (!t || !differs) return;
    const h = setTimeout(() => {
      estimate({ model: id, compute: c, engine: eng, tuning: t })
        .then((r) => { if (id === it.model_id && r.estimate) own = { estimate: r.estimate, fits: r.fits, warning: r.warning }; })
        .catch(() => {});
    }, 250);
    return () => clearTimeout(h);
  });
  const est = $derived(differs ? own?.estimate : chosen?.estimate);
  const fits = $derived(differs ? own?.fits : chosen?.fits);
  const warning = $derived(differs ? own?.warning : chosen?.warning);
  const pct = $derived(est && memGB ? Math.min(100, (est.total_gb / memGB) * 100) : 0);

  function effort(v: string) {
    if (!it.tuning) return;
    it.tuning = { ...it.tuning, reasoning_effort: (v || undefined) as Tuning["reasoning_effort"] };
  }
</script>

<div class="tun">
  <div class="row">
    <label class="f">
      <span>Tuning</span>
      <select value={sel} onchange={(e) => pick(e.currentTarget.value)} disabled={failed && !presets.length}>
        <option value="">llama.cpp's defaults</option>
        {#each presets as p (p.id)}<option value={p.id}>{p.label || PRESET_LABEL[p.id]}{p.fits === false ? " (doesn't fit)" : ""}</option>{/each}
        {#if sel === "custom"}<option value="custom">Custom</option>{/if}
      </select>
    </label>
    <p class="why">
      {#if chosen && ctxOver && ctxOver !== chosen.tuning.ctx_size}{chosen.label || PRESET_LABEL[chosen.id]} settings with the {ctxText(ctxOver)} context set above. <span class="mono">{tuningLine(eff)}</span>
      {:else if chosen}{chosen.explain} <span class="mono">{tuningLine(eff)}</span>
      {:else if sel === "custom"}<span class="mono">{tuningLine(eff)}</span>
      {:else if failed}The factory couldn't suggest presets for this model.
      {:else}Context from the setting below; change speed and memory settings any time on the Tune page.{/if}
    </p>
  </div>

  {#if sel && (it.tuning?.spec === "ngram" || it.tuning?.spec === "draft")}
    <div class="la" role="radiogroup" aria-label="Look-ahead">
      <div class="lt"><b>Look-ahead</b><small>Guesses ahead and checks the guesses in one pass. The answers are the same.</small></div>
      <label class="o" class:on={it.tuning?.spec === "ngram"}>
        <input type="radio" name="la-{it.model_id}" checked={it.tuning?.spec === "ngram"} onchange={() => look("ngram")} />
        <span><b>From the chat</b><small>No extra download. Best when code or text repeats.</small></span>
      </label>
      <label class="o" class:on={it.tuning?.spec === "draft"} class:dis={!draftId}>
        <input type="radio" name="la-{it.model_id}" disabled={!draftId} checked={it.tuning?.spec === "draft"} onchange={() => look("draft")} />
        <span>
          <b>{draftId ? `Draft model: ${draftId}` : "Draft model"}</b>
          <small>{draftId ? "Adds a small model to the image. Helps on new text too." : `No draft model pairs with ${m?.name ?? "this model"}.`}</small>
        </span>
        {#if it.tuning?.spec === "draft"}<span class="chip pr">Built in</span>{/if}
      </label>
    </div>
  {/if}

  {#if sel && effortModel}
    <div class="row">
      <label class="f">
        <span>Reasoning effort</span>
        <select value={it.tuning?.reasoning_effort ?? ""} onchange={(e) => effort(e.currentTarget.value)}>
          <option value="">Model default (medium)</option>
          <option value="low">Low</option>
          <option value="medium">Medium</option>
          <option value="high">High</option>
        </select>
      </label>
      <p class="why">How long it thinks before answering. Low answers sooner; High helps with hard coding and debugging.</p>
    </div>
  {/if}

  {#if est}
    <div class="mem">
      <span class="bar" class:wa={fits === false || !!warning}><i style="width:{pct}%"></i></span>
      <small>
        <b>{gb(est.total_gb)}{memGB ? ` of ${Math.floor(memGB)} GB` : ""}</b> on {engineName(it.engine)} when it runs · weights {gb(est.weights_gb)}, cache {gb(est.kv_gb)}
        {#if warning}· <span class="wa">{warning}</span>{/if}
      </small>
    </div>
  {/if}
  {#if sel}
    <p class="hint"><Icon name="info" size={12} />Everything except a draft model can change later on the Tune page, without rebuilding.</p>
  {/if}
</div>

<style>
  .tun { display: flex; flex-direction: column; gap: 10px; padding-top: 12px; border-top: 1px solid var(--ln); }
  .row { display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 3fr); gap: 12px; align-items: end; }
  .f { display: flex; flex-direction: column; gap: 5px; font-size: 12.5px; color: var(--mu); font-weight: 500; }
  .f select { font-size: 13px; background: var(--p1); }
  .why { font-size: 12.5px; color: var(--mu); margin: 0 0 8px; line-height: 1.45; }
  .why .mono { color: var(--ink); font-size: 12px; }
  .la { display: grid; grid-template-columns: 200px 1fr 1fr; gap: 10px; align-items: stretch; }
  .lt b { display: block; font-size: 13px; }
  .lt small { font-size: 12px; color: var(--mu); }
  .o { display: flex; align-items: flex-start; gap: 10px; padding: 10px 12px; border: 1px solid var(--ln2); border-radius: var(--r); background: var(--p1); cursor: pointer; }
  .o.on { border-color: var(--pr); box-shadow: inset 0 0 0 1px var(--pr); }
  .o.dis { cursor: not-allowed; opacity: 0.7; }
  .o input { accent-color: var(--pr); margin-top: 3px; }
  .o span:not(.chip) { display: flex; flex-direction: column; margin-right: auto; }
  .o b { font-size: 13px; }
  .o small { font-size: 12px; color: var(--mu); }
  .mem { display: flex; flex-direction: column; gap: 6px; }
  .mem .bar { max-width: 420px; }
  .mem small { font-size: 12px; color: var(--mu); }
  .mem b { color: var(--ink); font-family: var(--font-mono); font-weight: 500; }
  .wa { color: var(--wa); }
  .hint { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--fa); margin: 0; }
  @media (max-width: 1100px) {
    .row { grid-template-columns: minmax(0, 1fr); }
    .la { grid-template-columns: minmax(0, 1fr); }
  }
</style>
