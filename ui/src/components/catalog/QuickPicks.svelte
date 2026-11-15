<script lang="ts">
  // Quick picks (sheet frame f8): a working set of models in one click. The
  // factory's sets come from the catalog and what runs here now; saved ones
  // are the user's own (this browser).
  import { tick } from "svelte";
  import Icon from "../Icon.svelte";
  import { shortName, type QuickPick } from "../../lib/catalog";

  let {
    picks,
    isOn,
    onpick,
    onremove,
    onsave,
    canSave,
  }: {
    picks: QuickPick[];
    isOn: (p: QuickPick) => boolean;
    onpick: (p: QuickPick) => void;
    onremove: (p: QuickPick) => void;
    onsave: (label: string) => void;
    canSave: boolean;
  } = $props();

  let saving = $state(false);
  let label = $state("");
  let input: HTMLInputElement | undefined = $state();

  async function openSave() {
    saving = true;
    label = "";
    await tick();
    input?.focus();
  }
  function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!label.trim()) return;
    onsave(label);
    saving = false;
  }
  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      saving = false;
    }
  }
</script>

<div class="kits" role="group" aria-label="Quick picks">
  <span class="kl">Quick picks</span>
  {#each picks as p (p.id)}
    {@const on = isOn(p)}
    <span class="kit" class:on class:custom={p.custom}>
      <button type="button" class="kb" onclick={() => onpick(p)} aria-pressed={on}
        title={on ? `Remove ${p.label} from the plan` : `Add ${p.models.map((x) => x.name).join(", ")} to the plan`}>
        <Icon name={on ? "check" : "plus"} size={15} />{p.label}
        <small>{p.models.map(shortName).join(" · ")}</small>
      </button>
      {#if p.custom}
        <button type="button" class="kx" onclick={() => onremove(p)} aria-label="Forget the pick {p.label}" title="Forget this pick"><Icon name="xmark" size={13} /></button>
      {/if}
    </span>
  {/each}
  {#if saving}
    <form class="save" onsubmit={submit}>
      <label class="sr" for="pick-name">Name for this pick</label>
      <input id="pick-name" bind:this={input} bind:value={label} onkeydown={onKey} placeholder="Name this pick" maxlength="40" autocomplete="off" />
      <button type="submit" class="small primary" disabled={!label.trim()}>Save</button>
      <button type="button" class="small ghost" onclick={() => (saving = false)}>Cancel</button>
    </form>
  {:else}
    <span class="kit">
      <button type="button" class="kb" onclick={openSave} disabled={!canSave}
        title={canSave ? "Save the models in your plan as a quick pick" : "Pick some models first, then save them as a quick pick"}>
        <Icon name="plus" size={15} />Save as a pick
      </button>
    </span>
  {/if}
</div>

<style>
  .kits { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
  .kl { font-size: 12.5px; color: var(--fa); margin-right: 4px; white-space: nowrap; }
  .kit {
    display: inline-flex;
    align-items: center;
    height: 30px;
    border: 1px dashed var(--ln2);
    border-radius: 99px;
    flex: none;
    max-width: 100%;
  }
  .kit.on { border-style: solid; border-color: color-mix(in srgb, var(--pr) 55%, transparent); background: var(--prs); }
  .kit.custom { border-style: solid; }
  .kit.custom.on { border-color: color-mix(in srgb, var(--pr) 55%, transparent); }
  .kb {
    height: 28px;
    min-height: 0;
    padding: 0 11px;
    gap: 8px;
    border: 0;
    border-radius: 99px;
    background: none;
    font-size: 12.5px;
    font-weight: 500;
    color: var(--ink);
    min-width: 0;
  }
  .kb:hover { background: var(--p2); }
  .kit.on .kb:hover { background: none; }
  .kb :global(.ico) { color: var(--pr); }
  .kb small {
    color: var(--fa);
    font-family: var(--font-mono);
    font-size: 11px;
    overflow: hidden;
    text-overflow: ellipsis;
    min-width: 0;
  }
  .kit.custom .kb { padding-right: 4px; }
  .kx {
    width: 24px;
    height: 24px;
    min-height: 0;
    padding: 0;
    margin-right: 3px;
    border: 0;
    border-radius: 99px;
    background: none;
    color: var(--fa);
  }
  .kx:hover { background: var(--p3); color: var(--ink); }
  .save { display: inline-flex; align-items: center; gap: 6px; }
  .save input { width: 180px; height: 30px; padding: 4px 10px; border-radius: 99px; font-size: 12.5px; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
  @media (max-width: 760px) {
    .kits { flex-wrap: nowrap; overflow-x: auto; padding-bottom: 4px; scrollbar-width: none; }
    .kits::-webkit-scrollbar { display: none; }
  }
</style>
