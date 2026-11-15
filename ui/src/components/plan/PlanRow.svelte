<script lang="ts">
  // One planned build (sheet frame f9): model and image name, compute, where it
  // compiles and runs, how long it should take — then, under it, anything that
  // would stop it or slow it down, each with its fix, and the settings expander.
  import { tick } from "svelte";
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import ItemSettings from "./ItemSettings.svelte";
  import { ago, computeName, dur, engineName } from "../../lib/format";
  import { routeText } from "../../lib/jobs";
  import {
    effectiveCompute, isGPU, cpuEngineId, predictRoute, type LibEntry,
  } from "../../lib/catalog";
  import type { PlanItem } from "../../lib/plan.svelte";
  import type { Model, Persona, PreflightItem, SysInfo } from "../../lib/types";

  interface Msg {
    tone: "er" | "wa" | "info";
    title: string;
    body?: string[];
    cmd?: string;
    actions?: { label: string; run: () => void }[];
  }

  let {
    it,
    m,
    index,
    count,
    pf,
    checking,
    si,
    lib,
    personas,
    reuse,
    failure,
    expanded,
    dragging,
    drop,
    ontoggle,
    onremove,
    onmove,
    oncompute,
    ondragstart,
    ondragover,
    ondrop,
    ondragend,
  }: {
    it: PlanItem;
    m: Model | undefined;
    index: number;
    count: number;
    pf: PreflightItem | undefined;
    checking: boolean;
    si: SysInfo | null;
    lib: LibEntry;
    personas: Persona[];
    reuse: boolean;          // an earlier build in this lane compiles the same runtime
    failure: string;         // why the last queue attempt failed ("" = it didn't)
    expanded: boolean;
    dragging: boolean;
    drop: "" | "above" | "below"; // where a dragged build would land
    ontoggle: () => void;
    onremove: () => void;
    onmove: (to: number) => void;
    oncompute: (c: string) => void;
    ondragstart: (e: DragEvent) => void;
    ondragover: (e: DragEvent) => void;
    ondrop: (e: DragEvent) => void;
    ondragend: () => void;
  } = $props();

  const name = $derived(m?.name ?? it.model_id);

  // Moving a row moves its DOM nodes, which drops focus: put it back on the grip.
  let grip: HTMLButtonElement | undefined = $state();
  async function move(to: number) {
    onmove(to);
    await tick();
    grip?.focus();
  }

  const compute = $derived(m ? effectiveCompute(m, it.compute) : it.compute);
  const guess = $derived(m ? predictRoute(m, it, si) : null);
  const buildEngine = $derived(pf?.build_engine || guess?.buildEngine || it.engine);
  const transfer = $derived(pf ? pf.transfer : !!guess?.transfer);

  // The computes worth a click here: the CPU, the GPUs this machine has, and
  // whatever the item asks for now.
  const options = $derived.by(() => {
    const set = ["cpu"];
    for (const e of si?.engines ?? []) if (e.gpu && !set.includes(e.gpu)) set.push(e.gpu);
    if (!set.includes(it.compute)) set.push(it.compute);
    return set.map((c) => ({ c, ok: !m?.computes?.length || m.computes.includes(c) }));
  });

  const ref = $derived(`${it.image_name.trim() || "local-llm/" + it.model_id}:${it.tag.trim() || "latest"}`);
  const replaces = $derived(
    lib.images.find((im) => im.engine === it.engine && im.ref.replace(/^localhost\//, "") === ref.replace(/^localhost\//, "")),
  );

  const routeNote = $derived.by((): { t: string; c: string } => {
    if (!pf) return { t: checking ? "checking…" : "", c: "" };
    if (pf.error) return { t: "can't build", c: "er" };
    if (pf.run_block) return { t: `can't run on ${engineName(pf.engine)}`, c: "er" };
    if (pf.transfer) return { t: `${engineName(pf.engine)} can't compile`, c: "" };
    if (pf.run_note) return { t: "tight fit", c: "wa" };
    return { t: "builds and runs here", c: "" };
  });

  const timeNote = $derived.by(() => {
    if (!pf) return "";
    if (!pf.compiles) return "no compile";
    return reuse ? "reuses the compile" : "new compile";
  });

  const otherEngine = $derived(it.engine === "podman" ? "docker" : "podman");
  const otherUp = $derived(!!si?.engines?.find((e) => e.engine === otherEngine && e.reachable));
  const canCPU = $derived(!m?.computes?.length || m.computes.includes("cpu"));

  const useCPU = () => ({ label: `Build for the CPU on ${engineName(cpuEngineId(si))} instead`, run: () => oncompute("cpu") });
  // A GPU build can't follow to an engine without that GPU: it builds for the CPU there.
  const useOther = () => {
    const toCPU = isGPU(compute) && si?.engines?.find((e) => e.engine === otherEngine)?.gpu !== compute && canCPU;
    return {
      label: toCPU ? `Build for the CPU on ${engineName(otherEngine)} instead` : `Build for ${engineName(otherEngine)} instead`,
      run: () => {
        it.engine = otherEngine;
        if (toCPU) it.compute = "cpu";
      },
    };
  };
  const PODMAN_CMD = /podman machine stop && podman machine set --memory \d+ && podman machine start/;

  // What stops this build (a callout with the fix) …
  const callouts = $derived.by((): Msg[] => {
    const out: Msg[] = [];
    if (failure) out.push({ tone: "er", title: `It didn't queue: ${failure}`, actions: [{ label: "Remove it from the plan", run: onremove }] });
    if (!m) {
      out.push({ tone: "er", title: "This model isn't in the factory's catalog any more.", actions: [{ label: "Remove it from the plan", run: onremove }] });
      return out;
    }
    if (pf?.error) {
      const lines = pf.error.split("\n").map((l) => l.trim()).filter(Boolean);
      const cmd = pf.error.match(PODMAN_CMD)?.[0];
      const down = /isn't installed|isn't answering|unreachable|isn't running|isn't responding/i.test(pf.error);
      const actions: Msg["actions"] = [];
      if (/not enough memory to compile/i.test(pf.error) && isGPU(compute) && canCPU) actions.push(useCPU());
      if (down && otherUp) actions.push(useOther());
      if (/unknown model id/i.test(pf.error)) actions.push({ label: "Remove it from the plan", run: onremove });
      out.push({
        tone: "er",
        title: lines[0],
        // The server puts the Podman command on a line of its own: show it as a command to copy.
        body: lines.slice(1).filter((l) => !cmd || l !== cmd).map((l) => (cmd ? l.replace(PODMAN_CMD, "the command below") : l)),
        cmd: cmd ?? (down && /podman/i.test(lines[0]) && !/isn't installed/i.test(lines[0]) ? "podman machine start" : undefined),
        actions,
      });
    }
    return out;
  });

  // … and what's worth knowing (a line under the row; engine fixes live under "Before it starts").
  const notes = $derived.by((): Msg[] => {
    const out: Msg[] = [];
    if (!m || pf?.error) return out;
    if (pf?.run_note) {
      const head = pf.run_note.replace(/\s*Give it more memory:.*$/s, "").split(m.name).join("it").replace(/^it /, "It ");
      const cpuThere = isGPU(compute) && canCPU && pf.engine !== cpuEngineId(si);
      const actions: Msg["actions"] = cpuThere
        ? [{ label: `build for the CPU on ${engineName(cpuEngineId(si))} instead.`, run: () => oncompute("cpu") }]
        : [];
      out.push({
        tone: pf.run_block ? "er" : "wa",
        title: head + (pf.run_block ? ` Give ${engineName(pf.engine)} more memory (see Before it starts)${cpuThere ? ", or" : "."}` : ""),
        actions: pf.run_block ? actions : [],
      });
    }
    if (compute !== it.compute) {
      out.push({ tone: "info", title: `There's no ${computeName(it.compute)} build of ${m.name}, so it builds for ${computeName(compute)}.` });
    }
    if (compute === "cuda" && si?.vram_gb) {
      const need = m.min_vram_gb || m.size_gb || 0;
      if (need > si.vram_gb + 0.5) {
        out.push({ tone: "wa", title: `It wants about ${need} GB of VRAM and this GPU has ${si.vram_gb} GB, so it will offload to the CPU and run slowly.` });
      }
    }
    if (replaces) {
      out.push({ tone: "info", title: `Replaces ${replaces.ref}, already built for ${engineName(replaces.engine)}. Change the tag in the settings to keep both.` });
    }
    return out;
  });
</script>

<tr id="plan-{it.model_id}" class="row {drop}" class:dragging class:open={expanded} draggable="true" {ondragstart} {ondragover} {ondrop} {ondragend}>
  <td class="gc">
    <button type="button" class="grip" bind:this={grip} aria-label="Move {name} (now #{index + 1} of {count}); use the up and down arrow keys"
      onkeydown={(e) => {
        if (e.key === "ArrowUp" && index > 0) { e.preventDefault(); move(index - 1); }
        else if (e.key === "ArrowDown" && index < count - 1) { e.preventDefault(); move(index + 1); }
      }}><Icon name="grip" size={16} stroke={2.6} /></button>
  </td>
  <td class="nm">
    <b>{name}</b>
    <span class="l2">
      <span class="mono" title={ref}>{ref.endsWith(":latest") ? ref.slice(0, -7) : ref}</span>
      {#if lib.failed && !lib.active}
        <span class="chip er" title="Its last build failed{lib.failed.finished_at ? ' ' + ago(lib.failed.finished_at) : ''}: {lib.failed.error || 'see its log'}">Retry</span>
      {/if}
      {#if lib.active}
        <span class="chip" class:pr={lib.active.status === "running"} title="A build of it is already {lib.active.status === 'running' ? 'running' : 'queued'} for {engineName(lib.active.engine)}">{lib.active.status === "running" ? "Building now" : "Queued"}</span>
      {/if}
    </span>
  </td>
  <td class="cc">
    <span class="segc" role="group" aria-label="Compute for {name}">
      {#each options as o (o.c)}
        <button type="button" class:on={compute === o.c} class:g={isGPU(o.c)} aria-pressed={compute === o.c} disabled={!o.ok}
          title={o.ok ? `Build for ${computeName(o.c)}` : `${name} has no ${computeName(o.c)} build`}
          onclick={() => oncompute(o.c)}>{computeName(o.c)}</button>
      {/each}
    </span>
  </td>
  <td class="rc">
    <div class="route">
      {#if transfer && buildEngine !== it.engine}
        {engineName(buildEngine)}<Icon name="arrowRight" size={14} />{engineName(it.engine)}
      {:else}
        {routeText(buildEngine, it.engine)}
      {/if}
    </div>
    {#if routeNote.t}<span class="note {routeNote.c}">{routeNote.t}</span>{/if}
  </td>
  <td class="tc">
    <div class="time">{pf?.eta_seconds ? "~" + dur(pf.eta_seconds) : "—"}</div>
    {#if timeNote}<span class="note">{timeNote}</span>{/if}
  </td>
  <td class="ac">
    <span class="acts">
      <button type="button" class="ghost small icon" class:on={expanded} onclick={ontoggle} aria-expanded={expanded} aria-label="Settings for {name}" title="Image name, system prompt, context, memory…"><Icon name="sliders" size={15} /></button>
      <button type="button" class="ghost small icon" onclick={onremove} aria-label="Remove {name}" title="Remove from the plan"><Icon name="xmark" size={15} /></button>
    </span>
  </td>
</tr>
{#if callouts.length || notes.length}
  <tr class="sub-row">
    <td class="gc"></td>
    <td colspan="5">
      <div class="msgs">
        {#each callouts as x, i (i)}
          <div class="callout er">
            <Icon name="errc" size={16} />
            <span>
              <b>{x.title}</b>
              {#each x.body ?? [] as l, j (j)}<p>{l}</p>{/each}
              {#if x.cmd}<CodeLine cmd={x.cmd} />{/if}
              {#if x.actions?.length}
                <span class="fixes">{#each x.actions as a (a.label)}<button type="button" class="small" onclick={a.run}>{a.label}</button>{/each}</span>
              {/if}
            </span>
          </div>
        {/each}
        {#each notes as x, i (i)}
          <p class="nt {x.tone}">
            <Icon name={x.tone === "er" ? "errc" : x.tone === "wa" ? "warn" : "info"} size={14} />
            <span>{x.title}{#each x.actions ?? [] as a (a.label)}{" "}<button type="button" class="lk" onclick={a.run}>{a.label}</button>{/each}</span>
          </p>
        {/each}
      </div>
    </td>
  </tr>
{/if}
{#if expanded}
  <tr class="sub-row">
    <td class="gc"></td>
    <td colspan="5">
      <ItemSettings {it} {m} {si} {personas} {index} {count} {onmove} {oncompute} />
    </td>
  </tr>
{/if}

<style>
  td { padding: 12px 8px; border-top: 1px solid var(--ln); vertical-align: middle; }
  .row.open td, .sub-row td { border-bottom: 0; }
  .sub-row td { border-top: 0; padding-top: 0; }
  .gc { padding-left: 0; padding-right: 4px; width: 26px; }
  .ac { padding-right: 0; width: 64px; }
  .row.dragging { opacity: 0.45; }
  .row.above td { box-shadow: inset 0 2px 0 var(--pr); }
  .row.below td { box-shadow: inset 0 -2px 0 var(--pr); }
  .grip {
    width: 24px;
    min-height: 28px;
    padding: 0;
    border: 0;
    background: none;
    color: var(--fa);
    cursor: grab;
  }
  .grip:hover { background: var(--p2); color: var(--ink); }
  .nm b { display: block; font-weight: 600; }
  .l2 { display: flex; align-items: center; gap: 6px; margin-top: 2px; flex-wrap: wrap; }
  .l2 .mono { color: var(--mu); font-size: 11.5px; min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .l2 .chip { height: 20px; padding: 0 7px; font-size: 11px; }
  .segc { display: inline-flex; padding: 3px; border: 1px solid var(--ln2); border-radius: 9px; gap: 2px; }
  .segc button {
    min-height: 0;
    height: 26px;
    padding: 0 9px;
    border: 0;
    border-radius: 6px;
    background: none;
    color: var(--mu);
    font-size: 12.5px;
    font-weight: 500;
  }
  .segc button:hover:not(:disabled) { background: var(--p2); color: var(--ink); }
  .segc button.on { background: var(--p3); color: var(--ink); }
  .segc button.on.g { background: var(--gpus); color: var(--gpu); }
  .route { font-size: 13px; font-weight: 600; display: flex; align-items: center; gap: 6px; white-space: nowrap; }
  .route :global(.ico) { color: var(--fa); }
  .note { display: block; color: var(--mu); font-size: 12px; margin-top: 2px; white-space: nowrap; }
  .note.er { color: var(--er); }
  .note.wa { color: var(--wa); }
  .time { font: 12.5px var(--font-mono); white-space: nowrap; }
  .acts { display: flex; gap: 2px; justify-content: flex-end; }
  .acts .on { background: var(--p3); color: var(--ink); }
  .msgs { display: flex; flex-direction: column; gap: 8px; }
  .msgs .callout :global(.code) { margin-top: 8px; }
  .fixes { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 8px; }
  .nt { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: 8px; font-size: 12.5px; line-height: 1.45; color: var(--mu); padding: 0 2px; }
  .nt :global(.ico) { margin-top: 2px; color: var(--fa); }
  .nt.er :global(.ico) { color: var(--er); }
  .nt.wa :global(.ico) { color: var(--wa); }
  .nt.er span { color: var(--ink); }
  .lk {
    display: inline;
    min-height: 0;
    padding: 0;
    border: 0;
    background: none;
    color: var(--pr);
    font: inherit;
    font-weight: 500;
    white-space: normal;
    text-align: left;
  }
  .lk:hover { background: none; text-decoration: underline; text-underline-offset: 2px; }

  /* Phone: each build is a stacked card instead of a table row. */
  @media (max-width: 760px) {
    tr { display: grid; grid-template-columns: minmax(0, 1fr) auto; column-gap: 10px; }
    td { display: block; border-top: 0; padding: 4px 0; }
    .row { padding-top: 12px; border-top: 1px solid var(--ln); grid-template-areas: "nm ac" "cc cc" "rc tc"; row-gap: 8px; }
    .row .gc { display: none; }
    .row .nm { grid-area: nm; }
    .row .ac { grid-area: ac; width: auto; }
    .row .cc { grid-area: cc; }
    .row .rc { grid-area: rc; }
    .row .tc { grid-area: tc; text-align: right; }
    .sub-row { display: block; padding-bottom: 10px; }
    .sub-row .gc { display: none; }
    .route, .note { white-space: normal; }
  }
</style>
