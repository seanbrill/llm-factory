<script lang="ts">
  // "How it will run" (sheet frame f9): the plan laid out in the server's build
  // queue. One lane per engine the images are for; each lane builds one image at
  // a time, after whatever is already waiting in it, and the lanes run side by
  // side. Widths are the server's estimate from recent builds on each compute; when one is
  // unknown the bars only show the order and no finish time is claimed.
  import { dur, engineName, plural } from "../../lib/format";
  import type { Schedule } from "../../lib/catalog";

  let { sched, unknown }: { sched: Schedule; unknown: string[] } = $props();

  const total = $derived(sched.total || 1);
  const ahead = $derived(sched.lanes.reduce((n, l) => n + l.ahead, 0));

  // 3–5 round ticks across the whole schedule.
  const ticks = $derived.by(() => {
    if (!sched.known || !sched.total) return [] as number[];
    const steps = [60, 120, 300, 600, 900, 1200, 1800, 3600, 7200, 10800, 21600, 43200];
    const step = steps.find((s) => sched.total / s <= 4) ?? 86400;
    const out: number[] = [];
    for (let t = 0; t <= sched.total + 1; t += step) out.push(t);
    return out;
  });

  const sentence = $derived.by(() => {
    const parts: string[] = [];
    parts.push(
      sched.lanes.length > 1
        ? "Each engine has its own lane and builds one image at a time; the lanes run side by side."
        : `${engineName(sched.lanes[0]?.engine)} builds them one at a time, in this order.`,
    );
    if (ahead) parts.push(`${plural(ahead, "build")} already in the queue ${ahead === 1 ? "goes" : "go"} first.`);
    if (unknown.length) parts.push(`No ${unknown.join(" or ")} build has finished here yet, so the bars only show the order.`);
    return parts.join(" ");
  });

  const pct = (s: number) => `${(s / total) * 100}%`;
</script>

<section class="how" aria-labelledby="how-h">
  <h3 id="how-h">How it will run{#if sched.known && sched.total}<span class="fa">{" · done in about " + dur(sched.total)}</span>{/if}</h3>
  <p class="sub">{sentence}</p>

  <div class="lanes">
    {#each sched.lanes as lane (lane.engine)}
      <div class="lane">
        <span class="ln">{engineName(lane.engine)}</span>
        <ol class="track" aria-label="{engineName(lane.engine)}: {plural(lane.blocks.length, 'build')} in order">
          {#each lane.blocks as b (b.key)}
            <li
              class="blk {b.kind}"
              style:left={pct(b.start)}
              style:width="calc({pct(b.dur)} - 3px)"
              title="{b.label}{b.note ? ' · ' + b.note : ''}{b.known ? ' · about ' + dur(b.dur) : ''}"
            >
              <span>{b.label}{#if b.note}<small>{" · " + b.note}</small>{/if}</span>
            </li>
          {/each}
        </ol>
      </div>
    {/each}
    {#if ticks.length}
      <div class="lane axis" aria-hidden="true">
        <span class="ln"></span>
        <span class="ticks">
          {#each ticks as t, i (t)}
            <span class:first={i === 0} class:last={t / total > 0.92} style:left={pct(t)}>{t ? dur(t) : "0"}</span>
          {/each}
        </span>
      </div>
    {/if}
  </div>
  <p class="legend" aria-hidden="true">
    {#if ahead}<span><i class="sw queued"></i>Already in the queue</span>{/if}
    {#if sched.lanes.some((l) => l.blocks.some((b) => b.kind === "cpu"))}<span><i class="sw cpu"></i>CPU build</span>{/if}
    {#if sched.lanes.some((l) => l.blocks.some((b) => b.kind === "gpu"))}<span><i class="sw gpu"></i>GPU build</span>{/if}
  </p>
</section>

<style>
  .how { display: flex; flex-direction: column; gap: 4px; }
  h3 { font-size: 13.5px; font-weight: 600; font-family: var(--font-body); margin: 0; }
  h3 .fa { color: var(--ink); }
  .sub { font-size: 12.5px; }
  .lanes { display: flex; flex-direction: column; gap: 8px; margin-top: 12px; }
  .lane { display: grid; grid-template-columns: 96px minmax(0, 1fr); align-items: center; gap: 12px; }
  .ln { font-size: 12.5px; color: var(--mu); }
  .track {
    position: relative;
    height: 32px;
    margin: 0;
    padding: 0;
    list-style: none;
    border-radius: 8px;
    background: var(--p2);
  }
  .blk {
    position: absolute;
    top: 3px;
    bottom: 3px;
    min-width: 6px;
    display: flex;
    align-items: center;
    padding: 0 9px;
    border-radius: 6px;
    font-size: 12px;
    font-weight: 600;
    overflow: hidden;
  }
  .blk span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .blk small { font-size: 12px; font-weight: 500; opacity: 0.8; }
  .blk.cpu { background: var(--prs); color: var(--pr); }
  .blk.gpu { background: var(--gpus); color: var(--gpu); }
  .blk.queued {
    color: var(--mu);
    background: repeating-linear-gradient(135deg, var(--p3) 0 6px, var(--p2) 6px 12px);
    font-weight: 500;
  }
  .axis .ticks { position: relative; height: 16px; }
  .ticks span {
    position: absolute;
    top: 0;
    transform: translateX(-50%);
    font: 11px var(--font-mono);
    color: var(--fa);
    white-space: nowrap;
  }
  .ticks span.first { transform: none; }
  .ticks span.last { transform: translateX(-100%); }
  .legend { display: flex; flex-wrap: wrap; gap: 14px; margin-top: 8px; font-size: 12px; color: var(--mu); }
  .legend span { display: inline-flex; align-items: center; gap: 6px; }
  .sw { width: 12px; height: 12px; border-radius: 3px; display: inline-block; }
  .sw.cpu { background: var(--prs); box-shadow: inset 0 0 0 1px var(--pr); }
  .sw.gpu { background: var(--gpus); box-shadow: inset 0 0 0 1px var(--gpu); }
  .sw.queued { background: repeating-linear-gradient(135deg, var(--p3) 0 3px, var(--p2) 3px 6px); box-shadow: inset 0 0 0 1px var(--ln2); }
  @media (max-width: 760px) {
    .lane { grid-template-columns: 64px minmax(0, 1fr); gap: 8px; }
  }
</style>
