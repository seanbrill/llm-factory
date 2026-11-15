<script lang="ts">
  // Output settings for media models: image size and steps, video size and
  // steps, negative prompt and seed, and a voice model's voice, speed and style.
  import { imageDefaults, ttsSpec } from "../../lib/meta";
  import { save, type Tab } from "../../lib/chat.svelte";

  let { tab, mod, model }: { tab: Tab; mod: "image" | "video" | "tts"; model: string } = $props();

  const int = (v: string, d: number) => parseInt(v, 10) || d;
  function seed(v: string) {
    const s = v.trim();
    tab.mediaSeed = s === "" ? null : parseInt(s, 10) || 0;
    save();
  }
  const spec = $derived(ttsSpec(model));
  const voice = $derived(spec.voices.some(([v]) => v === tab.ttsVoice) ? tab.ttsVoice : spec.voices[0]?.[0]);
</script>

{#if mod === "image"}
  <div class="g">
    <label>Size
      <select value={String(tab.imgRes ?? 512)} onchange={(e) => ((tab.imgRes = int(e.currentTarget.value, 512)), save())}>
        <option value="512">512 × 512</option><option value="768">768 × 768</option><option value="1024">1024 × 1024</option>
      </select>
    </label>
    <label>Steps
      <input type="number" min="1" max="50" value={tab.imgSteps ?? imageDefaults(model).steps} oninput={(e) => ((tab.imgSteps = int(e.currentTarget.value, imageDefaults(model).steps)), save())} />
    </label>
  </div>
{:else if mod === "video"}
  <div class="g">
    <label>Size
      <select value={tab.vidRes ?? "832x480"} onchange={(e) => ((tab.vidRes = e.currentTarget.value), save())}>
        <option value="832x480">832 × 480 (480p)</option><option value="512x512">512 × 512</option><option value="1280x720">1280 × 720 (720p)</option>
      </select>
    </label>
    <label>Steps
      <input type="number" min="1" max="30" value={tab.vidSteps ?? 10} oninput={(e) => ((tab.vidSteps = int(e.currentTarget.value, 10)), save())} />
    </label>
  </div>
{/if}

{#if mod === "image" || mod === "video"}
  <label>Leave out
    <input type="text" value={tab.negative ?? ""} oninput={(e) => ((tab.negative = e.currentTarget.value), save())} placeholder="blurry, text, watermark" />
  </label>
  <label>Seed
    <input type="number" inputmode="numeric" placeholder="random" value={tab.mediaSeed ?? ""} oninput={(e) => seed(e.currentTarget.value)} />
  </label>
  <p class="hint">Bigger sizes and more steps take longer{mod === "video" ? " and need more memory; the clip length is next to Send" : ""}.</p>
{/if}

{#if mod === "tts"}
  {#if spec.voices.length}
    <label>Voice
      <select value={voice} onchange={(e) => ((tab.ttsVoice = e.currentTarget.value), save())}>
        {#each spec.voices as [id, name] (id)}<option value={id}>{name}</option>{/each}
      </select>
    </label>
  {/if}
  {#if spec.speed}
    <div class="f">
      <div class="fh"><label for="pg-tts-speed">Speed</label><span class="mono">{(tab.ttsSpeed ?? 1).toFixed(1)}×</span></div>
      <input id="pg-tts-speed" type="range" min="0.5" max="2" step="0.1" value={tab.ttsSpeed ?? 1} oninput={(e) => ((tab.ttsSpeed = parseFloat(e.currentTarget.value)), save())} />
    </div>
  {/if}
  {#if spec.style}
    <label>Style
      <input type="text" value={tab.ttsStyle ?? ""} oninput={(e) => ((tab.ttsStyle = e.currentTarget.value), save())} placeholder="excited, whispering, slow and calm" />
    </label>
  {/if}
  {#if !spec.voices.length && !spec.speed && !spec.style}
    <p class="hint">This voice model has one voice and no settings.</p>
  {/if}
{/if}

<style>
  .g { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  label { display: flex; flex-direction: column; gap: 6px; color: var(--ink); }
  .f { display: flex; flex-direction: column; gap: 6px; }
  .fh { display: flex; justify-content: space-between; font-size: 13px; }
  .fh .mono { color: var(--mu); font-size: 12px; }
  .hint { color: var(--fa); font-size: 12px; }
</style>
