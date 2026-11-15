<script lang="ts">
  // Memory plan (sheet frame f13): Docker's VM and the Podman machine both take
  // their memory from this Mac. Shows how it's split today and two other ways
  // to split it, with the host commands for each. The factory never changes
  // engine memory itself. The Mac's own memory isn't visible to the factory (it
  // runs inside Docker), so the user enters it once.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { catalog, sysinfo } from "../../lib/stores";
  import { DOCKER_MEMORY_PATH } from "../../lib/explain";
  import {
    engineOf,
    hostMem,
    hostMemCmd,
    isMac,
    memoryPlans,
    PODMAN_FIX_GB,
    planSteps,
    setHostMem,
    type MemPlan,
    type PlanId,
  } from "../../lib/system.svelte";

  const si = $derived($sysinfo);
  const mac = $derived(isMac(si));
  const here = $derived(mac ? "this Mac" : "this machine");
  const mp = $derived(memoryPlans(si, hostMem.gb, $catalog));
  const twoEngines = $derived(mp.podmanNow !== null);
  const podmanGPU = $derived(!!engineOf(si, "podman")?.gpu);

  let pick = $state<PlanId | "">("");
  const selId = $derived(pick || mp.recommended);
  const sel = $derived(mp.plans.find((p) => p.id === selId) ?? mp.plans[2]);
  const today = $derived(mp.plans[2]);
  const steps = $derived(planSteps(mp, sel, si));
  let showSteps = $state(false);

  // Host memory: asked for until it's known; "Change" opens it again.
  let editing = $state(false);
  let draft = $state<number | null>(hostMem.gb);
  let hostErr = $state("");
  function saveHost(ev: SubmitEvent) {
    ev.preventDefault();
    const v = Number(draft);
    if (!isFinite(v) || v < 4 || v > 4096) {
      hostErr = "Enter the memory in GB, like 24.";
      return;
    }
    if (mp.dockerNow != null && v < mp.dockerNow) {
      hostErr = `Docker alone has ${mp.dockerNow} GB, so ${here} has at least that. Check the number.`;
      return;
    }
    hostErr = "";
    setHostMem(Math.round(v));
    editing = false;
  }

  // The bar shows the picked plan when all its sizes are known, else today.
  const barPlan = $derived<MemPlan>(sel.docker != null && sel.podman != null ? sel : today);
  const seg = $derived.by(() => {
    const d = barPlan.docker ?? 0;
    const p = barPlan.podman ?? 0;
    const host = mp.host;
    const max = Math.max(host ?? 0, d + p) || 1;
    const rest = host ? Math.max(0, host - d - p) : 0;
    return { d, p, rest, max, over: host ? Math.max(0, d + p - host) : 0 };
  });
  const ticks = $derived.by(() => {
    if (!mp.host) return [];
    const n = seg.max;
    return [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(n * f * 10) / 10);
  });
  const barLabel = $derived(
    [
      seg.rest ? `${mac ? "macOS" : "System"} and apps ${seg.rest} GB` : "",
      barPlan.docker != null ? `Docker ${seg.d} GB` : "",
      seg.p ? `Podman ${seg.p} GB` : "",
      seg.over ? `${seg.over} GB more than ${here} has` : "",
    ]
      .filter(Boolean)
      .join(", "),
  );
  const pct = (n: number) => `${((n / seg.max) * 100).toFixed(2)}%`;
  // A segment's label shrinks to fit it: "Podman · 12 · GPU", "Podman · 12", "12".
  let barW = $state(0);
  function segText(n: number, ...options: string[]): string {
    const px = (n / seg.max) * (barW || 600);
    // ~6.2 px a character at 12px semibold, plus the 10px padding each side.
    return options.find((t) => t.length * 6.2 + 20 <= px) ?? String(n);
  }

  function sizes(p: MemPlan): string {
    const parts: string[] = [];
    if (p.docker != null) parts.push(`Docker ${p.docker}`);
    if (p.podman != null) parts.push(`Podman ${p.podman ? p.podman : "off"}`);
    return parts.join(" · ");
  }

  function choose(id: PlanId) {
    pick = id;
    showSteps = false;
  }
</script>

<div class="hd">
  <h2>Memory plan</h2>
  <span class="sub">
    {#if mp.host}{mp.host} GB to share between {mac ? "macOS" : "the system"} and {twoEngines ? "two engines" : "Docker"}{:else}{twoEngines ? "Both engines take" : "Docker takes"} their memory from {here}{/if}
  </span>
  {#if mp.host && !editing}
    <div class="sp"><button class="ghost small" onclick={() => ((draft = mp.host), (editing = true))}>Change</button></div>
  {/if}
</div>

{#if !mp.host || editing}
  <form class="host" onsubmit={saveHost}>
    <label for="hostmem">How much memory does {here} have?</label>
    <div class="hrow">
      <input id="hostmem" type="number" min="4" max="4096" step="1" inputmode="numeric" placeholder="24" bind:value={draft} aria-describedby="hostmem-how" />
      <span class="unit">GB</span>
      <button class="small primary" type="submit">Use it</button>
      {#if editing}<button class="small ghost" type="button" onclick={() => ((editing = false), (hostErr = ""))}>Cancel</button>{/if}
    </div>
    {#if hostErr}<p class="err" role="alert">{hostErr}</p>{/if}
    <p class="note" id="hostmem-how">The factory runs inside Docker and can't see it. This prints it:</p>
    <CodeLine cmd={hostMemCmd(si)} />
  </form>
{/if}

{#if barPlan.docker != null || barPlan.podman}
  <div class="bar" role="img" aria-label={barLabel} bind:clientWidth={barW}>
    {#if seg.rest}
      <span class="b-os" style="width:{pct(seg.rest)}" title="Left for {mac ? 'macOS' : 'the system'} and your apps: {seg.rest} GB">
        {segText(seg.rest, `${mac ? "macOS" : "System"} and apps · ${seg.rest}`, `${mac ? "macOS" : "System"} · ${seg.rest}`)}
      </span>
    {/if}
    {#if barPlan.docker != null}<span class="b-dk" style="width:{pct(seg.d)}" title="Docker's VM: {seg.d} GB">{segText(seg.d, `Docker · ${seg.d}`)}</span>{/if}
    {#if seg.p}
      <span class="b-pm" class:gpu={podmanGPU} style="width:{pct(seg.p)}" title="The Podman machine: {seg.p} GB">
        {segText(seg.p, ...(podmanGPU ? [`Podman · ${seg.p} · GPU`] : []), `Podman · ${seg.p}`)}
      </span>
    {/if}
    {#if seg.over && mp.host}<i class="limit" style="left:{pct(mp.host)}" title="{here}: {mp.host} GB"></i>{/if}
  </div>
  {#if ticks.length}
    <div class="scale" aria-hidden="true">
      {#each ticks as t, i (i)}<span>{t}{i === ticks.length - 1 ? " GB" : ""}</span>{/each}
    </div>
  {/if}
{/if}

{#if twoEngines}
  <div class="presets" role="radiogroup" aria-label="Ways to split the memory">
    {#each mp.plans as p (p.id)}
      {@const off = !p.ok && p.id !== "today"}
      <label class="pre" class:on={selId === p.id} class:off>
        <input class="vh" type="radio" name="memplan" value={p.id} checked={selId === p.id} disabled={off} onchange={() => choose(p.id)} />
        <b>{p.title}{#if p.id === mp.recommended && p.id !== "today"}<span class="chip pr">Suggested</span>{/if}</b>
        <span class="pt">{p.text}</span>
        {#if sizes(p)}<span class="mono">{sizes(p)}</span>{/if}
      </label>
    {/each}
  </div>
{/if}

{#if mp.host && !today.ok && mp.dockerNow != null && mp.podmanNow}
  <div class="warnline">
    <Icon name="warn" size={15} />
    <span>Today Docker {mp.dockerNow} + Podman {mp.podmanNow} GB ask for more than {here}'s {mp.host} GB. When both fill up, {mac ? "macOS" : "the system"} has to swap and everything slows down.</span>
  </div>
{:else if mp.naiveOver && sel.id === "gpu" && sel.ok && sel.docker != null}
  <div class="warnline">
    <Icon name="warn" size={15} />
    <span>Docker {mp.dockerNow} + Podman {PODMAN_FIX_GB} would ask for more than {here} has, so GPU first shrinks Docker to {sel.docker} GB.</span>
  </div>
{/if}

{#if twoEngines}
  <div class="acts">
    {#if steps.length}
      <button class="small primary" aria-expanded={showSteps} aria-controls="plan-steps" onclick={() => (showSteps = !showSteps)}>
        {showSteps ? "Hide the commands" : `Show commands for ${sel.title}`}
      </button>
    {:else}
      <span class="now">{sel.id === "today" || !sel.changes ? "This is how it's set now." : ""}</span>
    {/if}
    <span class="fine">The factory shows the commands; it never changes engine memory for you.</span>
  </div>
  {#if showSteps && steps.length}
    <ol class="steps" id="plan-steps">
      {#each steps as s, i (i)}
        <li>
          <p>{s.text}</p>
          {#if s.cmd}<CodeLine cmd={s.cmd} />{/if}
        </li>
      {/each}
    </ol>
  {/if}
{:else}
  <p class="fine">To change it: {DOCKER_MEMORY_PATH}, then Apply &amp; restart. The factory never changes engine memory for you.</p>
{/if}

<style>
  .hd .sub { min-width: 0; }
  .host { display: flex; flex-direction: column; gap: 6px; margin-bottom: 14px; }
  .host label { font-weight: 600; }
  .hrow { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .hrow input { width: 96px; min-height: 34px; padding: 0 10px; font-family: var(--font-mono); }
  .unit { color: var(--mu); font-size: 13px; }
  .note { font-size: 12px; color: var(--fa); margin-top: 2px; }
  .err { font-size: 12.5px; color: var(--er); }
  .bar {
    position: relative;
    display: flex;
    height: 34px;
    border-radius: 9px;
    overflow: hidden;
    gap: 2px;
    margin-top: 6px;
    background: var(--p2);
  }
  .bar span {
    display: flex;
    align-items: center;
    padding: 0 10px;
    font-size: 12px;
    font-weight: 600;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 6px;
  }
  .b-os { background: var(--p3); color: var(--mu); }
  .b-dk { background: color-mix(in srgb, var(--ink) 22%, var(--cv)); color: var(--ink); }
  .b-pm { background: color-mix(in srgb, var(--ink) 14%, var(--cv)); color: var(--ink); }
  .b-pm.gpu {
    background: var(--gpus);
    color: var(--gpu);
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--gpu) 45%, transparent);
  }
  .limit { position: absolute; top: 0; bottom: 0; width: 2px; background: var(--er); }
  .scale { display: flex; justify-content: space-between; font-family: var(--font-mono); font-size: 11px; color: var(--fa); margin-top: 5px; }
  .presets { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; margin-top: 14px; }
  .pre {
    position: relative;
    display: flex;
    flex-direction: column;
    border: 1px solid var(--ln2);
    border-radius: 11px;
    padding: 11px 12px;
    font-size: 12.5px;
    color: var(--mu);
    cursor: pointer;
    min-width: 0;
  }
  .pre:hover { border-color: color-mix(in srgb, var(--ink) 30%, var(--cv)); }
  .pre:has(input:focus-visible) { outline: 2px solid var(--pr); outline-offset: 2px; }
  .pre b { display: flex; align-items: center; gap: 6px; color: var(--ink); font-size: 13.5px; margin-bottom: 2px; }
  .pre b .chip { height: 19px; font-size: 10.5px; padding: 0 7px; }
  .pre .pt { flex: 1; }
  .pre .mono { display: block; color: var(--ink); margin-top: 6px; font-size: 12px; }
  .pre.on { border-color: var(--pr); background: color-mix(in srgb, var(--pr) 7%, var(--p1)); }
  .pre.off { cursor: not-allowed; opacity: 0.6; }
  .vh { position: absolute; width: 1px; height: 1px; opacity: 0; pointer-events: none; }
  .warnline { margin-top: 12px; display: grid; grid-template-columns: 15px minmax(0, 1fr); gap: 8px; font-size: 12.5px; color: var(--mu); }
  .warnline > :global(.ico) { color: var(--wa); margin-top: 2px; }
  .acts { display: flex; align-items: center; gap: 12px; margin-top: 12px; flex-wrap: wrap; }
  .now { font-size: 12.5px; color: var(--mu); }
  .now:empty { display: none; }
  .fine { font-size: 12px; color: var(--fa); }
  .steps { margin: 12px 0 0; padding-left: 20px; display: flex; flex-direction: column; gap: 10px; font-size: 12.5px; color: var(--mu); }
  .steps li::marker { color: var(--fa); font-family: var(--font-mono); font-size: 12px; }
  .steps p { margin-bottom: 6px; }
  @media (max-width: 760px) {
    .hd { flex-wrap: wrap; row-gap: 2px; }
    .hd .sub { order: 3; flex-basis: 100%; }
    .presets { grid-template-columns: 1fr; }
    .pre .mono { margin-top: 4px; }
  }
</style>
