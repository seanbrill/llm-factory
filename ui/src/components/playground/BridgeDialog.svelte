<script lang="ts">
  // Set up the bridge: two running chat models take turns in character,
  // starting from a scene. It opens in a tab of its own.
  import { untrack } from "svelte";
  import Dialog from "../library/Dialog.svelte";
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { isStreamMod } from "../../lib/util";
  import { runBridge, chat, type Target } from "../../lib/chat.svelte";

  let { open = $bindable(false), targets }: { open?: boolean; targets: Target[] } = $props();

  const chatty = $derived(targets.filter((t) => isStreamMod(t.modality)));
  let a = $state(0);
  let b = $state(0);
  let seed = $state("");
  let rounds = $state(4);
  let think = $state(false);

  $effect(() => {
    if (!open) return;
    const list = chatty;
    untrack(() => {
      if (!list.some((t) => t.port === a)) a = list[0]?.port ?? 0;
      if (!list.some((t) => t.port === b) || b === a) b = list.find((t) => t.port !== a)?.port ?? 0;
    });
  });

  const ready = $derived(chatty.length >= 2 && !!a && !!b && a !== b && !!seed.trim() && !chat.bridging);

  function start() {
    const ta = chatty.find((t) => t.port === a);
    const tb = chatty.find((t) => t.port === b);
    if (!ta || !tb || !ready) return;
    open = false;
    runBridge(ta, tb, seed.trim(), Math.max(1, Math.min(20, rounds || 1)), think);
  }
</script>

<Dialog bind:open title="Let two models talk" sub="They take turns in character, starting from a scene you set. It opens in its own tab.">
  {#if chatty.length < 2}
    <div class="callout wa">
      <Icon name="warn" />
      <span><b>Two chat models need to be running.</b><p>{chatty.length ? "Only one is running now." : "None is running now."} Start another from the <a href={href("library")}>Library</a>.</p></span>
    </div>
  {:else}
    <div class="g">
      <label>First speaker
        <select bind:value={a}>{#each chatty as t (t.port)}<option value={t.port}>{t.persona || t.name} · :{t.port}</option>{/each}</select>
      </label>
      <label>Second speaker
        <select bind:value={b}>{#each chatty as t (t.port)}<option value={t.port} disabled={t.port === a}>{t.persona || t.name} · :{t.port}</option>{/each}</select>
      </label>
    </div>
    <label>Scene
      <textarea rows="3" bind:value={seed} placeholder="Two sysadmins on a night shift argue about who broke the build."></textarea>
    </label>
    <div class="g">
      <label>Turns each
        <input type="number" min="1" max="20" bind:value={rounds} />
      </label>
      <label class="ck"><input type="checkbox" bind:checked={think} /><span>Let them think before each line (slower; better on a capable model)</span></label>
    </div>
    <p class="hint">Small models ramble or repeat themselves; a 7B or larger model on the GPU makes livelier talk.</p>
    {#if chat.bridging}<p class="hint">A bridge is already running; stop it first.</p>{/if}
  {/if}
  {#snippet actions()}
    <button type="button" onclick={() => (open = false)}>Cancel</button>
    <button class="primary" type="button" disabled={!ready} onclick={start}><Icon name="chain" size={14} />Start</button>
  {/snippet}
</Dialog>

<style>
  .g { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; margin-bottom: 12px; align-items: end; }
  label { display: flex; flex-direction: column; gap: 6px; }
  label + label, .g + label { margin-top: 0; }
  textarea { margin-bottom: 12px; }
  .ck { flex-direction: row; align-items: center; gap: 8px; color: var(--mu); padding-bottom: 8px; }
  .hint { color: var(--fa); font-size: 12.5px; }
  @media (max-width: 760px) { .g { grid-template-columns: 1fr; } }
</style>
