<script lang="ts">
  import { modMeta } from "../lib/icons";
  import Icon from "../components/Icon.svelte";
  const mods = ["text", "vision", "image", "video", "audio-stt", "tts", "embedding"];
</script>

<div class="card help">
  <h2>What this is</h2>
  <p>
    The factory turns an open AI model into a <b>self-contained container image</b> you
    can run anywhere. Pick a model, choose an engine + compute, optionally bake in an
    initialization prompt, then build, run, and chat — all locally.
  </p>

  <h2>Modalities</h2>
  <ul class="mods">
    {#each mods as m (m)}
      <li><span class="badge mod mod-{m}"><Icon name={modMeta(m).icon} size={13} /> {modMeta(m).label}</span></li>
    {/each}
  </ul>

  <h2>Ensembles</h2>
  <p>
    Combine several built images into one multimodal super-model: a tiny <b>Conductor</b>
    routes each request to the right specialist and starts/stops them to fit your VRAM.
    See <code>docs/ENSEMBLE.md</code>.
  </p>

  <h2>CPU vs GPU</h2>
  <p>
    Compute (CPU / CUDA / Vulkan) is <b>baked at build time</b> — each is a different image.
    GPU doesn't change a model's quality, only its speed; a bigger model is what raises
    quality, and the GPU is what makes a bigger model usable.
  </p>
</div>

<style>
  .help { max-width: 760px; }
  .help h2 { font-size: 15px; margin: 18px 0 6px; }
  .help h2:first-child { margin-top: 0; }
  .help p { color: #c4cdde; font-size: 13.5px; margin: 0 0 8px; }
  .mods { list-style: none; padding: 0; margin: 0 0 8px; display: grid; gap: 7px; }
  code { background: #0c0e13; padding: 1px 5px; border-radius: 4px; }
  .badge.mod {
    display: inline-flex; align-items: center; gap: 5px; padding: 2px 8px; border-radius: 6px;
    font-size: 11.5px; line-height: 1.6; background: #1c2436; border: 1px solid var(--border); color: #c7d0e0;
  }
  .mod-vision { border-color: #6d5ae0; color: #c4b8ff; }
  .mod-image { border-color: #c062a0; color: #f3b8e0; }
  .mod-video { border-color: #c0623a; color: #f3c8a8; }
  .mod-audio-stt, .mod-tts { border-color: #4a9c8c; color: #a8f0e0; }
  .mod-embedding { border-color: #b08a3a; color: #f0d8a0; }
</style>
