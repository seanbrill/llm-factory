<script lang="ts">
  // Benchmark on this machine (sheet frames f20, f24): starts the model once per
  // setting in its own container, sends the same task, measures it, and lets
  // the user keep one. A user-triggered run; the server removes each container
  // when it's measured, and on cancel.
  import Icon from "../Icon.svelte";
  import Seg from "./Seg.svelte";
  import { toast } from "../../lib/stores";
  import { engineName, gb } from "../../lib/format";
  import {
    applyBench, benchState, cancelBench, fastest, sameTuning, startBench, tuningLine,
    type BenchState, type Tuning,
  } from "../../lib/tuning";

  let {
    ref,
    engine,
    compute,
    candidates,
    saved,
    onapplied,
  }: {
    ref: string;
    engine: string;
    compute: string;
    candidates: { label: string; tuning: Tuning }[];
    saved: Tuning | null;
    onapplied: () => void;
  } = $props();

  const KEY = "llmfactory.bench.v1";
  let workload = $state<"coding" | "chat">("coding");
  let id = $state("");
  let st = $state<BenchState | null>(null);
  let busy = $state(false);
  let error = $state("");

  // Remember the running benchmark per image, so leaving the page and coming
  // back picks it up again.
  function remembered(): string {
    try {
      return (JSON.parse(sessionStorage.getItem(KEY) || "{}") as Record<string, string>)[engine + ref] || "";
    } catch {
      return "";
    }
  }
  function remember(v: string) {
    try {
      const m = JSON.parse(sessionStorage.getItem(KEY) || "{}") as Record<string, string>;
      if (v) m[engine + ref] = v;
      else delete m[engine + ref];
      sessionStorage.setItem(KEY, JSON.stringify(m));
    } catch {
      /* fine: it just won't resume */
    }
  }

  $effect(() => {
    const prev = remembered();
    id = prev;
    st = null;
    error = "";
  });

  const live = $derived(!!st && (st.state === "queued" || st.state === "running"));

  // Poll while it runs.
  $effect(() => {
    if (!id) return;
    let stop = false;
    let timer: ReturnType<typeof setTimeout>;
    const tick = async () => {
      try {
        const s = await benchState(id);
        if (stop) return;
        st = s;
        if (s.state === "queued" || s.state === "running") timer = setTimeout(tick, 1500);
      } catch (e) {
        if (stop) return;
        // A factory restart forgets old benchmarks.
        error = (e as Error).message;
        id = "";
        remember("");
      }
    };
    tick();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  });

  async function run() {
    busy = true;
    error = "";
    try {
      const r = await startBench({ ref, engine, compute, candidates: candidates.map((c) => c.tuning), labels: candidates.map((c) => c.label), workload });
      id = r.id;
      st = null;
      remember(r.id);
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }

  async function cancel() {
    if (!id) return;
    try {
      await cancelBench(id);
    } catch (e) {
      toast(`Couldn't cancel: ${(e as Error).message}`, 6000);
    }
  }

  async function use(i: number) {
    try {
      await applyBench(id, i);
      toast("Saved. It applies the next time the model starts.", 4000);
      onapplied();
    } catch (e) {
      toast(`Couldn't save it: ${(e as Error).message}`, 6000);
    }
  }

  const results = $derived(st?.results ?? []);
  const best = $derived(fastest(results));
  const pending = $derived.by(() => {
    // Rows for the candidates still to run while it's going.
    if (!live) return [] as string[];
    return candidates.slice(results.length + (st?.current ? 1 : 0)).map((c) => c.label);
  });
  const PHASE: Record<string, string> = { starting: "Starting the model", loading: "Loading the model", warmup: "Warming up", workload: "Measuring" };
  const n1 = (v: number | undefined) => (v == null ? "—" : v >= 100 ? v.toFixed(0) : v.toFixed(1));
</script>

<section class="card" aria-labelledby="bench-h">
  <div class="hd">
    <h2 id="bench-h">Benchmark on this computer</h2>
    {#if live}<span class="chip pr"><span class="spin"></span>{Math.min((st?.step ?? 0) + 1, st?.total ?? candidates.length)} of {st?.total ?? candidates.length}</span>{/if}
  </div>
  {#if !live}
    <p class="sub">
      Starts the model once per setting on {engineName(engine)}, sends the same {workload === "coding" ? "coding task" : "chat"}, and measures it. About a
      minute each. Each runs in its own container, removed when it's measured.
    </p>
  {/if}

  {#if live}
    <ol class="steps">
      {#each results as r, i (i)}
        <li class="done" class:bad={!r.ok}>
          <span class="m">{#if r.ok}<Icon name="check" size={13} />{:else}<Icon name="xmark" size={13} />{/if}</span>
          <span class="n"><b>{r.label}</b><small>{r.ok ? tuningLine(r.tuning) : r.skipped ? r.error || "Skipped: it doesn't fit." : r.error || "It didn't finish."}</small></span>
          <span class="r">{r.ok ? `${n1(r.gen_tps)} tok/s` : "—"}</span>
        </li>
      {/each}
      {#if st?.current}
        <li class="cur">
          <span class="m"><span class="spin"></span></span>
          <span class="n"><b>{st.current}</b><small>{PHASE[st.phase ?? ""] ?? "Working"}{engineName(engine) ? ` on ${engineName(engine)}` : ""}</small><span class="bar busy"><i></i></span></span>
          <span class="r">—</span>
        </li>
      {/if}
      {#each pending as p (p)}
        <li><span class="m"></span><span class="n"><b>{p}</b><small>Waiting</small></span><span class="r">—</span></li>
      {/each}
    </ol>
    <p class="hint">Your other models keep running; the benchmark waits if {engineName(engine)} doesn't have room for the next one.</p>
    <div class="ft"><span class="sp"></span><button type="button" onclick={cancel}>Cancel benchmark</button></div>
  {:else}
    {#if results.length}
      <div class="tw">
        <table class="res">
          <thead><tr><th>Setting</th><th title="Tokens per second reading the prompt">Reads</th><th title="Tokens per second writing the answer">Writes</th><th>First word</th><th>Memory</th><th><span class="sr">Use</span></th></tr></thead>
          <tbody>
            {#each results as r, i (i)}
              {@const isSaved = !!saved && sameTuning(saved, r.tuning)}
              <tr class:best={i === best} class:pick={isSaved}>
                <td>
                  <b>{r.label}</b>
                  {#if !r.ok}<small class="er">{r.error || "It didn't finish."}</small>
                  {:else if i === best}<small><span class="chip ok">Fastest</span></small>
                  {:else if r.draft_accept_rate != null}<small>look-ahead {Math.round(r.draft_accept_rate * 100)}% hit</small>
                  {:else}<small>{tuningLine(r.tuning)}</small>{/if}
                </td>
                <td>{r.ok ? n1(r.prompt_tps) : "—"}</td>
                <td>{r.ok ? n1(r.gen_tps) : "—"}</td>
                <td>{r.ok && r.ttft_ms != null ? `${(r.ttft_ms / 1000).toFixed(1)} s` : "—"}</td>
                <td>{r.ok ? gb(r.mem_gb) : "—"}</td>
                <td>
                  {#if isSaved}<span class="chip pr">Saved</span>
                  {:else if r.ok}<button class="small" type="button" onclick={() => use(i)}>Use</button>{/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="hint">Reads and Writes are tokens per second: reading your prompt, writing the answer.</p>
      {#if st?.state === "error" && st.error}<p class="hint er">It stopped early: {st.error}</p>{/if}
      {#if st?.state === "canceled"}<p class="hint">Canceled. These are the settings it measured before that.</p>{/if}
    {/if}
    {#if error}
      <div class="callout er" role="alert"><Icon name="errc" /><span><b>The benchmark didn't start.</b><p>{error}</p></span></div>
    {/if}
    <div class="ft">
      <Seg label="Workload" value={workload} options={[{ value: "coding", label: "Coding task" }, { value: "chat", label: "Chat" }]} onchange={(v) => (workload = v)} />
      <span class="sp"></span>
      <button type="button" disabled={busy || !candidates.length} onclick={run}>
        <Icon name="refresh" size={14} />{results.length ? "Run again" : `Run ${candidates.length} setting${candidates.length === 1 ? "" : "s"}`}
      </button>
    </div>
  {/if}
</section>

<style>
  .card { display: flex; flex-direction: column; gap: 10px; }
  .hd { margin-bottom: 0; }
  .ft { display: flex; align-items: center; gap: 10px; padding-top: 12px; border-top: 1px solid var(--ln); }
  .sp { flex: 1; }
  .tw { overflow-x: auto; }
  .res { width: 100%; border-collapse: collapse; font-size: 12.5px; }
  .res th { font-size: 11px; letter-spacing: 0.06em; text-transform: uppercase; color: var(--fa); font-weight: 600; text-align: right; padding: 6px; }
  .res th:first-child, .res td:first-child { text-align: left; padding-left: 0; }
  .res td { padding: 8px 6px; border-top: 1px solid var(--ln); text-align: right; font-family: var(--font-mono); vertical-align: middle; white-space: nowrap; }
  .res td:first-child { font-family: var(--font-body); white-space: normal; }
  .res td b { display: block; font-size: 13px; }
  .res td small { display: block; color: var(--mu); font-size: 11.5px; margin-top: 2px; }
  .res td small.er { color: var(--er); }
  .res tr.best td { background: color-mix(in srgb, var(--ok) 6%, transparent); }
  .res tr.pick td:first-child b { color: var(--pr); }
  .res td:last-child { padding-right: 0; width: 56px; }
  .hint.er { color: var(--er); }
  .steps { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .steps li { display: grid; grid-template-columns: 24px minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 10px 0; border-top: 1px solid var(--ln); }
  .steps li:first-child { border-top: 0; }
  .m { width: 20px; height: 20px; border-radius: 50%; display: grid; place-items: center; border: 1.5px solid var(--ln2); margin-top: 1px; }
  .done .m { background: var(--oks); border-color: transparent; color: var(--ok); }
  .done.bad .m { background: var(--ers); color: var(--er); }
  .cur .m { border-color: transparent; }
  .n { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
  .n b { font-size: 13.5px; }
  .n small { font-size: 12px; color: var(--mu); }
  .n .bar { width: min(220px, 100%); margin-top: 4px; }
  .r { font-family: var(--font-mono); font-size: 12.5px; padding-top: 1px; }
  li:not(.done) .r { color: var(--fa); }
</style>
