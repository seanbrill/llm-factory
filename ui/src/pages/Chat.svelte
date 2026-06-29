<script lang="ts">
  import { onMount } from "svelte";
  import Icon from "../components/Icon.svelte";
  import Modal from "../components/Modal.svelte";
  import ModalityBadge from "../components/ModalityBadge.svelte";
  import GenLoader from "../components/GenLoader.svelte";
  import { renderAssistant, escapeHtml } from "../lib/markdown";
  import { modMeta } from "../lib/icons";
  import { isStreamMod } from "../lib/util";
  import { catalog, toast } from "../lib/stores";
  import * as C from "../lib/chat.svelte";
  import { chat, type Msg, type Tab } from "../lib/chat.svelte";

  let prompt = $state("");
  let pendingImage = $state<string | null>(null);
  let pendingFile = $state<File | null>(null);
  let fileEl = $state<HTMLInputElement>();
  let logEl = $state<HTMLDivElement>();
  let sideCollapsed = $state(false);

  let bridgeOpen = $state(false);
  let bA = $state(0), bB = $state(0), bSeed = $state(""), bRounds = $state(4), bThink = $state(false);

  const tab = $derived(C.activeTab());
  const showSettings = $derived(!!tab && !tab.bridge && isStreamMod(tab.modality));
  // The tab's container is gone (killed) but its chat history + media persist.
  const tabStopped = $derived(!!tab && !tab.bridge && !!tab.port && !chat.running.some((r) => r.port === tab!.port));
  const tempMood = (t: number) => (t <= 0.01 ? "deterministic" : t < 0.3 ? "precise" : t < 0.6 ? "balanced" : t < 0.9 ? "creative" : t < 1.2 ? "wild" : "chaotic");

  $effect(() => { C.setCatalog($catalog); });
  // One-time init: load persisted tabs + fetch running containers. Must be
  // onMount, NOT $effect — refreshTargets() both reads and writes chat.running /
  // chat.tabs, which inside a tracked effect self-invalidates into an infinite
  // loop (effect_update_depth_exceeded) once a persisted tab exists.
  onMount(() => { C.load(); C.refreshTargets(); });
  // autoscroll the log when messages change
  $effect(() => {
    void tab?.msgs.length;
    void tab?.msgs[tab.msgs.length - 1]?.content;
    if (logEl) logEl.scrollTop = logEl.scrollHeight;
  });

  interface Bubble { scene?: boolean; cls: string; who: string; showWho: boolean; html: string; }
  function bubble(m: Msg, t: Tab | null): Bubble {
    if (m.bridgeScene) return { scene: true, cls: "", who: "", showWho: false, html: escapeHtml(m.content || "") };
    const isBridge = !!m.bridgeFrom;
    const role = isBridge ? (m.bridgeSide === "a" ? "user" : "assistant") : m.role;
    const persona = !isBridge && m.role === "assistant" && t && C.isPersonaName(t.name) ? t.name : "";
    const who = m.bridgeFrom || persona || (m.role === "user" ? "You" : "Model");
    let html = "";
    if (m.kind === "image" && m.images?.length) html = m.images.map((s) => `<img class="chat-img" src="${s}" alt="" />`).join("");
    else if (m.kind === "video" && m.video) html = `<video class="chat-video" controls loop playsinline src="${m.video}"></video>`;
    else if (m.kind === "audio" && m.audio) html = `<audio controls${m.autoplay ? " autoplay" : ""} src="${m.audio}"></audio>` + (m.content ? `<div class="audio-cap">${escapeHtml(m.content)}</div>` : "");
    else if ((m.kind === "image" || m.kind === "video" || m.kind === "audio") && !m.content) html = `<span class="media-lost">${m.kind} not retained after reload — regenerate to view</span>`;
    else if (m.role === "assistant") html = renderAssistant(m.content || "");
    else {
      if (m.image) html += `<img class="chat-img sm" src="${m.image}" alt="" />`;
      html += `<div class="user-text">${escapeHtml(m.content || "").replace(/\n/g, "<br>")}</div>`;
    }
    const named = isBridge || persona ? " msg-named" : "";
    return { cls: `msg msg-${role}${m.error ? " msg-error" : ""}${isBridge ? " msg-bridge" : ""}${named}`, who, showWho: isBridge || !!persona, html };
  }

  function doSend(e?: Event) {
    e?.preventDefault();
    const t = prompt.trim();
    prompt = "";
    C.send(t, pendingImage ?? undefined, pendingFile ?? undefined);
    pendingImage = null; pendingFile = null;
  }
  function onKey(e: KeyboardEvent) { if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); doSend(); } }
  function onFile() {
    const f = fileEl?.files?.[0]; if (!f) return;
    if (tab?.modality === "audio-stt") pendingFile = f;
    else { const rd = new FileReader(); rd.onload = () => (pendingImage = rd.result as string); rd.readAsDataURL(f); }
    if (fileEl) fileEl.value = "";
  }
  function onTarget(e: Event) {
    const t = tab; if (!t || t.bridge) return;
    const r = chat.running.find((x) => x.port === parseInt((e.currentTarget as HTMLSelectElement).value, 10));
    if (r) { t.port = r.port; t.modality = r.modality; t.name = r.name; }
  }
  function openBridge() {
    const cands = chat.running.filter((r) => isStreamMod(r.modality));
    if (cands.length < 2) { alert("Run at least two text/chat-capable models to bridge them."); return; }
    bA = cands[0].port; bB = cands[1].port; bridgeOpen = true;
  }
  function exportChat(fmt: "md" | "json") {
    const t = tab; if (!t || !t.msgs.length) { alert("Nothing to export yet."); return; }
    const who = (m: Msg) => (m.bridgeScene ? "Scene" : m.bridgeFrom || (m.role === "user" ? "You" : "Model"));
    let content: string, mime: string, ext: string;
    if (fmt === "json") {
      content = JSON.stringify({ model: t.name, messages: t.msgs.map((m) => ({ who: who(m), kind: m.kind || "text", content: m.content })) }, null, 2);
      mime = "application/json"; ext = "json";
    } else {
      content = `# Chat — ${t.name}\n\n` + t.msgs.map((m) => `**${who(m)}:**\n\n${m.content || "(" + (m.kind || "media") + ")"}`).join("\n\n---\n\n");
      mime = "text/markdown"; ext = "md";
    }
    const a = document.createElement("a");
    a.href = URL.createObjectURL(new Blob([content], { type: mime }));
    a.download = `chat-${(t.name || "session").replace(/[^a-z0-9]+/gi, "-")}.${ext}`;
    a.click();
  }
</script>

<div class="card chat-card">
  <aside class="side" class:hidden={sideCollapsed}>
    <div class="side-head">
      <span class="side-title">Chats</span>
      <div class="side-actions">
        <button class="new" onclick={C.newChatTab}><Icon name="plus" size={14} /> New</button>
        <button class="icon-btn" onclick={() => (sideCollapsed = true)} title="Hide threads"><Icon name="chevronsLeft" size={15} /></button>
      </div>
    </div>
    <div class="threads">
      {#if !chat.tabs.length}
        <div class="threads-empty">No chats yet — start one with “New”.</div>
      {:else}
        {#each chat.tabs as t (t.id)}
          <button class="ctab" class:active={t.id === chat.active} onclick={() => (chat.active = t.id)}>
            <span class="ctab-ic mod-{t.modality}"><Icon name={t.bridge ? "link" : modMeta(t.modality).icon} size={14} /></span>
            <span class="ctab-name">{t.name || "chat"}</span>
            <span class="ctab-x" role="button" tabindex="0" onkeydown={(e) => e.key === "Enter" && C.closeTab(t.id)} onclick={(e) => { e.stopPropagation(); C.closeTab(t.id); }}><Icon name="x" size={13} /></span>
          </button>
        {/each}
      {/if}
    </div>
  </aside>

  <div class="main">
    <div class="toolbar">
      {#if sideCollapsed}<button class="icon-btn" onclick={() => (sideCollapsed = false)} title="Show threads"><Icon name="menu" size={16} /></button>{/if}
      <div class="tleft">
        <span class="tlabel">Model</span>
        {#if chat.running.length || tabStopped}
          <select class="model-sel" value={tab && !tab.bridge ? String(tab.port) : ""} disabled={!!tab?.bridge} onchange={onTarget}>
            {#if tabStopped && tab}<option value={String(tab.port)}>{tab.name} · (stopped) · :{tab.port}</option>{/if}
            {#each chat.running as r (r.port)}<option value={String(r.port)}>{r.name} · {modMeta(r.modality).label} · :{r.port}</option>{/each}
          </select>
          {#if tab && !tab.bridge}<ModalityBadge mod={tab.modality} />{/if}
        {:else}
          <span class="tnone">none running</span>
        {/if}
        <button class="icon-btn" onclick={C.refreshTargets} title="Refresh running models"><Icon name="refresh" size={16} /></button>
      </div>
      <div class="tright">
        {#if chat.bridging}<button class="ghost small danger" onclick={C.bridgeStop}>Stop bridge</button>{/if}
        <button class="ghost small" onclick={openBridge}><Icon name="link" size={15} /> Bridge</button>
        <button class="ghost small" onclick={() => exportChat("md")}>Export</button>
        <button class="ghost small" onclick={C.clearChat}>Clear</button>
      </div>
    </div>

    {#if showSettings}
      <details class="sys"><summary>System prompt (optional)</summary>
        <textarea rows="2" value={tab!.system} oninput={(e) => (tab!.system = (e.currentTarget as HTMLTextAreaElement).value)} placeholder="You are a helpful assistant."></textarea>
      </details>
    {/if}
    {#if showSettings && tab}
      <details class="settings"><summary>Response style</summary>
        <div class="sgrid">
          <div class="sfield">
            <div class="shead"><span>Temperature</span><span class="sval">{tab.temp.toFixed(2)} · {tempMood(tab.temp)}</span></div>
            <input class="slider temp" type="range" min="0" max="1.5" step="0.05" value={tab.temp} oninput={(e) => (tab.temp = parseFloat((e.currentTarget as HTMLInputElement).value))} />
          </div>
          <div class="sfield">
            <div class="shead"><span>Top-P</span><span class="sval">{tab.topP.toFixed(2)}</span></div>
            <input class="slider" type="range" min="0.1" max="1" step="0.05" value={tab.topP} oninput={(e) => (tab.topP = parseFloat((e.currentTarget as HTMLInputElement).value))} />
          </div>
          <div class="sfield seed">
            <div class="shead"><span>Seed</span><span class="sval">{tab.seed == null ? "random" : "locked · " + tab.seed}</span></div>
            <div class="seed-row">
              <input type="number" placeholder="random" value={tab.seed ?? ""} oninput={(e) => { const v = (e.currentTarget as HTMLInputElement).value.trim(); tab.seed = v === "" ? null : parseInt(v, 10) || 0; }} />
              <button class="ghost small" onclick={() => (tab.seed = Math.floor(Math.random() * 1e9))} title="Roll & lock">🎲</button>
              <button class="ghost small" onclick={() => (tab.seed = null)}>Random</button>
            </div>
          </div>
        </div>
      </details>
    {/if}

    <div class="log" bind:this={logEl}>
      {#if !tab || (!tab.msgs.length && !tab.busy)}
        <div class="empty-chat"><div class="greet">Hello there</div><div class="greet-sub">{tab && tab.port ? `You're chatting with ${tab.name}.` : "Run a model from the Images page, then come back here."}</div></div>
      {:else}
        {#each tab.msgs as m, i (i)}
          {#if m.pending}
            <div class="msg msg-assistant">
              <div class="msg-body"><GenLoader eta={m.pending.eta} label={m.pending.label} /></div>
            </div>
          {:else}
            {@const b = bubble(m, tab)}
            {#if b.scene}
              <div class="msg-scene"><Icon name="link" size={13} /> <span>{b.html}</span></div>
            {:else}
              <div class={b.cls}>
                {#if b.showWho}<div class="msg-who">{b.who}</div>{/if}
                <div class="msg-body">{@html b.html}</div>
              </div>
            {/if}
          {/if}
        {/each}
      {/if}
    </div>

    {#if !tab?.bridge}
      <form class="composer" onsubmit={doSend}>
        {#if pendingImage || pendingFile}<div class="att">{pendingImage ? "📎 image" : "📎 " + pendingFile?.name}<button type="button" onclick={() => { pendingImage = null; pendingFile = null; }}><Icon name="x" size={13} /></button></div>{/if}
        <textarea rows="1" bind:value={prompt} onkeydown={onKey} placeholder={C.MODE[tab?.modality ?? "text"]?.ph} disabled={!!C.MODE[tab?.modality ?? "text"]?.disabled}></textarea>
        <input type="file" bind:this={fileEl} hidden accept={C.MODE[tab?.modality ?? "text"]?.attach ?? ""} onchange={onFile} />
        <div class="cbar">
          {#if C.MODE[tab?.modality ?? "text"]?.attach}<button type="button" class="cicon" onclick={() => fileEl?.click()} title="Attach"><Icon name="paperclip" size={18} /></button>{/if}
          {#if tab?.modality === "video"}
            <label class="vidlen" title="Longer clips use more VRAM and time">
              <Icon name="film" size={13} /> Length
              <select value={String(tab.vidFrames ?? 33)} onchange={(e) => { if (tab) tab.vidFrames = parseInt((e.currentTarget as HTMLSelectElement).value, 10); }}>
                <option value="17">~1s</option>
                <option value="33">~2s</option>
                <option value="49">~3s</option>
                <option value="65">~4s</option>
                <option value="81">~5s (max)</option>
              </select>
            </label>
          {/if}
          <span class="chint">{#if tab}<Icon name={modMeta(tab.modality).icon} size={13} /> {tab.name}{/if}</span>
          <button type="submit" class="csend" title="Send"><Icon name="arrowup" size={18} /></button>
        </div>
      </form>
    {/if}
  </div>
</div>

<Modal bind:open={bridgeOpen} title="Bridge two models">
  <p class="hint">They'll take turns replying in character. Seed the conversation with an opening line or scene.</p>
  <div class="bgrid">
    <label>Model A <select bind:value={bA}>{#each chat.running.filter((r) => isStreamMod(r.modality)) as r (r.port)}<option value={r.port}>{r.name}</option>{/each}</select></label>
    <label>Model B <select bind:value={bB}>{#each chat.running.filter((r) => isStreamMod(r.modality)) as r (r.port)}<option value={r.port}>{r.name}</option>{/each}</select></label>
  </div>
  <label class="blk">Seed message <textarea rows="2" bind:value={bSeed} placeholder="You both are playing Minecraft hardcore survival…"></textarea></label>
  <div class="bgrid">
    <label>Exchanges <input type="number" min="1" max="20" bind:value={bRounds} /></label>
    <label class="check"><input type="checkbox" bind:checked={bThink} /> <span>Let models think (slower; richer on a capable model)</span></label>
  </div>
  <p class="hint">Tip: tiny models ramble or repeat — a 7B+ model on GPU makes far livelier exchanges.</p>
  <div class="bactions"><button class="ghost" onclick={() => (bridgeOpen = false)}>Cancel</button><button class="primary" onclick={() => { bridgeOpen = false; C.runBridge(bA, bB, bSeed.trim(), bRounds, bThink); }}>Start</button></div>
</Modal>

<style>
  .chat-card { display: flex; flex-direction: row; padding: 0; overflow: hidden; height: calc(100vh - 178px); min-height: 460px; }
  .side { width: 244px; flex: none; display: flex; flex-direction: column; background: #14171e; border-right: 1px solid var(--border); }
  .side.hidden { display: none; }
  .side-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 14px 12px 10px; }
  .side-title { font-size: 11.5px; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: 0.5px; }
  .side-actions { display: inline-flex; gap: 6px; }
  .new { display: inline-flex; align-items: center; gap: 5px; padding: 6px 11px; font-size: 12.5px; background: #1c2740; border: 1px solid #33508f; color: #cfe0ff; border-radius: 8px; }
  .icon-btn { padding: 6px 8px; display: inline-flex; align-items: center; justify-content: center; }
  .threads { display: flex; flex-direction: column; gap: 3px; padding: 4px 8px 12px; overflow: auto; flex: 1; }
  .threads-empty { color: var(--muted); font-size: 12.5px; padding: 12px 8px; }
  .ctab { width: 100%; display: inline-flex; align-items: center; gap: 7px; padding: 8px 10px; border-radius: 8px; background: transparent; border: 1px solid transparent; color: var(--muted); }
  .ctab:hover { background: #161b27; }
  .ctab.active { background: #182338; color: #fff; border-color: var(--border); }
  .ctab-name { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-size: 13px; text-align: left; }
  .ctab-x { opacity: 0.55; display: inline-flex; }
  .ctab-x:hover { opacity: 1; }
  .main { flex: 1; min-width: 0; display: flex; flex-direction: column; padding: 16px 20px; }
  .toolbar { display: flex; align-items: center; gap: 10px 12px; flex-wrap: wrap; padding-bottom: 12px; border-bottom: 1px solid var(--border); }
  .tleft { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1 1 auto; }
  .tlabel { font-size: 12.5px; color: var(--muted); flex: none; }
  .tnone { font-size: 12.5px; color: var(--muted); font-style: italic; }
  /* No vertical padding override -> the base 9px padding + 34px height clipped
     the option text. Center it with line-height instead. */
  .model-sel { min-width: 0; max-width: 340px; flex: 0 1 auto; height: 34px; padding-top: 0; padding-bottom: 0; line-height: 32px; }
  .tright { margin-left: auto; display: flex; align-items: center; gap: 6px; flex: none; }
  /* Uniform control height so the select, badge, and buttons line up cleanly. */
  .toolbar .ghost.small { height: 34px; display: inline-flex; align-items: center; gap: 5px; }
  .toolbar .icon-btn { height: 34px; width: 34px; padding: 0; flex: none; }
  .toolbar :global(.badge) { flex: none; }
  .sys, .settings { margin-top: 10px; }
  .sys summary, .settings summary { cursor: pointer; color: var(--muted); font-size: 13px; }
  .sys textarea { margin-top: 8px; }
  .sgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px 24px; margin-top: 12px; }
  .seed { grid-column: 1 / -1; }
  .sfield { display: flex; flex-direction: column; gap: 8px; }
  .shead { display: flex; justify-content: space-between; font-size: 12.5px; color: var(--muted); }
  .sval { color: var(--text); font-size: 12px; }
  .slider { -webkit-appearance: none; appearance: none; width: 100%; height: 8px; border-radius: 999px; background: #2a2f3a; }
  .slider::-webkit-slider-thumb { -webkit-appearance: none; width: 18px; height: 18px; border-radius: 50%; background: #fff; border: 2px solid var(--accent-2); }
  .slider.temp { background: linear-gradient(90deg, #4f8cff, #46c969 32%, #e3b341 58%, #f0683c 80%, #f85149); }
  .seed-row { display: flex; align-items: center; gap: 8px; }
  .seed-row input { width: 150px; height: 34px; }
  .seed-row button { height: 34px; }
  .log { flex: 1; overflow: auto; margin: 8px 0; padding: 8px 6px; display: flex; flex-direction: column; gap: 22px; }
  .empty-chat { margin: auto; text-align: center; padding: 40px 10px; }
  .greet { font-size: 30px; font-weight: 700; margin-bottom: 8px; }
  .greet-sub { color: var(--muted); font-size: 15px; }
  .msg { display: flex; flex-direction: column; gap: 5px; }
  .msg-user { align-self: flex-end; align-items: flex-end; max-width: 82%; }
  .msg-assistant { align-self: stretch; max-width: 100%; }
  .msg-who { display: none; font-size: 12px; color: #aab4c8; font-weight: 600; }
  .msg-named .msg-who { display: block; }
  .msg-body { font-size: 14.5px; line-height: 1.5; }
  .msg-user .msg-body { background: #2b2f3a; border-radius: 18px; padding: 10px 14px; }
  .msg-error .msg-body { color: #ffb3ad; }
  .msg-scene { align-self: center; display: inline-flex; align-items: center; gap: 7px; padding: 6px 14px; border: 1px dashed var(--border); border-radius: 999px; color: var(--muted); font-size: 12.5px; font-style: italic; margin: 2px auto; }
  .composer { background: #1c1f27; border: 1px solid #2c313d; border-radius: 24px; padding: 8px 8px 8px 16px; display: flex; flex-direction: column; gap: 4px; }
  .composer textarea { background: transparent; border: none; resize: none; padding: 8px 0 2px; max-height: 200px; font-size: 15px; }
  .composer textarea:focus { outline: none; }
  .cbar { display: flex; align-items: center; gap: 8px; }
  .vidlen { display: inline-flex; align-items: center; gap: 5px; font-size: 11.5px; color: var(--muted); flex: none; }
  .vidlen select { width: auto; padding: 4px 6px; font-size: 11.5px; }
  .chint { flex: 1; display: inline-flex; align-items: center; gap: 5px; font-size: 11.5px; color: var(--muted); }
  .cicon { width: 34px; height: 34px; border-radius: 50%; padding: 0; display: inline-flex; align-items: center; justify-content: center; background: #2a2e38; }
  .csend { width: 36px; height: 36px; border-radius: 50%; padding: 0; display: inline-flex; align-items: center; justify-content: center; background: var(--accent-2); border: none; color: #fff; }
  .att { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: #cbd3e1; }
  .att button { padding: 2px; background: transparent; border: none; }
  .bgrid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin: 10px 0; }
  .bgrid label, .blk { display: flex; flex-direction: column; gap: 6px; font-size: 13px; color: var(--muted); }
  .check { flex-direction: row; align-items: center; gap: 8px; }
  .check input { width: auto; }
  .bactions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 14px; }
  :global(.chat-img) { max-width: 100%; border-radius: 10px; }
  :global(.chat-img.sm) { max-width: 220px; margin-bottom: 6px; }
  :global(.chat-video) { max-width: 100%; max-height: 480px; border-radius: 10px; background: #000; }
  :global(.media-lost) { color: var(--muted); font-size: 12.5px; font-style: italic; }
  :global(.code-block) { margin: 8px 0; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; background: #06080c; }
  :global(.code-head) { display: flex; justify-content: space-between; padding: 5px 10px; background: #10141b; font-size: 11px; color: var(--muted); }
  :global(.code-block pre) { margin: 0; padding: 11px 12px; overflow: auto; }
  :global(.think) { margin: 0 0 8px; border: 1px dashed var(--border); border-radius: 8px; background: #0f1320; }
  :global(.think > summary) { cursor: pointer; padding: 6px 10px; font-size: 12px; color: var(--muted); }
  :global(.think-body) { padding: 0 12px 10px; font-size: 12.5px; color: #9aa6bd; white-space: pre-wrap; }
  :global(.typing i) { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: var(--muted); margin: 0 2px; }
</style>
