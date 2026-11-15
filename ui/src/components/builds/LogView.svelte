<script lang="ts">
  // A build's log (sheet frame f10): numbered lines with the errors marked. A
  // failed build opens on the lines around its cause; a running one follows the
  // tail unless the reader scrolls up. On a phone (f17) it starts folded into
  // the one line that matters, a tap away.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { views } from "../../lib/build.svelte";
  import { logLines, focusAt, isErrorLine, saveText, logFileName } from "../../lib/builds";
  import type { BuildJob } from "../../lib/types";

  let { job, compact = false }: { job: BuildJob; compact?: boolean } = $props();

  type Mode = "all" | "around" | "errors";
  const CAP = 2500; // lines drawn at once in "All"; more on request

  const v = $derived(views[job.id]);
  const log = $derived(logLines(v));
  const n = $derived(log.lines.length);
  const focus = $derived(job.status === "error" ? focusAt(log.lines) : -1);
  const errs = $derived.by(() => {
    const out: number[] = [];
    log.lines.forEach((l, i) => {
      if (isErrorLine(l)) out.push(i);
    });
    return out;
  });
  const errSet = $derived(new Set(errs));

  let picked = $state<Mode | null>(null);
  const mode = $derived<Mode>(picked ?? (focus >= 0 ? "around" : "all"));

  // "All" draws a window: the tail, or the stretch around a failure's cause.
  let earlier = $state(0);
  let later = $state(0);
  const auto = $derived.by(() => {
    if (focus >= 0 && n > CAP) {
      const s = Math.max(0, Math.min(focus - 200, n - CAP));
      return { s, e: Math.min(n, s + CAP) };
    }
    return { s: Math.max(0, n - CAP), e: n };
  });
  const win = $derived({ s: Math.max(0, auto.s - earlier), e: Math.min(n, auto.e + later) });

  const rows = $derived.by(() => {
    let idx: number[];
    if (mode === "around" && focus >= 0) {
      idx = [];
      for (let i = Math.max(0, focus - 3); i < Math.min(n, focus + 9); i++) idx.push(i);
    } else if (mode === "errors") idx = errs;
    else {
      idx = [];
      for (let i = win.s; i < win.e; i++) idx.push(i);
    }
    return idx.map((i) => ({ i, no: log.first + i + 1, text: log.lines[i], err: errSet.has(i), focus: i === focus }));
  });

  const placeholder = $derived(
    job.status === "queued"
      ? "Waiting for its turn…"
      : job.line_count > 0
        ? "Loading the log…"
        : job.active
          ? "Starting…"
          : "This build wrote no log.",
  );

  // The one line a folded log shows: the cause, else the newest line that says
  // something (a finished build's last word is a bare DONE).
  const headline = $derived.by(() => {
    if (focus >= 0) return log.lines[focus];
    for (let i = n - 1; i >= Math.max(0, n - 5); i--) {
      const l = log.lines[i]?.trim();
      if (l && l !== "DONE") return l;
    }
    return log.lines[n - 1] || job.last_line || placeholder;
  });

  let unfolded = $state<boolean | null>(null);
  const open = $derived(unfolded ?? !compact);
  let box: HTMLDivElement | undefined = $state();
  let stick = true;
  let following = $state(true);
  let placed = ""; // what the box was last scrolled for

  function onScroll() {
    if (!box) return;
    stick = box.scrollTop + box.clientHeight >= box.scrollHeight - 24;
    if (following !== stick) following = stick;
  }

  // Position the box: on the cause for a failure, at the end otherwise, and
  // keep a running build's tail in view while the reader hasn't scrolled up.
  $effect(() => {
    const el = box;
    if (!el || !open) return;
    const key = `${mode}:${focus}:${win.s}`;
    void rows.length;
    if (mode === "all" && job.status === "running") {
      if (stick) el.scrollTop = el.scrollHeight;
      return;
    }
    if (placed === key) return;
    placed = key;
    const f = el.querySelector<HTMLElement>("[data-focus]");
    // The excerpt around a cause opens at its top when the cause shows from there.
    const fits = !!f && f.offsetTop + f.offsetHeight <= el.clientHeight - 24;
    if (mode === "around" && fits) el.scrollTop = 0;
    else if (mode !== "errors" && f) el.scrollTop = Math.max(0, f.offsetTop - el.clientHeight / 3);
    else if (mode === "all") el.scrollTop = el.scrollHeight;
    else el.scrollTop = 0;
  });

  function pick(m: Mode) {
    picked = m;
    placed = "";
    stick = true;
    following = true;
  }

  const tabs = $derived([
    { m: "all" as Mode, label: "All" },
    ...(focus >= 0 ? [{ m: "around" as Mode, label: compact ? "Cause" : "Around the error" }] : []),
    ...(errs.length ? [{ m: "errors" as Mode, label: `Errors · ${errs.length}` }] : []),
  ]);
  // One way to read it needs no tabs: the header says how long it is instead.
  const tabbed = $derived(n > 0 && tabs.length > 1);

  // Arrow keys move between the tabs (the ARIA tabs pattern).
  function onTabKey(e: KeyboardEvent) {
    if (e.key !== "ArrowRight" && e.key !== "ArrowLeft" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const at = tabs.findIndex((t) => t.m === mode);
    let next = at;
    if (e.key === "ArrowRight") next = (at + 1) % tabs.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + tabs.length) % tabs.length;
    else if (e.key === "Home") next = 0;
    else next = tabs.length - 1;
    pick(tabs[next].m);
    document.getElementById(`logtab-${job.id}-${tabs[next].m}`)?.focus();
  }

  function latest() {
    if (!box) return;
    stick = true;
    following = true;
    box.scrollTop = box.scrollHeight;
  }
</script>

<div class="log" class:compact>
  {#if compact && !open}
    <button class="fold" class:er={focus >= 0} onclick={() => (unfolded = true)} aria-expanded="false">
      <span class="ht">{headline}</span>
      <span class="go">Log<Icon name="chevronRight" size={15} /></span>
    </button>
  {:else}
    <div class="lt">
      {#if !compact || !tabbed}<b>Log</b>{/if}
      {#if !tabbed}
        {#if job.line_count}<span class="cnt">{job.line_count.toLocaleString()} {job.line_count === 1 ? "line" : "lines"}</span>{/if}
      {:else}
        <div class="tabs" role="tablist" aria-label="Which lines" tabindex="-1" onkeydown={onTabKey}>
          {#each tabs as t (t.m)}
            <button
              role="tab"
              id="logtab-{job.id}-{t.m}"
              aria-selected={mode === t.m}
              aria-controls="log-{job.id}"
              tabindex={mode === t.m ? 0 : -1}
              class:on={mode === t.m}
              onclick={() => pick(t.m)}>{t.label}</button>
          {/each}
        </div>
      {/if}
      <div class="sp">
        {#if job.status === "running" && mode === "all" && !following}
          <button class="small ghost" onclick={latest}><Icon name="arrowDown" size={14} />Latest</button>
        {/if}
        {#if v?.log}
          {#if compact}<CopyButton text={v.log} title="Copy the log" />{/if}
          <button class="small" class:icon={compact} onclick={() => saveText(logFileName(job), v?.log ?? "")} title="Download the log" aria-label={compact ? "Download the log" : undefined}>
            <Icon name="download" size={14} />{#if !compact}Download{/if}
          </button>
        {/if}
        {#if compact}
          <button class="small ghost icon" onclick={() => (unfolded = false)} aria-label="Fold the log"><Icon name="chevronUp" size={15} /></button>
        {/if}
      </div>
    </div>
    <!-- A scrolling box takes focus so the keyboard can scroll it. -->
    <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
    <div
      class="lines"
      id="log-{job.id}"
      role={tabbed ? "tabpanel" : "region"}
      tabindex="0"
      aria-labelledby={tabbed ? `logtab-${job.id}-${mode}` : undefined}
      aria-label={tabbed ? undefined : `Build log of ${job.name || job.ref}`}
      style="--gw: {Math.max(3, String(log.first + n).length)}ch"
      bind:this={box}
      onscroll={onScroll}>
      {#if !n}
        <p class="ph">{placeholder}</p>
      {:else}
        {#if mode === "all" && win.s > 0}
          <button class="more" onclick={() => (earlier += CAP)}>Show {Math.min(CAP, win.s).toLocaleString()} earlier lines</button>
        {/if}
        {#if mode === "around" && focus > 3}
          <button class="more" onclick={() => pick("all")}>Show the {(focus - 3).toLocaleString()} lines before</button>
        {/if}
        {#each rows as r (r.i)}
          <div class="ln" class:e={r.err} class:f={r.focus} data-focus={r.focus || undefined}><i>{r.no}</i><span>{r.text}</span></div>
        {/each}
        {#if mode === "all" && win.e < n}
          <button class="more" onclick={() => (later += CAP)}>Show {Math.min(CAP, n - win.e).toLocaleString()} later lines</button>
        {/if}
        {#if mode === "around" && focus + 9 < n}
          <button class="more" onclick={() => pick("all")}>Show the {(n - focus - 9).toLocaleString()} lines after</button>
        {/if}
      {/if}
    </div>
  {/if}
</div>

<style>
  .log {
    border: 1px solid var(--ln);
    border-radius: 12px;
    overflow: hidden;
    min-width: 0;
  }
  .lt {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 8px 7px 14px;
    background: var(--p2);
    border-bottom: 1px solid var(--ln);
    min-width: 0;
  }
  .lt b { font-size: 13px; margin-right: 6px; }
  .cnt { font: 11.5px var(--font-mono); color: var(--fa); white-space: nowrap; }
  .tabs {
    display: flex;
    gap: 2px;
    padding: 3px;
    border-radius: 8px;
    background: var(--cv);
    min-width: 0;
    overflow-x: auto;
  }
  .tabs button {
    min-height: 0;
    height: 24px;
    padding: 0 10px;
    border: 0;
    border-radius: 6px;
    background: transparent;
    font-size: 12px;
    font-weight: 400;
    color: var(--mu);
  }
  .tabs button:hover { color: var(--ink); background: var(--p2); }
  .tabs button.on { background: var(--p3); color: var(--ink); }
  .sp { margin-left: auto; display: flex; gap: 6px; align-items: center; }

  .lines {
    position: relative;
    max-height: 380px;
    overflow: auto;
    padding: 10px 0;
    background: var(--cv);
    font: 11.5px/1.75 var(--font-mono);
    color: var(--mu);
  }
  .lines:focus-visible { outline-offset: -2px; }
  .ln {
    display: grid;
    /* The number column fits the longest line number (--gw, in digits). */
    grid-template-columns: calc(var(--gw, 4ch) + 28px) minmax(0, 1fr);
    padding-right: 14px;
  }
  .ln i {
    font-style: normal;
    color: var(--fa);
    opacity: 0.7;
    padding-left: 14px;
    user-select: none;
  }
  /* Long lines wrap, indented after the first row so each line still reads as one. */
  .ln span { white-space: pre-wrap; overflow-wrap: anywhere; padding-left: 2ch; text-indent: -2ch; }
  .ln.e { background: var(--ers); color: var(--er); }
  .ln.e i { color: var(--er); opacity: 0.75; }
  .ln.f { box-shadow: inset 3px 0 0 var(--er); }
  .ph { padding: 4px 14px; color: var(--fa); font-family: var(--font-body); font-size: 12.5px; }
  .more {
    display: block;
    width: calc(100% - 28px);
    margin: 2px 14px 6px;
    min-height: 26px;
    font: 500 12px var(--font-body);
    color: var(--mu);
    background: var(--p1);
    border-color: var(--ln);
  }

  /* Folded (phone): the line that matters, a tap away. */
  .fold {
    width: 100%;
    min-height: 44px;
    justify-content: flex-start;
    gap: 8px;
    padding: 10px 12px;
    border: 0;
    border-radius: 0;
    background: var(--cv);
    font: 11px/1.4 var(--font-mono);
    color: var(--mu);
    text-align: left;
  }
  .fold.er { color: var(--er); }
  .fold:hover { background: var(--p1); }
  .ht { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .go { display: inline-flex; align-items: center; gap: 2px; color: var(--fa); font: 500 12px var(--font-body); }
  .compact .lines { max-height: 55vh; font-size: 11px; }
  .compact .lt { padding-left: 8px; }
  .compact .tabs button { padding: 0 8px; }
  .compact .ln { grid-template-columns: calc(var(--gw, 4ch) + 18px) minmax(0, 1fr); padding-right: 10px; }
  .compact .ln i { padding-left: 10px; }
  .compact .more { width: calc(100% - 20px); margin: 2px 10px 6px; }
</style>
