<script lang="ts">
  // The menu behind a queued build's handle: move it within its lane, or take
  // it out of the queue. Escape or a click elsewhere closes it.
  import Icon from "../Icon.svelte";
  import type { MoveTo } from "../../lib/build.svelte";

  let {
    name,
    first,
    last,
    onmove,
    onremove,
    onclose,
  }: {
    name: string;
    first: boolean;
    last: boolean;
    onmove: (to: MoveTo) => void;
    onremove: () => void;
    onclose: (refocus: boolean) => void;
  } = $props();

  let menu: HTMLDivElement | undefined = $state();

  const items = () => [...(menu?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])];

  $effect(() => {
    items()[0]?.focus();
    const away = (e: PointerEvent) => {
      const t = e.target as Node | null;
      if (menu && t && !menu.contains(t) && !(t instanceof Element && t.closest("[data-menu-owner]"))) onclose(false);
    };
    window.addEventListener("pointerdown", away, true);
    return () => window.removeEventListener("pointerdown", away, true);
  });

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      onclose(true);
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const all = items();
    if (!all.length) return;
    const at = all.indexOf(document.activeElement as HTMLButtonElement);
    let next = 0;
    if (e.key === "ArrowDown") next = (at + 1) % all.length;
    else if (e.key === "ArrowUp") next = (at - 1 + all.length) % all.length;
    else if (e.key === "End") next = all.length - 1;
    all[next].focus();
  }
</script>

<div class="menu" role="menu" aria-label="Move {name}" bind:this={menu} onkeydown={onKey} tabindex="-1">
  <button role="menuitem" disabled={first} onclick={() => onmove("top")}><Icon name="toTop" size={15} />Move to the front</button>
  <button role="menuitem" disabled={first} onclick={() => onmove("up")}><Icon name="arrowup" size={15} />Move up</button>
  <button role="menuitem" disabled={last} onclick={() => onmove("down")}><Icon name="arrowDown" size={15} />Move down</button>
  <button role="menuitem" disabled={last} onclick={() => onmove("bottom")}><Icon name="toBottom" size={15} />Move to the back</button>
  <hr />
  <button role="menuitem" class="danger" onclick={onremove}><Icon name="xmark" size={15} />Remove from the queue</button>
</div>

<style>
  .menu {
    position: absolute;
    z-index: 30;
    top: calc(100% - 4px);
    left: 0;
    min-width: 210px;
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 6px;
    background: var(--p2);
    border: 1px solid var(--ln2);
    border-radius: 12px;
    box-shadow: var(--shadow);
  }
  .menu:focus { outline: none; }
  button {
    justify-content: flex-start;
    gap: 9px;
    min-height: 32px;
    padding: 0 10px;
    border: 0;
    border-radius: 8px;
    background: transparent;
    font-weight: 500;
  }
  button:hover:not(:disabled), button:focus-visible { background: var(--p3); }
  button :global(.ico) { color: var(--mu); }
  button.danger :global(.ico) { color: var(--er); }
  hr { border: 0; border-top: 1px solid var(--ln); margin: 4px 2px; }
</style>
