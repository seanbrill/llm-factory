<script lang="ts">
  // Prompts (route #prompts, #prompts/<id>): saved prompts for benchmarks. Write a
  // prompt once, run it (or every prompt in the list) on the running models one
  // after another, and compare how soon each started, how fast it wrote and
  // what it said. Every answer is kept with the prompt (config/prompt-results.json),
  // as are the ones the Playground records when a saved prompt opens a chat.
  import { untrack } from "svelte";
  import Icon from "../components/Icon.svelte";
  import { live, watch } from "../lib/live.svelte";
  import { catalog, ensembles, personas, toast } from "../lib/stores";
  import { routeArg, href, go } from "../lib/router";
  import { engineName, computeName, computeChip, ago } from "../lib/format";
  import { renderAssistant } from "../lib/markdown";
  import { targetsFrom, type Target } from "../lib/chat.svelte";
  import {
    promptState, refreshPrompts, createPrompt, updatePrompt, deletePrompt, importPrompts, exportPrompts, readPromptFile,
    listResults, recordResult, deleteResult, runPrompt, resultOf, promptMessages, promptSampling, summarize, modelLabel,
    CHAT_MODS, type SavedPrompt, type PromptDraft, type PromptResult, type RunOutcome,
  } from "../lib/prompts.svelte";

  $effect(() => watch("containers"));
  refreshPrompts();

  const targets = $derived(
    targetsFrom(live.containers, $catalog, { ensembles: $ensembles, personas: $personas }).filter((t) => CHAT_MODS.has(t.modality)),
  );

  // ---- the list ----------------------------------------------------------------------
  let q = $state("");
  const shown = $derived.by(() => {
    const t = q.trim().toLowerCase();
    if (!t) return promptState.list;
    return promptState.list.filter((p) => [p.name, p.text, ...(p.tags ?? [])].some((s) => s.toLowerCase().includes(t)));
  });

  // The selected prompt: "#prompts/<id>", "new" for a new one.
  const selId = $derived($routeArg || "");
  const sel = $derived(promptState.list.find((p) => p.id === selId) ?? null);
  $effect(() => {
    // Open the first prompt when none is chosen.
    if (!selId && promptState.loaded && promptState.list.length) untrack(() => go("prompts", promptState.list[0].id));
  });

  // ---- the editor -------------------------------------------------------------------------
  const blank = (): PromptDraft & { tagText: string } => ({ name: "", text: "", system: "", tags: [], expect: "", temperature: null, top_p: null, tagText: "" });
  let ed = $state(blank());
  let edFor = "";
  let busySave = $state(false);
  $effect(() => {
    const p = sel;
    const id = selId;
    untrack(() => {
      const key = p ? `${p.id}@${p.updated}` : id;
      if (key === edFor) return;
      edFor = key;
      ed = p
        ? { name: p.name, text: p.text, system: p.system ?? "", tags: p.tags ?? [], expect: p.expect ?? "", temperature: p.temperature ?? null, top_p: p.top_p ?? null, tagText: (p.tags ?? []).join(", ") }
        : blank();
    });
  });
  const isNew = $derived(selId === "new" || (!sel && !promptState.list.length));
  const dirty = $derived(
    isNew
      ? !!ed.text.trim()
      : !!sel &&
          (ed.name !== sel.name || ed.text !== sel.text || ed.system !== (sel.system ?? "") || ed.expect !== (sel.expect ?? "") ||
            ed.tagText !== (sel.tags ?? []).join(", ") || (ed.temperature ?? null) !== (sel.temperature ?? null) || (ed.top_p ?? null) !== (sel.top_p ?? null)),
  );

  const num = (v: unknown) => (v === "" || v == null || Number.isNaN(Number(v)) ? null : Number(v));

  async function saveEd() {
    if (!ed.text.trim() || busySave) return;
    busySave = true;
    const body: PromptDraft = {
      name: ed.name.trim(),
      text: ed.text,
      system: ed.system?.trim() ? ed.system : undefined,
      tags: ed.tagText.split(",").map((s) => s.trim()).filter(Boolean),
      expect: ed.expect?.trim() || undefined,
      temperature: num(ed.temperature),
      top_p: num(ed.top_p),
    };
    try {
      const p = sel && !isNew ? await updatePrompt(sel.id, body) : await createPrompt(body);
      await refreshPrompts();
      edFor = "";
      go("prompts", p.id);
      toast(`Saved “${p.name}”.`, 2500);
    } catch (e) {
      toast(`Couldn't save: ${(e as Error).message}`, 6000);
    } finally {
      busySave = false;
    }
  }

  async function remove() {
    if (!sel) return;
    const n = results.length;
    if (!confirm(`Delete “${sel.name}”${n ? ` and its ${n} result${n === 1 ? "" : "s"}` : ""}?`)) return;
    try {
      await deletePrompt(sel.id);
      await refreshPrompts();
      go("prompts", promptState.list[0]?.id ?? "");
    } catch (e) {
      toast(`Couldn't delete: ${(e as Error).message}`, 6000);
    }
  }

  let fileEl: HTMLInputElement | undefined = $state();
  async function onImport(e: Event) {
    const f = (e.currentTarget as HTMLInputElement).files?.[0];
    (e.currentTarget as HTMLInputElement).value = "";
    if (!f) return;
    try {
      const got = await importPrompts(await readPromptFile(f));
      await refreshPrompts();
      toast(`Imported ${got.length} prompt${got.length === 1 ? "" : "s"}.`, 3000);
    } catch (err) {
      toast(`Couldn't import ${f.name}: ${(err as Error).message}`, 7000);
    }
  }

  // ---- results ------------------------------------------------------------------------------
  let results = $state<PromptResult[]>([]);
  let resErr = $state("");
  async function loadResults(id: string) {
    try {
      const rs = await listResults(id);
      if (id === selId) {
        results = rs.sort((a, b) => (a.at < b.at ? 1 : -1));
        resErr = "";
      }
    } catch (e) {
      resErr = (e as Error).message;
    }
  }
  $effect(() => {
    const id = sel?.id;
    results = [];
    if (id) untrack(() => loadResults(id));
  });
  const summary = $derived(summarize(results));

  async function dropResult(r: PromptResult) {
    try {
      await deleteResult(r.id);
      results = results.filter((x) => x.id !== r.id);
    } catch (e) {
      toast(`Couldn't delete the result: ${(e as Error).message}`, 6000);
    }
  }

  // ---- running ---------------------------------------------------------------------------------
  let picked = $state<Record<number, boolean>>({}); // port -> run on it
  let repeats = $state(1);
  let allShown = $state(false);
  let maxTokens = $state(0);
  let temp = $state(0.4);
  let topP = $state(0.95);
  const chosen = $derived(targets.filter((t) => picked[t.port]));
  $effect(() => {
    // Pick every running model the first time some are listed.
    if (targets.length && !Object.keys(picked).length) untrack(() => (picked = Object.fromEntries(targets.map((t) => [t.port, true]))));
  });

  interface Progress {
    model: string;
    prompt: string;
    i: number;
    n: number;
    out: RunOutcome | null;
  }
  let running = $state(false);
  let prog = $state<Progress | null>(null);
  let batch = $state<PromptResult[]>([]); // this run's results, for a multi-prompt summary
  let ctrl: AbortController | null = null;

  async function run() {
    const prompts = allShown ? shown : sel ? [sel] : [];
    if (!prompts.length || !chosen.length || running) return;
    running = true;
    batch = [];
    ctrl = new AbortController();
    const id = Date.now().toString(36);
    const total = prompts.length * chosen.length * repeats;
    let i = 0;
    let failed = 0;
    // One model at a time, each through every prompt, so it has the machine to itself.
    outer: for (const t of chosen) {
      for (const p of prompts) {
        for (let k = 0; k < repeats; k++) {
          if (ctrl.signal.aborted) break outer;
          i++;
          prog = { model: t.name, prompt: p.name, i, n: total, out: null };
          const opts = promptSampling(p, { temperature: temp, top_p: topP });
          if (maxTokens > 0) opts.max_tokens = maxTokens;
          const o = await runPrompt(t, promptMessages(p), opts, ctrl.signal, (x) => (prog = { ...prog!, out: { ...x, stats: { ...x.stats } } }));
          if (o.stopped) break outer;
          if (o.error) failed++;
          try {
            const r = await recordResult(resultOf(p, t, opts, o, "bench", id));
            batch = [...batch, r];
            if (p.id === sel?.id) results = [r, ...results];
          } catch (e) {
            toast(`A result wasn't kept: ${(e as Error).message}`, 6000);
          }
        }
      }
    }
    const stopped = ctrl.signal.aborted;
    running = false;
    prog = null;
    ctrl = null;
    toast(
      stopped ? `Stopped after ${batch.length} of ${total} runs.` : `Done: ${total} run${total === 1 ? "" : "s"}${failed ? `, ${failed} without an answer` : ""}.`,
      4000,
    );
  }

  const batchSummary = $derived(allShown && batch.length ? summarize(batch) : []);

  // ---- formatting ---------------------------------------------------------------------------------
  const sec = (ms: number | null | undefined) => (ms == null ? "–" : ms < 1000 ? `${Math.round(ms)} ms` : `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)} s`);
  const tps = (v: number | null | undefined) => (v == null ? "–" : `${v.toFixed(1)}`);
  const when = (at: string) => ago(Date.parse(at));
</script>

<div class="pp">
  <aside class="card list" aria-label="Saved prompts">
    <div class="hd">
      <h2>Saved prompts</h2>
      <span class="sp">
        <button class="small icon" type="button" title="Import prompts from a file" aria-label="Import prompts" onclick={() => fileEl?.click()}><Icon name="upload" size={14} /></button>
        <button class="small icon" type="button" title="Export the prompts as a file" aria-label="Export prompts" disabled={!promptState.list.length} onclick={() => exportPrompts(promptState.list)}><Icon name="download" size={14} /></button>
        <button class="small" type="button" onclick={() => go("prompts", "new")}><Icon name="plus" size={14} />New</button>
      </span>
      <input type="file" accept="application/json,.json" hidden bind:this={fileEl} onchange={onImport} />
    </div>
    {#if promptState.list.length > 5}
      <input type="search" placeholder="Search by name, text or tag" aria-label="Search prompts" bind:value={q} />
    {/if}
    {#if promptState.error}
      <p class="hint er">{promptState.error}</p>
    {:else if promptState.loaded && !promptState.list.length}
      <p class="empty">No saved prompts yet. Write one on the right, or save one from the Playground's Prompts menu.</p>
    {/if}
    <ul>
      {#each shown as p (p.id)}
        <li>
          <a href={href("prompts", p.id)} class:on={p.id === selId} aria-current={p.id === selId ? "page" : undefined}>
            <b>{p.name}</b>
            <span>{p.text}</span>
            {#if p.tags?.length}<small>{p.tags.join(", ")}</small>{/if}
          </a>
        </li>
      {/each}
    </ul>
  </aside>

  <div class="main">
    <section class="card" aria-labelledby="ed-h">
      <div class="hd">
        <h2 id="ed-h">{isNew ? "New prompt" : "Prompt"}</h2>
        {#if sel && !isNew}<span class="sub">saved {when(sel.updated)}</span>{/if}
        <span class="sp">
          {#if sel && !isNew}<button class="small ghost dg" type="button" onclick={remove}><Icon name="trash" size={14} />Delete</button>{/if}
          <button class="small primary" type="button" disabled={!dirty || !ed.text.trim() || busySave} onclick={saveEd}>{busySave ? "Saving…" : "Save"}</button>
        </span>
      </div>
      <div class="form">
        <label class="f"><span>Name</span><input bind:value={ed.name} placeholder="Taken from the first line when left empty" maxlength="120" /></label>
        <label class="f"><span>Prompt</span><textarea rows="6" bind:value={ed.text} placeholder="What every model is asked"></textarea></label>
        <details open={!!ed.system || !!ed.expect || ed.temperature != null || ed.top_p != null || !!ed.tagText}>
          <summary>System prompt, sampling, tags and notes</summary>
          <div class="form">
            <label class="f"><span>System prompt <small>(optional: sent first, the same to every model)</small></span><textarea rows="3" bind:value={ed.system}></textarea></label>
            <div class="two">
              <label class="f"><span>Temperature <small>(blank: the run's)</small></span><input type="number" min="0" max="2" step="0.05" bind:value={ed.temperature} /></label>
              <label class="f"><span>Top-p <small>(blank: the run's)</small></span><input type="number" min="0.05" max="1" step="0.05" bind:value={ed.top_p} /></label>
            </div>
            <label class="f"><span>Tags <small>(comma-separated: “coding, tools”)</small></span><input bind:value={ed.tagText} /></label>
            <label class="f"><span>What a good answer has <small>(a note for comparing; not checked)</small></span><textarea rows="2" bind:value={ed.expect}></textarea></label>
          </div>
        </details>
      </div>
    </section>

    <section class="card" aria-labelledby="run-h">
      <div class="hd">
        <h2 id="run-h">Run on the running models</h2>
        <span class="sub">one model at a time, so each has the machine to itself</span>
      </div>
      {#if !targets.length}
        <p class="empty">No chat model is running. <a href={href("library")}>Start some from the Library</a>, then come back.</p>
      {:else}
        <ul class="tg-list">
          {#each targets as t (t.port)}
            <li>
              <label>
                <input type="checkbox" bind:checked={picked[t.port]} disabled={running} />
                <b>{t.name}</b>
                <span class="chip">{engineName(t.engine)}</span>
                <span class="chip {computeChip(t.compute)}">{computeName(t.compute)}</span>
                <span class="mono fa">:{t.port}</span>
              </label>
            </li>
          {/each}
        </ul>
        <div class="opts">
          <label>Times each <select bind:value={repeats} disabled={running}><option value={1}>1</option><option value={3}>3</option><option value={5}>5</option></select></label>
          <label>Temperature <input type="number" min="0" max="2" step="0.05" bind:value={temp} disabled={running} /></label>
          <label>Top-p <input type="number" min="0.05" max="1" step="0.05" bind:value={topP} disabled={running} /></label>
          <label>Answer length <select bind:value={maxTokens} disabled={running}>
            <option value={0}>No limit</option><option value={512}>512 tokens</option><option value={1024}>1,024 tokens</option><option value={2048}>2,048 tokens</option><option value={4096}>4,096 tokens</option>
          </select></label>
          {#if shown.length > 1}
            <label class="ck"><input type="checkbox" bind:checked={allShown} disabled={running} /> Every prompt in the list ({shown.length})</label>
          {/if}
        </div>
        <p class="hint">
          A prompt's own temperature and top-p win over these. Repeats of one prompt can start sooner than the first: the model
          keeps the prompt in its cache. A model with tools answers through its harness, tool calls included.
        </p>
        <div class="go">
          {#if running}
            <button type="button" onclick={() => ctrl?.abort()}><Icon name="stopsq" size={14} />Stop</button>
          {:else}
            <button class="primary" type="button" disabled={!chosen.length || (!allShown && (!sel || isNew)) || dirty} onclick={run}>
              <Icon name="play" size={14} />Run {allShown ? `${shown.length} prompts` : "it"} on {chosen.length} model{chosen.length === 1 ? "" : "s"}
            </button>
            {#if dirty}<span class="hint">Save the prompt first.</span>{/if}
          {/if}
        </div>
        {#if prog}
          <div class="prog" role="status" aria-live="polite">
            <div class="pl">
              <span class="spin"></span>
              <span><b>{prog.model}</b> · {prog.prompt} · run {prog.i} of {prog.n}</span>
              {#if prog.out?.stats.ttft != null}<span class="mono">{sec(prog.out.stats.ttft)} to start · {tps(prog.out.stats.tps)} tok/s</span>{/if}
            </div>
            {#if prog.out?.output}<pre>{prog.out.output.slice(-600)}</pre>{:else if prog.out?.reasoning}<p class="hint">Thinking… {prog.out.reasoning} characters</p>{/if}
          </div>
        {/if}
      {/if}
    </section>

    {#if batchSummary.length}
      <section class="card" aria-labelledby="batch-h">
        <div class="hd"><h2 id="batch-h">This run, over {batch.length} answers</h2><span class="sub">medians per model</span></div>
        {@render table(batchSummary)}
      </section>
    {/if}

    {#if sel && !isNew}
      <section class="card" aria-labelledby="res-h">
        <div class="hd">
          <h2 id="res-h">Results</h2>
          <span class="sub">{results.length ? `${results.length} answer${results.length === 1 ? "" : "s"} · medians per model` : ""}</span>
        </div>
        {#if resErr}
          <p class="hint er">{resErr}</p>
        {:else if !results.length}
          <p class="empty">No answers yet. Run it above, or send it from the Playground's Prompts menu in a new chat.</p>
        {:else}
          {@render table(summary)}
          {#if sel.expect}<p class="expect"><b>A good answer:</b> {sel.expect}</p>{/if}
          <ul class="answers">
            {#each results as r (r.id)}
              <li>
                <details>
                  <summary>
                    <b>{modelLabel(r)}</b>
                    <span class="chip">{engineName(r.engine)}</span>
                    {#if r.error}<span class="chip er">No answer</span>{/if}
                    <span class="mono">{sec(r.ttft)} · {tps(r.tps)} tok/s · {r.tokens ?? "–"} tok · {sec(r.ms)}</span>
                    <span class="fa">{r.source === "playground" ? "Playground · " : ""}{when(r.at)}</span>
                  </summary>
                  <div class="ans">
                    {#if r.error}<p class="hint er">{r.error}</p>{/if}
                    {#if r.output}<div class="md">{@html renderAssistant(r.output)}</div>{/if}
                    {#if (r.output_bytes ?? 0) > (r.output?.length ?? 0)}<p class="hint">The answer was longer; the start is kept.</p>{/if}
                    <p class="meta">
                      {#if r.reasoning_bytes}Thought for {r.reasoning_bytes.toLocaleString()} characters · {/if}temperature {r.temperature ?? "–"} · top-p {r.top_p ?? "–"}{#if r.ref} · <span class="mono">{r.ref}</span>{/if}
                      <button class="small ghost" type="button" onclick={() => dropResult(r)}><Icon name="trash" size={13} />Delete</button>
                    </p>
                  </div>
                </details>
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
  </div>
</div>

{#snippet table(rows: ReturnType<typeof summarize>)}
  {@const b = { ttft: Math.min(...rows.map((s) => s.ttft ?? Infinity)), tps: Math.max(...rows.map((s) => s.tps ?? -Infinity)) }}
  <div class="tw">
    <table class="tbl">
      <thead><tr><th>Model</th><th>Runs</th><th>First token</th><th>Speed</th><th>Tokens</th><th>Total</th></tr></thead>
      <tbody>
        {#each rows as s (s.key)}
          <tr>
            <td><b>{s.label}</b> <span class="fa">{engineName(s.last.engine)} · {computeName(s.last.compute)}</span></td>
            <td>{s.runs}{#if s.failed} <span class="chip er">{s.failed} failed</span>{/if}</td>
            <td class="mono" class:win={rows.length > 1 && s.ttft === b.ttft}>{sec(s.ttft)}</td>
            <td class="mono" class:win={rows.length > 1 && s.tps === b.tps}>{tps(s.tps)} tok/s</td>
            <td class="mono">{s.tokens ?? "–"}</td>
            <td class="mono">{sec(s.ms)}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<style>
  .pp { display: grid; grid-template-columns: 300px minmax(0, 1fr); gap: 16px; align-items: start; }
  .list { position: sticky; top: 12px; display: flex; flex-direction: column; gap: 10px; max-height: calc(100dvh - 120px); overflow: auto; }
  .list .hd { margin-bottom: 0; }
  .list ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
  .list a { display: flex; flex-direction: column; gap: 2px; padding: 8px 10px; border-radius: 9px; color: inherit; text-decoration: none; border: 1px solid transparent; }
  .list a:hover { background: var(--p2); }
  .list a.on { background: var(--p3); border-color: var(--ln2); }
  .list a b { font-size: 13px; font-weight: 600; }
  .list a span { font-size: 12px; color: var(--mu); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .list a small { font-size: 11px; color: var(--fa); }
  .main { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
  .form { display: flex; flex-direction: column; gap: 12px; }
  .f { display: flex; flex-direction: column; gap: 5px; }
  .f > span { font-size: 13px; color: var(--mu); }
  .f small { color: var(--fa); }
  .f textarea { resize: vertical; font: inherit; }
  .two { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
  details summary { cursor: pointer; font-size: 13px; color: var(--mu); }
  details[open] > summary { margin-bottom: 12px; }
  .tg-list { list-style: none; margin: 0 0 12px; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .tg-list label { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .opts { display: flex; flex-wrap: wrap; gap: 10px 18px; align-items: center; font-size: 13px; color: var(--mu); }
  .opts label { display: flex; align-items: center; gap: 6px; }
  .opts input[type="number"] { width: 72px; }
  .go { display: flex; align-items: center; gap: 10px; margin-top: 12px; }
  .prog { margin-top: 12px; padding: 10px 12px; border-radius: 10px; background: var(--p2); border: 1px solid var(--ln); }
  .pl { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 13px; }
  .pl .mono { margin-left: auto; color: var(--mu); }
  .prog pre { margin: 8px 0 0; max-height: 160px; overflow: auto; white-space: pre-wrap; font-size: 12px; color: var(--mu); }
  .tw { overflow-x: auto; }
  td.win { color: var(--ok); font-weight: 600; }
  .fa { color: var(--fa); font-size: 12px; }
  .er { color: var(--er); }
  .dg { color: var(--er); }
  .expect { font-size: 13px; color: var(--mu); margin: 14px 0 0; }
  .answers { list-style: none; margin: 14px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
  .answers details { border: 1px solid var(--ln); border-radius: 10px; padding: 8px 12px; }
  .answers summary { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 13px; color: inherit; }
  .answers summary .mono { color: var(--mu); }
  .answers summary .fa { margin-left: auto; }
  .ans { display: flex; flex-direction: column; gap: 8px; }
  .ans .md { font-size: 14px; line-height: 1.55; overflow-x: auto; }
  .ans .md :global(pre) { overflow-x: auto; padding: 10px; border-radius: 8px; background: var(--p2); }
  .meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 0; font-size: 12px; color: var(--fa); }
  .meta button { margin-left: auto; }
  .ck { display: flex; align-items: center; gap: 6px; }
  @media (max-width: 860px) {
    .pp { grid-template-columns: 1fr; }
    .list { position: static; max-height: none; }
    .two { grid-template-columns: 1fr; }
  }
</style>
