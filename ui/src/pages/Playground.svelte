<script lang="ts">
  // Playground (sheet frame f14). Not a second Open WebUI: it's for checking a
  // model right after a build. Talk to one model, or turn on compare and send
  // one prompt to two side by side: how soon each starts, how fast it streams
  // (measured here) and its memory estimate, plus the exact request as curl.
  // Every kind of model works: chat, vision, image and video generation, speech
  // in both directions, and ensembles (chat with media in the replies, and the
  // mic for a spoken question answered in the persona's voice).
  // "#playground/<port>" opens the model on that port.
  import { untrack } from "svelte";
  import { MediaQuery } from "svelte/reactivity";
  import Icon from "../components/Icon.svelte";
  import Pane from "../components/playground/Pane.svelte";
  import Composer from "../components/playground/Composer.svelte";
  import Popover from "../components/playground/Popover.svelte";
  import StylePanel from "../components/playground/StylePanel.svelte";
  import PersonaPanel from "../components/playground/PersonaPanel.svelte";
  import OutputPanel from "../components/playground/OutputPanel.svelte";
  import BridgeDialog from "../components/playground/BridgeDialog.svelte";
  import { live, watch } from "../lib/live.svelte";
  import { catalog, ensembles, personas, toast } from "../lib/stores";
  import { routeArg, href } from "../lib/router";
  import { modMeta } from "../lib/icons";
  import { isStreamMod } from "../lib/util";
  import {
    chat, load, newTab, closeTab, activeTab, visiblePanes, targetsFrom, openTarget, setCompare, swapSides,
    clearTab, exportTab, lastStats, winners, setTarget,
    type Tab, type Pane as PaneT,
  } from "../lib/chat.svelte";

  $effect(() => watch("containers", "resources", "harness"));
  load();

  const targets = $derived(targetsFrom(live.containers, $catalog, { ensembles: $ensembles, personas: $personas }));
  const tab = $derived(activeTab());
  const panes = $derived(tab ? visiblePanes(tab) : []);
  const modOf = (p: PaneT) => targets.find((t) => t.port === p.port)?.modality ?? p.modality;
  const mods = $derived(panes.map(modOf));
  const chatty = $derived(mods.some((m) => isStreamMod(m)));
  const media = $derived([...new Set(mods.filter((m): m is "image" | "video" | "tts" => m === "image" || m === "video" || m === "tts"))]);
  const owui = $derived(live.harness?.openwebui?.url ?? "");
  let draft = $state("");
  let bridgeOpen = $state(false);
  let moreOpen = $state(false);
  // On a phone the settings buttons shrink to icons so the row fits.
  const phone = new MediaQuery("max-width: 760px");

  // A tab to start with, and a model for a side that has none yet.
  $effect(() => {
    if (!live.loaded.containers) return;
    const ts = targets;
    untrack(() => {
      if (!chat.tabs.length) {
        if (ts.length) newTab(ts[0]);
        return;
      }
      if (!activeTab()) chat.active = chat.tabs[0].id;
      const t = activeTab();
      if (!t || t.bridge || !ts.length) return;
      const [l, r] = t.panes;
      if (!l.port && !l.msgs.length) setTarget(l, ts.find((x) => x.port !== r?.port) ?? ts[0]);
      if (t.compare && r && !r.port && !r.msgs.length) {
        const other = ts.find((x) => x.port !== l.port);
        if (other) setTarget(r, other);
      }
    });
  });

  // "#playground/<port>" (also a model id or container name).
  let handled = "";
  let missing = $state("");
  $effect(() => {
    const arg = $routeArg;
    if (!arg) {
      handled = "";
      missing = "";
      return;
    }
    if (!live.loaded.containers || arg === handled) return;
    const ts = targets;
    untrack(() => {
      handled = arg;
      const t = ts.find((x) => String(x.port) === arg || x.model === arg || x.container === arg);
      missing = t ? "" : arg;
      if (t) openTarget(t);
    });
  });

  // Green for the side that started sooner and the one that streamed faster.
  const NONE = { ttft: false, tps: false };
  const win = $derived.by(() => {
    if (!tab?.compare || panes.length < 2) return [NONE, NONE];
    const [ma, mb] = mods;
    if (!(isStreamMod(ma) && isStreamMod(mb)) && ma !== mb) return [NONE, NONE];
    const w = winners(lastStats(panes[0]), lastStats(panes[1]));
    return [
      { ttft: w.ttft === 0, tps: w.tps === 0 },
      { ttft: w.ttft === 1, tps: w.tps === 1 },
    ];
  });

  function nameOf(p: PaneT): string {
    return targets.find((x) => x.port === p.port)?.name || p.name || (p.port ? `:${p.port}` : "New chat");
  }
  function tabTitle(t: Tab): string {
    if (t.bridge) return t.panes[0]?.name || "Two models talking";
    if (t.compare && t.panes.length > 1) return `${nameOf(t.panes[0])} vs ${nameOf(t.panes[1])}`;
    return nameOf(t.panes[0]);
  }
  const tabIcon = (t: Tab) => (t.bridge ? "chain" : t.compare ? "swap" : modMeta(modOf(t.panes[0])).icon);

  const personaLabel = $derived(!tab ? "" : tab.persona ? `Persona: ${tab.persona}` : tab.system.trim() ? "Persona: your own" : "Persona: none");
  const tempLabel = $derived(!tab ? "" : `Temperature ${String(+tab.temp.toFixed(2))}`);
  const mediaLabel = (m: string) => (m === "image" ? "Image settings" : m === "video" ? "Video settings" : "Voice");
  const modelFor = (m: string) => panes.find((p) => modOf(p) === m)?.model ?? "";

  function exportAs(fmt: "md" | "json") {
    moreOpen = false;
    if (tab && !exportTab(tab, fmt)) toast("Nothing to export yet: send a message first.");
  }

  function onKey(e: KeyboardEvent) {
    if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || e.key.toLowerCase() !== "d") return;
    if (!tab?.compare || document.querySelector("[aria-modal='true']")) return;
    e.preventDefault();
    swapSides(tab);
  }
</script>

<svelte:window onkeydown={onKey} />

{#if !tab}
  {#if live.errors.containers && !live.containers.length}
    <div class="callout er">
      <Icon name="errc" />
      <span><b>Couldn't list the running models.</b><p>{live.errors.containers}. Check that Docker or Podman is running on <a href={href("system")}>System</a>; this page tries again on its own.</p></span>
    </div>
  {:else if !live.loaded.containers}
    <p class="empty"><span class="spin"></span> Looking for running models…</p>
  {:else}
    <section class="card none">
      <span class="hi"><Icon name="playground" size={22} /></span>
      <h2>No model is running</h2>
      <p class="sub">The Playground talks to the models running on this machine. Run one from the Library, then come back; a model you just built is there too.</p>
      <div class="acts">
        <a class="btn pri" href={href("library")}><Icon name="library" size={15} />Open the Library</a>
        <a class="btn" href={href("catalog")}><Icon name="catalog" size={15} />Find a model to build</a>
      </div>
    </section>
  {/if}
{:else}
  <div class="pg" class:cmp={tab.compare && panes.length > 1}>
    <div class="ptools">
      <nav class="tabs" aria-label="Chats">
        {#each chat.tabs as t (t.id)}
          {@const title = tabTitle(t)}
          <div class="tab" class:on={t.id === chat.active}>
            <button class="tb" type="button" aria-current={t.id === chat.active ? "page" : undefined} {title} onclick={() => (chat.active = t.id)}>
              <Icon name={tabIcon(t)} size={14} /><span>{title}</span>
              {#if t.panes.some((p) => p.busy)}<span class="spin" aria-label="Answering"></span>{/if}
            </button>
            <button class="x" type="button" aria-label="Close {title}" title="Close" onclick={() => closeTab(t.id)}><Icon name="xmark" size={12} /></button>
          </div>
        {/each}
        <button class="small icon nt" type="button" aria-label="New chat" title="New chat" onclick={() => newTab(targets[0])}><Icon name="plus" size={15} /></button>
      </nav>

      <div class="ctl">
        {#if !tab.bridge}
          <label class="cmpt" title="Send each prompt to two models side by side">
            <input type="checkbox" class="tg" checked={tab.compare} onchange={(e) => setCompare(tab, e.currentTarget.checked, targets)} />
            <span>Compare two models</span>
          </label>
          {#if chatty}
            <Popover label={personaLabel} icon={phone.current ? "persona" : ""} iconOnly={phone.current} title="Persona and system prompt" wide><PersonaPanel {tab} /></Popover>
            <Popover label={tempLabel} icon={phone.current ? "sliders" : ""} iconOnly={phone.current} title="Temperature, top-p and seed"><StylePanel {tab} /></Popover>
          {/if}
          {#each media as m (m)}
            <Popover label={mediaLabel(m)} icon={phone.current ? modMeta(m).icon : ""} iconOnly={phone.current} title={mediaLabel(m)}><OutputPanel {tab} mod={m} model={modelFor(m)} /></Popover>
          {/each}
        {/if}
        <Popover label="More" icon="dots" iconOnly bind:open={moreOpen}>
          <div class="menu">
            {#if tab.compare && !tab.bridge}
              <button type="button" onclick={() => ((moreOpen = false), swapSides(tab))}><Icon name="swap" size={15} />Swap sides<span class="kbd">⌘D</span></button>
            {/if}
            <button type="button" onclick={() => ((moreOpen = false), (bridgeOpen = true))}><Icon name="chain" size={15} />Let two models talk…</button>
            <button type="button" onclick={() => exportAs("md")}><Icon name="download" size={15} />Export as Markdown</button>
            <button type="button" onclick={() => exportAs("json")}><Icon name="download" size={15} />Export as JSON</button>
            <button type="button" class="dg" onclick={() => ((moreOpen = false), clearTab(tab))}><Icon name="trash" size={15} />Clear this chat</button>
          </div>
        </Popover>
      </div>
    </div>

    {#if missing}
      <div class="callout">
        <Icon name="info" />
        <span><b>Nothing is running on :{missing}.</b><p>It may have stopped. <a href={href("library")}>Run it from the Library</a>, or pick a running model below.</p></span>
      </div>
    {/if}
    {#if live.errors.containers}
      <div class="callout wa">
        <Icon name="warn" />
        <span><b>Couldn't refresh the running models.</b><p>{live.errors.containers}. Open chats keep working while their models are up; this page tries again on its own.</p></span>
      </div>
    {/if}

    <section class="panes" aria-label={tab.compare ? "Two models side by side" : "Conversation"}>
      {#each panes as p, i (p.id)}
        <Pane {tab} pane={p} side={panes.length > 1 ? (i === 0 ? "left" : "right") : ""} {targets} win={win[i]} {draft} />
      {/each}
    </section>

    {#if !tab.bridge}
      <div class="cw">
        <Composer {tab} {targets} bind:draft />
        <p class="hints">
          <span><span class="kbd">Enter</span> send</span>
          <span><span class="kbd">Shift</span> <span class="kbd">Enter</span> new line</span>
          {#if tab.compare}<span><span class="kbd">⌘</span> <span class="kbd">D</span> swap sides</span>{/if}
          <span class="ow">
            Full chats and tools live in
            {#if owui}<a href={owui} target="_blank" rel="noopener">Open WebUI</a>{:else}<a href={href("harness")}>Open WebUI</a>{/if}.
          </span>
        </p>
      </div>
    {/if}
  </div>
{/if}

<BridgeDialog bind:open={bridgeOpen} {targets} />

<style>
  .pg {
    display: flex;
    flex-direction: column;
    gap: 12px;
    height: calc(100dvh - 62px - 64px);
    min-height: 520px;
  }
  .ptools { display: flex; align-items: center; gap: 12px; min-width: 0; flex: none; }
  .tabs { display: flex; align-items: center; gap: 4px; min-width: 0; flex: 1; overflow-x: auto; scrollbar-width: none; padding: 2px; }
  .tabs::-webkit-scrollbar { display: none; }
  .tab {
    display: flex;
    align-items: center;
    flex: none;
    max-width: 280px;
    border: 1px solid transparent;
    border-radius: var(--r);
    color: var(--mu);
  }
  .tab:hover { background: var(--p2); }
  .tab.on { background: var(--p3); border-color: var(--ln2); color: var(--ink); }
  .tb {
    all: unset;
    display: flex;
    align-items: center;
    gap: 7px;
    min-width: 0;
    height: 32px;
    padding: 0 4px 0 10px;
    font-size: 13px;
    font-weight: 500;
    cursor: pointer;
    border-radius: var(--r);
  }
  .tb span:not(.spin) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tb:focus-visible, .x:focus-visible { outline: 2px solid var(--pr); outline-offset: 1px; }
  .x {
    all: unset;
    display: grid;
    place-items: center;
    width: 24px;
    height: 24px;
    margin-right: 4px;
    border-radius: 6px;
    color: var(--fa);
    cursor: pointer;
    flex: none;
  }
  .x:hover { background: var(--p3); color: var(--ink); }
  .nt { flex: none; }
  .ctl { display: flex; align-items: center; gap: 8px; flex: none; }
  .cmpt { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; color: var(--mu); cursor: pointer; white-space: nowrap; margin-right: 4px; }
  .menu { display: flex; flex-direction: column; gap: 2px; margin: -6px; }
  .menu button { justify-content: flex-start; gap: 10px; border: 0; background: none; height: 36px; padding: 0 10px; font-weight: 500; }
  .menu button:hover { background: var(--p3); }
  .menu button .kbd { margin-left: auto; }
  .menu .dg { color: var(--er); }

  .panes { flex: 1; min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .pg.cmp .panes { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  /* One model: keep the conversation to a readable width. */
  .pg:not(.cmp) .panes :global(.msgs) { padding-inline: max(16px, calc((100% - 880px) / 2)); }
  .cw { display: flex; flex-direction: column; gap: 8px; }
  .hints { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 14px; padding: 0 6px; font-size: 12px; color: var(--fa); }
  .hints .kbd { font-size: 10.5px; }
  .ow { margin-left: auto; }

  .none { max-width: 640px; margin: 40px auto; display: flex; flex-direction: column; align-items: flex-start; gap: 10px; padding: 28px; }
  .none h2 { margin: 0; }
  .hi { width: 44px; height: 44px; border-radius: 12px; display: grid; place-items: center; background: var(--prs); color: var(--pr); }
  .acts { display: flex; gap: 8px; flex-wrap: wrap; margin-top: 6px; }
  .empty { display: flex; align-items: center; gap: 8px; }

  @media (max-width: 900px) {
    .ptools { flex-wrap: wrap; }
    .tabs { flex-basis: 100%; }
    .ctl { margin-left: auto; }
  }
  @media (max-width: 1100px) {
    .pg { height: calc(100dvh - 62px - 56px); }
  }
  @media (max-width: 900px) {
    .pg.cmp .panes { grid-template-columns: minmax(0, 1fr); grid-auto-rows: minmax(0, 1fr); }
  }
  @media (max-width: 760px) {
    .pg { height: auto; min-height: 0; padding-top: 4px; }
    .ctl { margin-left: 0; flex: 1 1 100%; min-width: 0; }
    .cmpt { margin-right: auto; }
    .panes { display: flex; flex-direction: column; }
    .panes > :global(.pane) { height: 56dvh; min-height: 340px; }
    .pg.cmp .panes > :global(.pane) { height: 44dvh; min-height: 300px; }
    .cw {
      position: sticky;
      bottom: calc(64px + env(safe-area-inset-bottom, 0px));
      z-index: 5;
      padding: 8px 0 4px;
      background: var(--cv);
    }
    /* Fill the gap down to the tab bar so replies don't show through. */
    .cw::after { content: ""; position: absolute; left: 0; right: 0; top: 100%; height: 24px; background: var(--cv); }
    .hints { display: none; }
  }
</style>
