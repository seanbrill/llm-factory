<script lang="ts">
  // "When each build finishes" (sheet frame f9): what happens to each image once
  // it's built. Restart behaviour is baked into each image (set per build in its
  // settings; this switch sets all of them), the .tar export is always on, and
  // browser notifications are this browser's own.
  import { gb } from "../../lib/format";
  import { notify, notifyCan, setAutostartAll, setNotify, type PlanItem } from "../../lib/plan.svelte";

  let { items, weightsGB }: { items: PlanItem[]; weightsGB: number } = $props();

  const on = $derived(items.filter((i) => i.autostart).length);
  const all = $derived(items.length > 0 && on === items.length);
  const mixed = $derived(on > 0 && on < items.length);

  let auto: HTMLInputElement | undefined = $state();
  $effect(() => {
    if (auto) auto.indeterminate = mixed;
  });

  async function toggleNotify(e: Event) {
    const el = e.currentTarget as HTMLInputElement;
    await setNotify(el.checked);
    el.checked = notify.on; // the browser may have said no
  }

  const notifyNote = $derived(
    !notifyCan
      ? "This browser only allows it on an https or localhost address."
      : notify.denied
        ? "Blocked for this site in the browser's settings."
        : "When each build ends or fails, while this tab is in the background.",
  );
</script>

<section class="card" aria-labelledby="finish-h">
  <h2 id="finish-h">When each build finishes</h2>
  <ul class="opts">
    <li>
      <label for="fin-auto">
        <b>Bring it back after a restart</b>
        <small>{mixed ? `On for ${on} of ${items.length}. ` : ""}Once running, it comes back when the engine or the factory restarts.</small>
      </label>
      <input id="fin-auto" type="checkbox" class="tg" bind:this={auto} checked={all} onchange={(e) => setAutostartAll(e.currentTarget.checked)} />
    </li>
    <li>
      <span class="lb">
        <b>Export a .tar for another machine</b>
        <small>Every image is also saved to images/{weightsGB ? `, about ${gb(weightsGB)} in all` : ""}.</small>
      </span>
      <span class="chip">Always</span>
    </li>
    <li>
      <label for="fin-notify">
        <b>Notify me in the browser</b>
        <small>{notifyNote}</small>
      </label>
      <input id="fin-notify" type="checkbox" class="tg" checked={notify.on} disabled={!notifyCan || notify.denied} onchange={toggleNotify} />
    </li>
  </ul>
</section>

<style>
  h2 { margin-bottom: 6px !important; }
  .opts { list-style: none; margin: 0; padding: 0; }
  li { display: flex; align-items: center; gap: 14px; padding: 13px 0; border-top: 1px solid var(--ln); }
  li:first-child { border-top: 0; }
  label, .lb { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; cursor: pointer; }
  .lb { cursor: default; }
  b { font-weight: 500; font-size: 13.5px; }
  small { color: var(--mu); font-size: 12.5px; line-height: 1.4; }
</style>
