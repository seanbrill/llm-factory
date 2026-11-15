<script lang="ts">
  // Where a build got to (sheet frame f10): weights, base image, compile,
  // package and — when it was built on the other engine — the load, read from
  // its log (lib/builds.ts buildSteps).
  import Icon from "../Icon.svelte";
  import type { Step } from "../../lib/builds";

  let { steps }: { steps: Step[] } = $props();

  const SAY: Record<string, string> = { ok: "done", run: "in progress", er: "failed", stop: "canceled", todo: "not reached" };
</script>

<ol class="steps" aria-label="Build steps">
  {#each steps as s (s.key)}
    <li class="stp {s.state}">
      <span class="b" aria-hidden="true">
        {#if s.state === "ok"}<Icon name="check" size={12} stroke={2.8} />
        {:else if s.state === "er"}<Icon name="xmark" size={12} stroke={2.8} />
        {:else if s.state === "stop"}<Icon name="minus" size={12} stroke={2.8} />
        {:else if s.state === "run"}<span class="spin"></span>{/if}
      </span>
      <b>{s.label}<span class="sr"> — {SAY[s.state]}</span></b>
      <span class="n">{s.note}</span>
    </li>
  {/each}
</ol>

<style>
  .steps {
    display: flex;
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .stp {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
    position: relative;
    font-size: 12px;
    color: var(--mu);
    padding-right: 8px;
  }
  .stp::before {
    content: "";
    position: absolute;
    top: 10px;
    left: 22px;
    right: 0;
    height: 2px;
    background: var(--ln2);
  }
  .stp:last-child::before { display: none; }
  .stp.ok::before { background: color-mix(in srgb, var(--ok) 45%, var(--ln2)); }
  .b {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--p3);
    color: var(--fa);
    position: relative;
    z-index: 1;
  }
  .b .spin { width: 14px; height: 14px; }
  .stp.ok .b { background: var(--oks); color: var(--ok); }
  .stp.er .b { background: var(--er); color: var(--cv); }
  .stp.stop .b { background: var(--was); color: var(--wa); }
  .stp.run .b { background: var(--prs); }
  .stp b { color: var(--ink); font-weight: 600; font-size: 13px; }
  .stp.todo b { color: var(--mu); }
  .n { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .stp.er .n { color: var(--er); }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip-path: inset(50%);
    white-space: nowrap;
  }
</style>
