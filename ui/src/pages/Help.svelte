<script lang="ts">
  import { modMeta } from "../lib/icons";
  import Icon from "../components/Icon.svelte";

  const mods: [string, string][] = [
    ["text", "Chat, writing, Q&A, reasoning — the everyday all-rounder."],
    ["vision", "Understands images you send it (describe a photo, read a chart). Does NOT create images."],
    ["image", "Generates images from a text prompt."],
    ["video", "Generates short video clips from a prompt (length selectable in chat)."],
    ["audio-stt", "Transcribes speech to text (attach an audio file)."],
    ["tts", "Speaks text aloud — plays automatically in chat."],
    ["embedding", "Turns text into vectors for search/RAG — returns numbers, not replies."],
  ];
  const steps = [
    ["Build", "wrench", "Pick a model, engine, and compute. The picker shows how well each model fits your hardware. Build packages it into a self-contained image."],
    ["Images", "box", "Your built images. Click Run to start one on a port — the resource bar warns if it won't fit alongside what's already running."],
    ["Chat", "chat", "Talk to a running model in your browser — text, images, audio, or video, depending on its modality."],
  ];
</script>

<div class="card help">
  <h2>What this is</h2>
  <p>
    LLM Factory turns an open AI model into a <b>self-contained container image</b> you can
    run anywhere — then build, run, and use it entirely on your own machine. No cloud, no API keys.
  </p>

  <h2>First steps</h2>
  <ol class="steps">
    {#each steps as [title, icon, desc] (title)}
      <li><span class="step-h"><Icon name={icon} size={15} /> {title}</span><span class="step-d">{desc}</span></li>
    {/each}
  </ol>

  <h2>Modalities</h2>
  <p class="sub">What a model can do is set by its modality — the factory picks the right inference engine for each.</p>
  <ul class="mods">
    {#each mods as [m, desc] (m)}
      <li><span class="badge mod mod-{m}"><Icon name={modMeta(m).icon} size={13} /> {modMeta(m).label}</span><span class="mdesc">{desc}</span></li>
    {/each}
  </ul>

  <h2>Runtimes — C++ vs Python</h2>
  <p>
    Most models run on our <b>C++/GGUF stack</b> (llama.cpp, stable-diffusion.cpp): tiny images,
    quantized weights, and CPU-offload — which is how a modest GPU runs big models. A few models with
    no C++ port use the <b>Python/PyTorch runtime</b> instead (bigger image, no quantization, a hard
    VRAM floor). The Build picker labels which is which and rates each honestly for your hardware.
  </p>

  <h2>Resources &amp; safety</h2>
  <p>
    The Containers and Images pages show a live <b>VRAM / RAM budget</b>. When you Run a model, the
    factory <b>blocks a start that would over-subscribe</b> your memory and crash — you can still
    force it. (Usage from apps outside the factory isn't counted, so "free" is an upper bound.)
  </p>

  <h2>Personas &amp; Ensembles</h2>
  <p>
    <b>Personas</b> are named system prompts you reuse — bake one into a build or apply it at Run time.
    <b>Ensembles</b> combine several built images into one multimodal super-model: a tiny <b>Conductor</b>
    routes each request to the right specialist and starts/stops them to fit your VRAM budget.
  </p>

  <h2>CPU vs GPU</h2>
  <p>
    Compute (CPU / CUDA / Vulkan) is <b>baked at build time</b> — each is a different image. GPU doesn't
    change a model's <i>quality</i>, only its <i>speed</i>; a bigger model is what raises quality, and the
    GPU is what makes a bigger model usable.
  </p>
</div>

<style>
  .help { max-width: 780px; }
  .help h2 { font-size: 15px; margin: 20px 0 6px; }
  .help h2:first-child { margin-top: 0; }
  .help p { color: #c4cdde; font-size: 13.5px; line-height: 1.55; margin: 0 0 8px; }
  .sub { color: var(--muted); font-size: 12.5px; margin-bottom: 8px; }
  .steps { margin: 0 0 8px; padding: 0; list-style: none; display: grid; gap: 8px; }
  .steps li { display: flex; flex-direction: column; gap: 2px; border-left: 2px solid #2c3a57; padding: 2px 0 2px 12px; }
  .step-h { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; font-size: 13.5px; }
  .step-h :global(.ico) { color: var(--accent); }
  .step-d { color: var(--muted); font-size: 12.5px; line-height: 1.5; }
  .mods { list-style: none; padding: 0; margin: 0 0 8px; display: grid; gap: 8px; }
  .mods li { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .mdesc { color: var(--muted); font-size: 12.5px; }
  .badge.mod {
    display: inline-flex; align-items: center; gap: 5px; padding: 2px 8px; border-radius: 6px; flex: none;
    font-size: 11.5px; line-height: 1.6; background: #1c2436; border: 1px solid var(--border); color: #c7d0e0; min-width: 116px;
  }
  .mod-vision { border-color: #6d5ae0; color: #c4b8ff; }
  .mod-image { border-color: #c062a0; color: #f3b8e0; }
  .mod-video { border-color: #c0623a; color: #f3c8a8; }
  .mod-audio-stt, .mod-tts { border-color: #4a9c8c; color: #a8f0e0; }
  .mod-embedding { border-color: #b08a3a; color: #f0d8a0; }
</style>
