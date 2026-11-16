<script lang="ts">
  // The Playground's saved prompts: put one in the box (with its system prompt
  // and sampling), send it, or save what's in the box. A saved prompt that
  // opens a conversation is recorded as a result, so the Prompts page can
  // compare every model it was sent to.
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { toast } from "../../lib/stores";
  import { applyPrompt, type Tab } from "../../lib/chat.svelte";
  import { promptState, refreshPrompts, createPrompt, type SavedPrompt } from "../../lib/prompts.svelte";

  let {
    tab,
    draft = $bindable(""),
    fromPrompt = $bindable(null),
    open = $bindable(false),
    onSend,
  }: {
    tab: Tab;
    draft?: string;
    fromPrompt?: SavedPrompt | null;
    open?: boolean;
    onSend: (p: SavedPrompt) => void;
  } = $props();

  let q = $state("");
  let name = $state("");
  let withSystem = $state(true);
  let saving = $state(false);

  $effect(() => {
    if (open) refreshPrompts();
  });

  const shown = $derived.by(() => {
    const t = q.trim().toLowerCase();
    if (!t) return promptState.list;
    return promptState.list.filter((p) => [p.name, p.text, ...(p.tags ?? [])].some((s) => s.toLowerCase().includes(t)));
  });

  /** Put a prompt in the box and the tab's settings to what it asks for. */
  function use(p: SavedPrompt) {
    draft = p.text;
    fromPrompt = p;
    const changed = applyPrompt(tab, p);
    if (changed.length) toast(`Set this chat's ${changed.join(" and ")} from “${p.name}”.`, 4000);
    open = false;
  }

  async function saveDraft() {
    const text = draft.trim();
    if (!text || saving) return;
    saving = true;
    try {
      const p = await createPrompt({
        name: name.trim(),
        text,
        system: withSystem && tab.system.trim() ? tab.system : undefined,
      });
      fromPrompt = p;
      name = "";
      await refreshPrompts();
      toast(`Saved “${p.name}”.`, 3000);
    } catch (e) {
      toast(`Couldn't save the prompt: ${(e as Error).message}`, 6000);
    } finally {
      saving = false;
    }
  }
</script>

<div class="pm">
  {#if promptState.list.length > 4}
    <input type="search" placeholder="Search saved prompts" aria-label="Search saved prompts" bind:value={q} />
  {/if}
  {#if promptState.error}
    <p class="hint err">{promptState.error}</p>
  {:else if !promptState.list.length}
    <p class="hint">{promptState.loaded ? "No saved prompts yet. Write one in the box and save it below." : "Loading…"}</p>
  {:else}
    <ul>
      {#each shown as p (p.id)}
        <li>
          <button class="pick" type="button" title="Put it in the message box" onclick={() => use(p)}>
            <b>{p.name}</b>
            <span>{p.text}</span>
            {#if p.system || p.tags?.length}
              <small>{#if p.system}system prompt{/if}{#if p.system && p.tags?.length} · {/if}{p.tags?.join(", ")}</small>
            {/if}
          </button>
          <button class="small icon" type="button" aria-label="Send “{p.name}”" title="Send it now" onclick={() => ((open = false), onSend(p))}><Icon name="play" size={13} /></button>
        </li>
      {:else}
        <li class="none">No prompt matches.</li>
      {/each}
    </ul>
  {/if}

  <div class="sv">
    <label for="pm-name">{draft.trim() ? "Save the message box as a prompt" : "To save a prompt, type it in the message box, then click the bookmark beside Send (or reopen this menu)."}</label>
    <div class="r" hidden={!draft.trim()}>
      <input id="pm-name" placeholder={draft.trim() ? draft.trim().split("\n")[0].slice(0, 60) : "Write a prompt first"} bind:value={name} disabled={!draft.trim()} onkeydown={(e) => e.key === "Enter" && saveDraft()} />
      <button class="small" type="button" disabled={!draft.trim() || saving} onclick={saveDraft}>Save</button>
    </div>
    {#if tab.system.trim()}
      <label class="ck"><input type="checkbox" bind:checked={withSystem} /> With this chat's system prompt</label>
    {/if}
  </div>
  <p class="hint">
    You can also write prompts on the <a href={href("prompts", "new")} onclick={() => (open = false)}>Prompts page</a>.
    When a saved prompt starts a conversation (a new or cleared chat), each model's answer and speed is kept.
    <a href={href("prompts")} onclick={() => (open = false)}>Compare models on the Prompts page</a>
  </p>
</div>

<style>
  .pm { display: flex; flex-direction: column; gap: 10px; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 4px; max-height: 300px; overflow: auto; }
  li { display: flex; align-items: flex-start; gap: 6px; }
  li.none { color: var(--fa); font-size: 13px; padding: 6px 4px; }
  .pick {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    text-align: left;
    padding: 7px 9px;
    border-radius: 9px;
    background: transparent;
    border: 1px solid transparent;
  }
  .pick:hover, .pick:focus-visible { background: var(--p3); border-color: var(--ln2); }
  .pick b { font-weight: 600; font-size: 13px; }
  .pick span { color: var(--mu); font-size: 12px; width: 100%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .pick small { color: var(--fa); font-size: 11px; }
  li .icon { margin-top: 6px; }
  .sv { display: flex; flex-direction: column; gap: 6px; padding-top: 10px; border-top: 1px solid var(--ln); }
  .sv > label { font-size: 12px; color: var(--mu); }
  .r { display: flex; gap: 6px; }
  .r input { flex: 1; min-width: 0; }
  .ck { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--mu); }
  .hint { margin: 0; font-size: 12px; color: var(--fa); line-height: 1.45; }
  .hint a { white-space: nowrap; }
  .err { color: var(--er, #e5484d); }
</style>
