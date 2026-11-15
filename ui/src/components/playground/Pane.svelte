<script lang="ts">
  // One side of the Playground (sheet frame f14): which model, where it runs,
  // what its last reply measured (time to the first token, tokens per second,
  // tokens) next to the factory's memory estimate, "Copy as curl", and the
  // conversation.
  import { MediaQuery } from "svelte/reactivity";
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import Messages from "./Messages.svelte";
  import { href } from "../../lib/router";
  import { resources } from "../../lib/stores";
  import { computeName, engineName, computeChip } from "../../lib/format";
  import { modMeta } from "../../lib/icons";
  import { isStreamMod } from "../../lib/util";
  import { num } from "../../lib/library";
  import {
    chat, setTarget, requestFor, curlFor, memEstimate, lastStats, bridgeStop,
    type Tab, type Pane, type Target,
  } from "../../lib/chat.svelte";

  let {
    tab,
    pane,
    side = "",
    targets,
    win = { ttft: false, tps: false },
    draft = "",
  }: {
    tab: Tab;
    pane: Pane;
    side?: "" | "left" | "right";
    targets: Target[];
    win?: { ttft: boolean; tps: boolean };
    draft?: string;
  } = $props();

  const target = $derived(targets.find((t) => t.port === pane.port));
  const stopped = $derived(!tab.bridge && !!pane.port && !target);
  const mod = $derived(target?.modality ?? pane.modality);
  const name = $derived(tab.bridge ? pane.name : target?.name || pane.name || (pane.port ? `the model on :${pane.port}` : "No model"));
  const mem = $derived(memEstimate(target, $resources?.running));
  const stats = $derived(lastStats(pane));
  const curl = $derived.by(() => {
    if (tab.bridge || stopped) return "";
    const r = requestFor(tab, pane, draft);
    return r ? curlFor(r) : "";
  });
  const streamy = $derived(isStreamMod(mod));
  const persona = $derived(tab.bridge ? "" : tab.persona || target?.persona || "");
  const uid = $derived("pane-" + pane.id);
  // A phone keeps the header to one line: "Copy as curl" becomes an icon.
  const phone = new MediaQuery("max-width: 760px");

  function pick(v: string) {
    const t = targets.find((x) => x.port === parseInt(v, 10));
    if (t) setTarget(pane, t);
  }

  const secs = (ms: number) => (ms < 10000 ? (ms / 1000).toFixed(1) : String(Math.round(ms / 1000))) + " s";
  const rate = (t: number) => (t < 10 ? t.toFixed(1) : String(Math.round(t))) + " tok/s";

  const HELLO: Record<string, string> = {
    text: "Send a message to check it answers, and how fast.",
    code: "Ask for some code to check it answers, and how fast.",
    reasoning: "Ask something that takes a few steps; its thinking shows above the answer.",
    vision: "Attach or paste an image, then ask about it.",
    image: "Describe a picture; it comes back here.",
    video: "Describe a short clip; it comes back here. Pick the length next to Send.",
    "audio-stt": "Attach an audio file; its transcript comes back here.",
    tts: "Type something for it to say aloud.",
    ensemble: "Chat with it, attach an image, ask it to draw a picture or make a short video, or press the mic and ask out loud: it answers in its persona's voice. A member that isn't running yet starts first, which can take a minute.",
  };
</script>

<section class="pane" aria-labelledby="{uid}-h">
  <header class="ph">
    <h2 class="vh" id="{uid}-h">{side ? `${side === "left" ? "Left" : "Right"}: ${name}` : name}</h2>
    {#if tab.bridge}
      <span class="pick static"><Icon name="chain" size={14} /><span class="nm">{pane.name}</span></span>
      <div class="sp">
        {#if chat.bridging && pane.busy}<button class="small" type="button" onclick={bridgeStop}><Icon name="stopsq" size={14} />Stop</button>{/if}
      </div>
    {:else}
      <label class="pick" title={target ? `${target.ref} on :${target.port}` : "Pick a running model"}>
        <span class="dot" class:off={!target}></span>
        <span class="vh">Model{side ? ` on the ${side}` : ""}</span>
        <span class="shown" aria-hidden="true">
          <span class="nm">{pane.port ? name : "Pick a model"}</span>
          {#if stopped}<small>stopped</small>{:else if pane.port}<small>:{pane.port}</small>{/if}
        </span>
        <select value={String(pane.port)} disabled={pane.busy} onchange={(e) => pick(e.currentTarget.value)}>
          {#if !pane.port}<option value="0" disabled>Pick a model</option>{/if}
          {#if stopped}<option value={String(pane.port)}>{name} · stopped</option>{/if}
          {#each targets as t (t.port)}<option value={String(t.port)}>{t.name} · :{t.port}</option>{/each}
        </select>
        <Icon name="chevronDown" size={14} cls="cv" />
      </label>
      {#if target}
        <span class={computeChip(target.compute)}>{computeName(target.compute)} · {engineName(target.engine)}</span>
        {#if target.persona}<span class="chip pchip" title="Runs with this persona's prompt built in">{target.persona}</span>{/if}
      {/if}
      <div class="sp">
        {#if curl}
          {#if phone.current}
            <CopyButton text={curl} title="Copy as curl" />
          {:else}
            <CopyButton text={curl} label="Copy as curl" title={pane.last ? "The request this side sent last, as a curl command" : "The request this side would send now, as a curl command"} />
          {/if}
        {/if}
        {#if target?.ensemble}
          <a class="btn sm icon" href={href("ensembles")} aria-label="Show {name} on the Ensembles page" title="Its members and their state"><Icon name="ensembles" size={14} /></a>
        {:else if pane.port}
          <a class="btn sm icon" href={href("library", target?.model || String(pane.port))} aria-label="Show {name} in the Library" title="Show it in the Library"><Icon name="library" size={14} /></a>
        {/if}
      </div>
    {/if}
  </header>

  {#if !tab.bridge}
    <div class="stats">
      {#if streamy || !pane.port}
        <div class="st" class:win={win.ttft} title="From sending to the first streamed token, measured here"><b>{stats?.ttft != null ? secs(stats.ttft) : "—"}</b><small>first word</small></div>
        <div class="st" class:win={win.tps} title="Tokens per second after the first, measured from the stream"><b>{stats?.tps != null ? rate(stats.tps) : "—"}</b><small>speed</small></div>
      {:else}
        <div class="st" class:win={win.ttft} title="From sending to the result, measured here"><b>{stats ? secs(stats.ms) : "—"}</b><small>took</small></div>
      {/if}
      <div class="st" title={mem ? "The factory's estimate from the model's size, not a live reading" : "No estimate for this model"}>
        <b>{mem ? `~${num(mem.gb)} GB` : "—"}</b><small>{mem?.gpu ? "GPU memory" : "memory"}{mem ? " (est.)" : ""}</small>
      </div>
      {#if streamy || !pane.port}
        <div class="st" title="Streamed chunks; llama.cpp sends one token per chunk"><b>{stats?.tokens ?? "—"}</b><small>tokens</small></div>
      {/if}
      <span class="kind"><Icon name={modMeta(mod).icon} size={13} />{modMeta(mod).label}</span>
    </div>
  {/if}

  {#if stopped}
    <div class="callout wa gone">
      <Icon name="warn" />
      <span>
        <b>{name} isn't running on :{pane.port} any more.</b>
        <p>Its chat is kept here. Run it again from the <a href={href("library", pane.model || String(pane.port))}>Library</a>, or pick a running model above.</p>
      </span>
    </div>
  {/if}

  <Messages {pane} {persona} label="Conversation with {name}">
    {#snippet empty()}
      <div class="hello">
        {#if tab.bridge}
          <p>Starting…</p>
        {:else if !pane.port}
          <span class="hi"><Icon name="playground" size={18} /></span>
          <b>Pick a model</b>
          <p>
            {#if targets.length}Choose one of the running models above.{:else}Nothing is running. <a href={href("library")}>Run a model in the Library</a>, then pick it here.{/if}
          </p>
        {:else if !stopped}
          <span class="hi"><Icon name={modMeta(mod).icon} size={18} /></span>
          <b>{name}</b>
          {#if mod === "embedding"}
            <p>An embedding model turns text into vectors for search, so there's nothing to chat with. Copy its request as curl to try it on the factory's machine.</p>
          {:else}
            <p>{HELLO[mod] ?? HELLO.text}</p>
          {/if}
        {/if}
      </div>
    {/snippet}
  </Messages>
</section>

<style>
  .pane {
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--rl);
    display: flex;
    flex-direction: column;
    min-height: 0;
    min-width: 0;
    overflow: hidden;
  }
  .vh { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; margin: 0; }
  /* One line: a long model name gives way (ellipsis) before anything wraps,
     so both sides' stats stay level. */
  .ph { display: flex; align-items: center; gap: 10px; padding: 12px 14px; border-bottom: 1px solid var(--ln); min-width: 0; }
  .ph > :global(.chip) { flex: none; }
  .pick {
    position: relative;
    display: inline-flex;
    align-items: center;
    gap: 8px;
    height: 34px;
    padding: 0 10px 0 12px;
    border: 1px solid var(--ln2);
    border-radius: 9px;
    min-width: 0;
    max-width: min(320px, 100%);
    background: var(--p1);
  }
  .pick:focus-within { border-color: var(--pr); box-shadow: 0 0 0 3px var(--prs); }
  /* The native select sits invisibly over its label, so it keeps the
     platform's picker and keyboard handling. */
  .shown { display: inline-flex; align-items: baseline; gap: 6px; min-width: 0; font-weight: 600; font-size: 13.5px; }
  .shown .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .shown small { font: 500 11.5px var(--font-mono); color: var(--fa); white-space: nowrap; }
  .pick select {
    position: absolute;
    inset: 0;
    width: 100%;
    height: 100%;
    opacity: 0;
    appearance: none;
    -webkit-appearance: none;
    border: 0;
    padding: 0;
    cursor: pointer;
  }
  .pick select:disabled { cursor: default; }
  .pick:has(select:disabled) { opacity: 0.75; }
  .pick :global(.cv) { color: var(--fa); pointer-events: none; flex: none; }
  .pick.static { font-weight: 600; font-size: 13.5px; border-style: dashed; }
  .pick.static .nm { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ph .sp { margin-left: auto; display: flex; gap: 6px; align-items: center; flex: none; }

  .stats { display: flex; align-items: center; gap: 18px; padding: 10px 14px; border-bottom: 1px solid var(--ln); background: var(--p2); flex-wrap: wrap; row-gap: 6px; }
  .st { display: flex; flex-direction: column; min-width: 0; }
  .st b { font: 600 15px var(--font-mono); white-space: nowrap; }
  .st small { font-size: 11.5px; color: var(--mu); white-space: nowrap; }
  .st.win b { color: var(--ok); }
  .kind { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--fa); white-space: nowrap; }

  .gone { margin: 12px 14px 0; }
  .hello {
    margin: auto;
    max-width: 340px;
    text-align: center;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 24px 8px;
    color: var(--mu);
    font-size: 13px;
  }
  .hello b { color: var(--ink); font-size: 14px; }
  .hi { width: 40px; height: 40px; border-radius: 10px; display: grid; place-items: center; background: var(--p3); color: var(--mu); margin-bottom: 4px; }
  @media (max-width: 760px) {
    .ph { gap: 8px; padding: 10px 12px; }
    .pchip, .shown small { display: none; }
    .stats { gap: 14px; }
    .kind { display: none; }
  }
</style>
