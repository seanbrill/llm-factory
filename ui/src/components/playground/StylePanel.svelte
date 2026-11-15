<script lang="ts">
  // Sampling for a tab: temperature, top-p and seed. Both sides of a comparison
  // use the same values, so the difference is the model.
  import { save, type Tab } from "../../lib/chat.svelte";

  let { tab }: { tab: Tab } = $props();

  const mood = (t: number) =>
    t <= 0.01 ? "the same answer every time" : t < 0.3 ? "precise" : t < 0.6 ? "balanced" : t < 0.9 ? "creative" : t < 1.2 ? "loose" : "chaotic";
  const set = <K extends "temp" | "topP">(k: K, v: number) => {
    tab[k] = v;
    save();
  };
  function setSeed(v: string) {
    const s = v.trim();
    tab.seed = s === "" ? null : parseInt(s, 10) || 0;
    save();
  }
</script>

<div class="f">
  <div class="fh"><label for="pg-temp">Temperature</label><span class="mono">{tab.temp.toFixed(2)} · {mood(tab.temp)}</span></div>
  <input id="pg-temp" type="range" min="0" max="1.5" step="0.05" value={tab.temp} oninput={(e) => set("temp", parseFloat(e.currentTarget.value))} />
</div>
<div class="f">
  <div class="fh"><label for="pg-topp">Top-p</label><span class="mono">{tab.topP.toFixed(2)}</span></div>
  <input id="pg-topp" type="range" min="0.1" max="1" step="0.05" value={tab.topP} oninput={(e) => set("topP", parseFloat(e.currentTarget.value))} />
</div>
<div class="f">
  <div class="fh"><label for="pg-seed">Seed</label><span class="mono">{tab.seed == null ? "random" : "fixed"}</span></div>
  <div class="sr">
    <input id="pg-seed" type="number" inputmode="numeric" placeholder="random" value={tab.seed ?? ""} oninput={(e) => setSeed(e.currentTarget.value)} />
    <button class="small" type="button" onclick={() => ((tab.seed = Math.floor(Math.random() * 1e9)), save())}>Pick one</button>
    <button class="small" type="button" disabled={tab.seed == null} onclick={() => ((tab.seed = null), save())}>Random</button>
  </div>
  <p class="hint">A fixed seed gives the same answer to the same prompt, so a rerun compares like with like.</p>
</div>

<style>
  .f { display: flex; flex-direction: column; gap: 6px; }
  .fh { display: flex; justify-content: space-between; gap: 10px; font-size: 13px; }
  .fh .mono { color: var(--mu); font-size: 12px; }
  .sr { display: flex; gap: 6px; align-items: center; }
  .sr input { height: 34px; }
  .hint { color: var(--fa); font-size: 12px; }
</style>
