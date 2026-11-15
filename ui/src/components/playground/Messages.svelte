<script lang="ts">
  // One side's conversation: your messages on the right (typed or asked out
  // loud), the model's replies as markdown (with its thinking folded away),
  // generated images, video and speech (an ensemble's reply can hold words and
  // media together), a progress ring while media generates or an ensemble's
  // member warms up, and what went wrong.
  import type { Snippet } from "svelte";
  import Icon from "../Icon.svelte";
  import GenLoader from "../GenLoader.svelte";
  import { renderAssistant, escapeHtml } from "../../lib/markdown";
  import { copyText } from "../../lib/ui.svelte";
  import { toast } from "../../lib/stores";
  import { href } from "../../lib/router";
  import type { Pane } from "../../lib/chat.svelte";

  let { pane, persona = "", label, empty }: { pane: Pane; persona?: string; label: string; empty?: Snippet } = $props();

  let el: HTMLDivElement | undefined = $state();
  let stick = true;
  let count = 0;

  function onScroll() {
    if (el) stick = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }

  // Follow the reply as it streams, unless you've scrolled up to read.
  $effect(() => {
    const n = pane.msgs.length;
    const last = pane.msgs[n - 1];
    void last?.content;
    void last?.reasoning;
    void last?.pending;
    void last?.err;
    void last?.images;
    void last?.video;
    void last?.audio;
    void last?.note;
    if (n !== count) {
      if (n > count) stick = true;
      count = n;
    }
    if (el && stick) el.scrollTop = el.scrollHeight;
  });

  // Code blocks come from rendered markdown; one listener serves their copy buttons.
  $effect(() => {
    const node = el;
    if (!node) return;
    const onClick = async (e: MouseEvent) => {
      const b = (e.target as HTMLElement).closest<HTMLButtonElement>(".code-copy");
      if (!b) return;
      const code = document.getElementById(b.dataset.codeId ?? "")?.textContent ?? "";
      toast((await copyText(code)) ? "Copied the code." : "Couldn't copy here — select the code and copy it yourself.");
    };
    node.addEventListener("click", onClick);
    return () => node.removeEventListener("click", onClick);
  });
</script>

<div class="msgs" bind:this={el} onscroll={onScroll} role="log" aria-label={label}>
  {#if !pane.msgs.length}
    {@render empty?.()}
  {:else}
    {#each pane.msgs as m, i (i)}
      {@const live = pane.busy && i === pane.msgs.length - 1}
      {#if m.bridgeScene}
        <div class="scene"><Icon name="chain" size={13} /><span>{m.content}</span></div>
      {:else if m.role === "user" && !m.bridgeFrom}
        <div class="u">
          {#if m.image}<img class="att" src={m.image} alt="What you attached" />{/if}
          {#if m.kind === "audio"}
            {#if m.audio}<audio controls src={m.audio}></audio>{/if}
            <span class="fn"><Icon name="mic" size={13} />{m.content}</span>
          {:else if m.spoken}
            <div class="ut" class:heard={m.content === "Transcribing…"}>{m.content}</div>
            <span class="fn"><Icon name="mic" size={13} />Asked out loud</span>
          {:else if m.content}
            <div class="ut">{m.content}</div>
          {/if}
        </div>
      {:else}
        {@const media = !!(m.images?.length || m.video || m.audio)}
        <div class="a" class:bridge={!!m.bridgeFrom} class:b={m.bridgeSide === "b"}>
          {#if m.bridgeFrom || persona}<div class="who">{m.bridgeFrom || persona}</div>{/if}
          {#if m.pending}
            <GenLoader eta={m.pending.eta} label={m.pending.label} />
          {:else}
            <!-- A reply can hold words, generated media, or both (an ensemble's caption and its image). -->
            {#if m.reasoning}
              <details class="think" open={!m.content}>
                <summary><Icon name="idea" size={13} />Thinking{live && !m.content ? "…" : ""}</summary>
                <div class="tb">{m.reasoning}</div>
              </details>
            {/if}
            {#if m.content}
              <div class="md" class:old-err={m.error} class:live>{@html m.error ? escapeHtml(m.content) : renderAssistant(m.content)}</div>
            {:else if live && !media}
              <span class="typing" role="status" aria-label="Writing"><i></i><i></i><i></i></span>
            {/if}
            {#if m.images?.length}
              <div class="gens">{#each m.images as src, j (j)}<img class="gen" {src} alt={`Made from “${pane.msgs[i - 1]?.content || "your prompt"}”`} />{/each}</div>
            {/if}
            {#if m.video}
              <!-- svelte-ignore a11y_media_has_caption (generated clips have no sound) -->
              <video class="gen" controls loop playsinline src={m.video}></video>
            {/if}
            {#if m.audio}
              <audio controls autoplay={m.autoplay} src={m.audio}></audio>
            {/if}
            {#if (m.kind === "image" || m.kind === "video" || m.kind === "audio") && !media && !m.err}
              <p class="note">This {m.kind} wasn't kept after the page reloaded. Send the prompt again to make a new one.</p>
            {/if}
          {/if}
          {#if m.note}<p class="note">{m.note}</p>{/if}
          {#if m.err}
            <div class="callout er">
              <Icon name="errc" />
              <span><p>{m.err}</p>{#if pane.port}<a href={href("library", String(pane.port))}>Open it in the Library</a>{/if}</span>
            </div>
          {/if}
          {#if m.stopped}<p class="note">Stopped{m.content || m.reasoning ? "" : " before it answered"}.</p>{/if}
        </div>
      {/if}
    {/each}
  {/if}
</div>

<style>
  .msgs {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    overscroll-behavior: contain;
  }
  .u { align-self: flex-end; max-width: 80%; display: flex; flex-direction: column; align-items: flex-end; gap: 6px; }
  .ut {
    padding: 10px 13px;
    border-radius: 14px 14px 4px 14px;
    background: var(--p3);
    font-size: 13.5px;
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .ut.heard { color: var(--mu); font-style: italic; }
  .att { max-width: 220px; max-height: 220px; border-radius: 10px; border: 1px solid var(--ln); object-fit: cover; }
  .fn { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--mu); }
  .a { font-size: 13.5px; line-height: 1.6; color: color-mix(in srgb, var(--ink) 90%, var(--cv)); min-width: 0; display: flex; flex-direction: column; gap: 6px; }
  .who { font-size: 12px; font-weight: 600; color: var(--mu); }
  .a.bridge { padding: 10px 13px; border-radius: 12px; border: 1px solid var(--ln); background: var(--p2); max-width: 88%; }
  .a.bridge.b { align-self: flex-end; background: var(--p3); }
  .scene {
    align-self: center;
    display: inline-flex;
    align-items: center;
    gap: 7px;
    padding: 6px 14px;
    border: 1px dashed var(--ln2);
    border-radius: 99px;
    color: var(--mu);
    font-size: 12.5px;
  }
  .note { color: var(--fa); font-size: 12.5px; }
  .gens { display: flex; flex-wrap: wrap; gap: 8px; }
  .gen { max-width: 100%; max-height: 480px; border-radius: 10px; border: 1px solid var(--ln); background: var(--p2); }
  audio { max-width: 100%; }
  .callout { margin-top: 2px; }
  .callout a { font-size: 12.5px; }
  .think { border: 1px dashed var(--ln2); border-radius: var(--r); background: var(--p1); }
  .think summary { cursor: pointer; display: flex; align-items: center; gap: 6px; padding: 6px 10px; font-size: 12px; color: var(--mu); }
  .think .tb { padding: 0 12px 10px; font-size: 12.5px; color: var(--mu); white-space: pre-wrap; overflow-wrap: anywhere; }
  .typing { display: inline-flex; gap: 4px; padding: 6px 0; }
  .typing i { width: 6px; height: 6px; border-radius: 50%; background: var(--fa); animation: blink 1.2s infinite ease-in-out; }
  .typing i:nth-child(2) { animation-delay: 0.15s; }
  .typing i:nth-child(3) { animation-delay: 0.3s; }
  @keyframes blink { 0%, 80%, 100% { opacity: 0.25; } 40% { opacity: 1; } }

  /* Rendered markdown (lib/markdown.ts). */
  .md { overflow-wrap: anywhere; }
  .md.old-err { color: var(--er); }
  /* A caret at the end of a reply that's still streaming. */
  .md.live > :global(p:last-child)::after {
    content: "";
    display: inline-block;
    width: 7px;
    height: 1.1em;
    margin-left: 2px;
    vertical-align: text-bottom;
    border-radius: 1px;
    background: var(--pr);
    animation: caret 1s steps(1) infinite;
  }
  @keyframes caret { 50% { opacity: 0; } }
  @media (prefers-reduced-motion: reduce) {
    .md.live > :global(p:last-child)::after { animation: none; }
    .typing i { animation: none; opacity: 0.6; }
  }
  .md :global(p + p), .md :global(p + ul), .md :global(p + ol), .md :global(ul + p), .md :global(ol + p) { margin-top: 8px; }
  .md :global(ul), .md :global(ol) { margin: 6px 0; padding-left: 20px; }
  .md :global(li + li) { margin-top: 2px; }
  .md :global(.md-h) { font-family: var(--font-display); font-size: 15px; margin: 12px 0 4px; }
  .md :global(code) { font-family: var(--font-mono); font-size: 12px; background: var(--p3); padding: 1px 5px; border-radius: 5px; }
  .md :global(a) { color: var(--pr); }
  .md :global(.code-block) { margin: 8px 0; border: 1px solid var(--ln); border-radius: var(--r); overflow: hidden; background: color-mix(in srgb, var(--cv) 70%, black); }
  .md :global(.code-head) { display: flex; justify-content: space-between; align-items: center; padding: 4px 6px 4px 12px; background: var(--p2); font-size: 11.5px; color: var(--mu); }
  .md :global(.code-copy) { min-height: 0; height: 24px; padding: 0 8px; font-size: 11.5px; gap: 5px; background: none; border-color: transparent; color: var(--mu); }
  .md :global(.code-copy:hover) { background: var(--p3); color: var(--ink); }
  .md :global(.code-block pre) { margin: 0; padding: 11px 12px; overflow: auto; font-size: 12px; line-height: 1.55; }
  .md :global(.code-block code) { background: none; padding: 0; }
  .md :global(.think) { margin-bottom: 8px; border: 1px dashed var(--ln2); border-radius: var(--r); background: var(--p1); }
  .md :global(.think > summary) { cursor: pointer; padding: 6px 10px; font-size: 12px; color: var(--mu); }
  .md :global(.think-body) { padding: 0 12px 10px; font-size: 12.5px; color: var(--mu); white-space: pre-wrap; }
  .md :global(.typing i) { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--fa); margin: 0 2px; }
</style>
