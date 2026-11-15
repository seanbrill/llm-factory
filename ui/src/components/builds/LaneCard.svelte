<script lang="ts">
  // One engine's build lane (sheet frames f10, f17): the engine's state (and
  // its fix when it's down or too small), the running build with its phase,
  // progress, route and Cancel, then the queued builds in run order. Queued
  // builds reorder by dragging, from their handle's menu, or with the arrow
  // keys on the handle.
  import { tick } from "svelte";
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import MoveMenu from "./MoveMenu.svelte";
  import { moveJob, moveJobTo, stopJob, type MoveTo } from "../../lib/build.svelte";
  import { progress, routeText } from "../../lib/jobs";
  import {
    jobRoute, leftText, modalityText, modelOf, ordinal, phaseText, placeText,
    reusesCompile, waitText, type Lane,
  } from "../../lib/builds";
  import { catalog, sysinfo } from "../../lib/stores";
  import { href } from "../../lib/router";
  import { clock, computeChip, computeName } from "../../lib/format";
  import type { BuildJob } from "../../lib/types";

  let { lane, selected = "", compact = false }: { lane: Lane; selected?: string; compact?: boolean } = $props();

  const r = $derived(lane.running);

  /** The meta line under a build's name: cache reuse, else its route, else what it is. */
  function meta(j: BuildJob, ahead: BuildJob[]): string {
    const m = modelOf(j, $catalog);
    if (j.status === "queued" && reusesCompile(j, ahead, $catalog)) return "reuses the compile";
    const route = jobRoute(j, m, $sysinfo);
    if (route.transfer) return routeText(route.build, route.target);
    return j.kind === "ensemble" ? "ensemble" : modalityText(m);
  }

  // ---- cancel (running: asks first) ---------------------------------------------
  let confirmFor = $state("");
  let stopping = $state(false);
  async function cancelRunning(j: BuildJob) {
    stopping = true;
    const ok = await stopJob(j);
    stopping = false;
    confirmFor = "";
    if (ok) said = `Canceled the build of ${j.name || j.ref}.`;
  }
  // The question takes the focus on its safe answer; Escape or Keep building
  // hands it back to the stop button.
  async function askStop(j: BuildJob) {
    confirmFor = j.id;
    await tick();
    document.querySelector<HTMLElement>(`[data-keep="${j.id}"]`)?.focus();
  }
  async function keepBuilding(j: BuildJob) {
    confirmFor = "";
    await tick();
    document.querySelector<HTMLElement>(`[data-stop="${j.id}"]`)?.focus();
  }
  function onConfirmKey(e: KeyboardEvent, j: BuildJob) {
    if (e.key !== "Escape") return;
    e.preventDefault();
    e.stopPropagation();
    keepBuilding(j);
  }

  // ---- reorder ---------------------------------------------------------------------
  let menuFor = $state("");
  let said = $state(""); // announced after a move
  let dragId = $state("");
  let dropAt = $state(-1); // insertion point among the queued builds (0..n)

  async function refocus(id: string) {
    await tick();
    document.querySelector<HTMLElement>(`[data-grip="${id}"]`)?.focus();
  }

  async function announce(j: BuildJob) {
    await tick();
    const q = lane.queued;
    const at = q.findIndex((x) => x.id === j.id);
    if (at >= 0) said = `${j.name || j.ref} is now ${at === 0 ? "next" : ordinal(q[at].position + 1) + " in line"} on ${lane.name}.`;
  }

  async function move(j: BuildJob, to: MoveTo, keepFocus = true) {
    menuFor = "";
    if (await moveJob(j, to)) announce(j);
    if (keepFocus) refocus(j.id);
  }

  async function remove(j: BuildJob) {
    menuFor = "";
    await stopJob(j);
    said = `${j.name || j.ref} was removed from the queue.`;
  }

  function onGripKey(e: KeyboardEvent, j: BuildJob, i: number) {
    const n = lane.queued.length;
    let to: MoveTo | "" = "";
    if (e.key === "ArrowUp" && i > 0) to = e.altKey ? "top" : "up";
    else if (e.key === "ArrowDown" && i < n - 1) to = e.altKey ? "bottom" : "down";
    else if (e.key === "Home" && i > 0) to = "top";
    else if (e.key === "End" && i < n - 1) to = "bottom";
    if (!to) return;
    e.preventDefault();
    move(j, to);
  }

  function onDragStart(e: DragEvent, j: BuildJob) {
    dragId = j.id;
    menuFor = "";
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/plain", j.id);
    }
  }
  function onDragOver(e: DragEvent, i: number) {
    if (!dragId) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
    const box = (e.currentTarget as HTMLElement).getBoundingClientRect();
    dropAt = e.clientY < box.top + box.height / 2 ? i : i + 1;
  }
  async function onDrop(e: DragEvent) {
    e.preventDefault();
    const q = lane.queued;
    const from = q.findIndex((x) => x.id === dragId);
    let to = dropAt;
    const j = q[from];
    dragId = "";
    dropAt = -1;
    if (!j || to < 0) return;
    if (to > from) to -= 1;
    if (to !== from && (await moveJobTo(j, from, to, q.length))) announce(j);
  }
  function onDragEnd() {
    dragId = "";
    dropAt = -1;
  }
</script>

<section class="card lane" class:compact aria-label="{lane.name} lane">
  <div class="lh">
    {#if compact}
      <p class="lbl">{lane.name} lane</p>
      {#if lane.chip && lane.chip.t !== "Building"}<span class={lane.chip.c}>{lane.chip.t}</span>{/if}
    {:else}
      <span class="dot {lane.dot}"></span>
      <b>{lane.name}</b>
      {#if lane.chip}<span class={lane.chip.c}>{lane.chip.t}</span>{/if}
    {/if}
    {#if lane.facts}<span class="r">{lane.facts}</span>{/if}
  </div>

  {#if lane.note}
    <details class="note {lane.note.tone}" open={lane.note.tone === "er"}>
      <summary>
        <Icon name={lane.note.tone === "er" ? "errc" : "warn"} size={15} />
        <span>{lane.note.title}</span>
        <Icon name="chevronDown" size={14} cls="cv" />
      </summary>
      <p>{lane.note.text}</p>
      {#if lane.note.cmd}<CodeLine cmd={lane.note.cmd} />{/if}
    </details>
  {/if}

  {#if !lane.jobs.length}
    <p class="idle">
      {#if lane.res && !lane.res.reachable}Builds you queue for {lane.name} wait here until it's running.
      {:else}Nothing building. Builds for {lane.name} run here one at a time.{/if}
    </p>
  {/if}

  {#if lane.jobs.length}
  <ol class="jobs" aria-label="{lane.name} builds in run order">
  {#if r}
    {@const p = progress(r)}
    {@const left = leftText(r)}
    <li class="jr run" class:sel={selected === r.id} id="job-{r.id}">
      <span class="spin" aria-hidden="true"></span>
      <div class="jm">
        <a class="nm" href={href("builds", r.id)} draggable="false" aria-current={selected === r.id ? "true" : undefined}>{r.name || r.ref}</a>
        <div class="m">
          {#if !compact}<span class={computeChip(r.compute)}>{computeName(r.compute)}</span>{/if}
          <span class="mt">{compact ? `${phaseText(r)} · ${computeName(r.compute)}` : meta(r, [])}</span>
        </div>
      </div>
      <span class="r">{clock(r.elapsed_ms)}</span>
      <div class="pg">
        {#if confirmFor === r.id}
          <div class="confirm" role="group" aria-label="Cancel this build?">
            <span>Cancel this build?</span>
            <button class="small danger" onclick={() => cancelRunning(r)} onkeydown={(e) => onConfirmKey(e, r)} disabled={stopping}>
              {#if stopping}<span class="spin"></span>{/if}Yes, cancel
            </button>
            <button class="small ghost" data-keep={r.id} onclick={() => keepBuilding(r)} onkeydown={(e) => onConfirmKey(e, r)}>Keep building</button>
          </div>
        {:else}
          {#if !compact}
            <div class="row">
              <span class="ph" title={phaseText(r)}>{phaseText(r)}</span>
              {#if left}<span class="lf">{left}</span>{/if}
            </div>
          {/if}
          <div class="barrow">
            <div class="bar" class:busy={p == null} role="progressbar" aria-label="Progress of {r.name || r.ref}" aria-valuemin={0} aria-valuemax={100} aria-valuenow={p == null ? undefined : Math.round(p * 100)}>
              <i style="width:{p == null ? 30 : Math.round(p * 100)}%"></i>
            </div>
            <button class="small ghost icon stop" data-stop={r.id} onclick={() => askStop(r)} title="Cancel this build" aria-label="Cancel the build of {r.name || r.ref}">
              <Icon name="stopsq" size={14} />
            </button>
          </div>
        {/if}
      </div>
    </li>
  {/if}

  {#each lane.queued as j, i (j.id)}
    {@const w = waitText(j)}
    <!-- Dragging is the mouse shortcut; the handle's menu and arrow keys do the same. -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <li
      class="jr q"
      class:sel={selected === j.id}
      class:dragging={dragId === j.id}
      class:drop-before={dropAt === i}
      class:drop-after={dropAt === i + 1 && i === lane.queued.length - 1}
      id="job-{j.id}"
      draggable={!compact && lane.queued.length > 1}
      ondragstart={(e) => onDragStart(e, j)}
      ondragover={(e) => onDragOver(e, i)}
      ondrop={onDrop}
      ondragend={onDragEnd}
    >
      <span class="gw">
        <button
          class="g"
          class:n={compact}
          data-grip={j.id}
          data-menu-owner
          aria-haspopup="menu"
          aria-expanded={menuFor === j.id}
          aria-label="Move or remove {j.name || j.ref}, {placeText(j, i)} in line. Arrow keys move it."
          title="Drag to reorder, or click for options"
          onclick={() => (menuFor = menuFor === j.id ? "" : j.id)}
          onkeydown={(e) => onGripKey(e, j, i)}
        >
          {#if compact}{j.position + 1}{:else}<Icon name="grip" size={15} stroke={2.6} />{/if}
        </button>
        {#if menuFor === j.id}
          <MoveMenu
            name={j.name || j.ref}
            first={i === 0}
            last={i === lane.queued.length - 1}
            onmove={(to) => move(j, to)}
            onremove={() => remove(j)}
            onclose={(back) => {
              menuFor = "";
              if (back) refocus(j.id);
            }}
          />
        {/if}
      </span>
      <div class="jm">
        <a class="nm" href={href("builds", j.id)} draggable="false" aria-current={selected === j.id ? "true" : undefined}>{j.name || j.ref}</a>
        <div class="m">
          {#if !compact}<span class={computeChip(j.compute)}>{computeName(j.compute)}</span>{/if}
          <span class="mt">{compact ? `${computeName(j.compute)} · ${meta(j, lane.jobs.slice(0, lane.jobs.indexOf(j)))}` : meta(j, lane.jobs.slice(0, lane.jobs.indexOf(j)))}</span>
        </div>
      </div>
      <span class="r" title={w ? `Starts in about ${w.slice(1)}` : undefined}>
        {#if compact}{w || placeText(j, i)}{:else}{placeText(j, i)}{#if w}<small>{w}</small>{/if}{/if}
      </span>
    </li>
  {/each}
  </ol>
  {/if}
  <p class="sr" aria-live="polite">{said}</p>
</section>

<style>
  .lane { padding: 16px; min-width: 0; }
  .lh { display: flex; align-items: center; gap: 8px; margin-bottom: 6px; min-width: 0; }
  .lh b { font-size: 14px; }
  .lh .r { margin-left: auto; font-size: 12px; color: var(--mu); font-family: var(--font-mono); white-space: nowrap; }
  .compact .lh .lbl { margin: 0; }

  /* flow-root keeps the command's bottom margin inside the tinted box. */
  .note { display: flow-root; margin: 6px 0 8px; border-radius: var(--r); font-size: 12.5px; }
  .note summary {
    display: flex;
    align-items: flex-start;
    gap: 8px;
    padding: 9px 10px;
    cursor: pointer;
    list-style: none;
    border-radius: var(--r);
  }
  .note summary::-webkit-details-marker { display: none; }
  .note summary span { flex: 1; color: var(--ink); font-weight: 500; }
  .note.wa { background: var(--was); }
  .note.er { background: var(--ers); }
  .note.wa summary > :global(.ico:first-child) { color: var(--wa); margin-top: 1px; }
  .note.er summary > :global(.ico:first-child) { color: var(--er); margin-top: 1px; }
  .note summary :global(.cv) { color: var(--fa); margin-top: 2px; transition: transform 0.15s ease; }
  .note[open] summary :global(.cv) { transform: rotate(180deg); }
  .note p { color: var(--mu); padding: 0 10px 8px 33px; }
  .note :global(.code) { margin: 0 10px 10px 33px; align-items: flex-start; }
  .note :global(.code > span) { white-space: normal; overflow-wrap: anywhere; line-height: 1.5; padding: 2px 0; }

  .idle { font-size: 12.5px; color: var(--mu); padding: 2px 0 2px; }
  .jobs { list-style: none; margin: 0; padding: 0; }

  .jr {
    position: relative;
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr) auto;
    gap: 4px 10px;
    align-items: center;
    padding: 10px 0;
    border-top: 1px solid var(--ln);
  }
  .lh + .jobs > .jr:first-child, .note + .jobs > .jr:first-child { border-top-color: transparent; }
  .jm { min-width: 0; }
  .nm {
    display: block;
    font-weight: 600;
    font-size: 13.5px;
    color: var(--ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .nm:hover { text-decoration: none; }
  /* The name's link covers the row; the controls sit above it. */
  .nm::after { content: ""; position: absolute; inset: 0; border-radius: 10px; }
  .nm:focus-visible { outline: none; }
  .nm:focus-visible::after { outline: 2px solid var(--pr); outline-offset: -2px; }
  .jr:hover:not(.sel) { background: color-mix(in srgb, var(--p2) 60%, transparent); }
  .m { font-size: 12.5px; color: var(--mu); display: flex; gap: 6px; align-items: center; margin-top: 2px; min-width: 0; }
  .mt { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .r {
    font-size: 12px;
    color: var(--mu);
    font-family: var(--font-mono);
    white-space: nowrap;
    text-align: right;
    display: flex;
    flex-direction: column;
    align-items: flex-end;
  }
  .r small { font-size: 11px; color: var(--fa); }
  .jr.sel {
    background: var(--p2);
    border-radius: 10px;
    border-top-color: transparent;
    padding: 10px;
    margin: 0 -10px;
    box-shadow: inset 3px 0 0 var(--pr);
  }
  .jr.sel + .jr { border-top-color: transparent; }

  .pg { grid-column: 2 / 4; display: flex; flex-direction: column; gap: 5px; min-width: 0; position: relative; z-index: 1; }
  .pg .row { display: flex; justify-content: space-between; gap: 10px; font-size: 12px; color: var(--mu); }
  .ph { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lf { white-space: nowrap; }
  .barrow { display: flex; align-items: center; gap: 8px; }
  .barrow .bar { flex: 1; }
  .stop { width: 24px !important; min-height: 24px; color: var(--fa); }
  .stop :global(svg) { fill: currentColor; }
  .stop:hover { color: var(--er); }
  .confirm { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12.5px; color: var(--ink); }

  .gw { position: relative; z-index: 2; display: flex; }
  .g {
    width: 16px;
    min-height: 24px;
    padding: 0;
    border: 0;
    background: none;
    color: var(--fa);
    cursor: grab;
  }
  .g:hover { background: none; color: var(--ink); }
  .g.n {
    width: 18px;
    height: 18px;
    min-height: 18px;
    border-radius: 50%;
    background: var(--p3);
    font-size: 10.5px;
    font-weight: 700;
    color: var(--mu);
    cursor: pointer;
  }
  .jr.q[draggable="true"] { cursor: grab; }
  .jr.dragging { opacity: 0.45; }
  .jr.drop-before { box-shadow: inset 0 2px 0 var(--pr); }
  .jr.drop-after { box-shadow: inset 0 -2px 0 var(--pr); }

  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }

  /* Phone (f17): rounder card, the queue numbered. */
  .compact { border-radius: 16px; padding: 14px; }
  .compact .jr { grid-template-columns: 18px minmax(0, 1fr) auto; padding: 9px 0; }
  .compact .nm { font-size: 14px; }
  .compact .m { font-size: 12px; }
  .compact .jr.sel { padding: 9px 10px; }
</style>
