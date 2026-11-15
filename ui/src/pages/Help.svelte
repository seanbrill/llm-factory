<script lang="ts">
  // Help & concepts (#help, "?" from anywhere): what each page is for, the ideas
  // behind the factory in plain words, keyboard shortcuts, and using it from a
  // phone. Every fact here is what the code does (see the sources in comments).
  import Icon from "../components/Icon.svelte";
  import CodeLine from "../components/CodeLine.svelte";
  import { PAGES, href } from "../lib/router";
  import { MODALITY } from "../lib/icons";
  import { sysinfo } from "../lib/stores";
  import { gb } from "../lib/format";

  const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
  const pages = PAGES.filter((p) => p.id !== "help");

  // Least engine memory to compile a runtime (internal/builder compileFloorGB),
  // when the factory reports it. One "GPU" number only when Vulkan and CUDA agree.
  const floors = $derived($sysinfo?.compile_min_gb);
  const gpuFloor = $derived.by(() => {
    const v = floors?.vulkan || 0;
    const c = floors?.cuda || 0;
    return v && c && v !== c ? 0 : v || c;
  });

  // docs/concepts/04-modalities-and-runtimes.md
  const MOD_TEXT: Record<string, string> = {
    text: "Conversation, writing, questions and answers.",
    code: "Programming help and code completion.",
    reasoning: "Thinks step by step before it answers.",
    vision: "Understands images you send it. It doesn't make them.",
    embedding: "Turns text into vectors for search and RAG, instead of replying.",
    image: "Makes images from a prompt.",
    video: "Makes video from a prompt.",
    "audio-stt": "Turns speech in an audio file into text.",
    tts: "Reads text aloud.",
  };

  // App.svelte's key handler and the command palette.
  const KEYS: { keys: string[]; what: string }[] = [
    { keys: [isMac ? "⌘" : "Ctrl", "K"], what: "Search models, pages and commands" },
    { keys: ["/"], what: "The same search" },
    { keys: ["?"], what: "This page" },
    ...PAGES.filter((p) => p.key).map((p) => ({ keys: [p.key!.toUpperCase()], what: p.label })),
    { keys: ["Esc"], what: "Close the search or an editor" },
  ];

  const sections = [
    { id: "help-pages", label: "Pages" },
    { id: "help-concepts", label: "How it works" },
    { id: "help-models", label: "Models" },
    { id: "help-keys", label: "Keyboard" },
    { id: "help-phone", label: "From a phone" },
  ];
  function jump(e: MouseEvent, id: string) {
    e.preventDefault(); // the URL hash is the router's
    const el = document.getElementById(id);
    el?.scrollIntoView({ behavior: "smooth", block: "start" });
    el?.focus({ preventScroll: true });
  }
</script>

<div class="hp">
  <section class="card intro" aria-labelledby="help-title">
    <div>
      <h2 id="help-title">LLM Factory, in plain words</h2>
      <p>
        It turns open AI models into self-contained container images and runs them on this computer — no cloud, no API
        keys. Pick models in the Catalog, build them, then run and try them, or give them tools in Open WebUI.
      </p>
    </div>
    <nav class="toc" aria-label="On this page">
      {#each sections as s (s.id)}<a href="#help" onclick={(e) => jump(e, s.id)}>{s.label}</a>{/each}
    </nav>
  </section>

  <div class="cols">
    <div class="main">
      <!-- Pages -->
      <section class="card" id="help-pages" tabindex="-1" aria-labelledby="help-pages-t">
        <h2 id="help-pages-t">Pages</h2>
        <ul class="pages">
          {#each pages as p (p.id)}
            <li>
              <a href={href(p.id)}>
                <span class="pi" aria-hidden="true"><Icon name={p.icon} size={17} /></span>
                <span class="pt">
                  <b>{p.label}{#if p.key}<span class="kbd">{p.key.toUpperCase()}</span>{/if}</b>
                  <small>{p.blurb}</small>
                </span>
              </a>
            </li>
          {/each}
        </ul>
      </section>

      <!-- Concepts -->
      <section class="card" id="help-concepts" tabindex="-1" aria-labelledby="help-concepts-t">
        <h2 id="help-concepts-t">How it works</h2>
        <dl class="concepts">
          <div>
            <dt><Icon name="layers" size={16} />Engines: Docker and Podman</dt>
            <dd>
              The factory builds and runs models in containers, on Docker, Podman or both. On a Mac or a Windows PC each
              engine runs in its own Linux virtual machine, with its own memory — the top bar and the System page show
              how much each has. A model has to fit in its engine's memory, not just in the computer's.
            </dd>
          </div>
          <div>
            <dt><Icon name="chip" size={16} />Compute: CPU, Vulkan, CUDA</dt>
            <dd>
              Each image is built for one compute. <b>CPU</b> runs anywhere. <b>CUDA</b> uses an NVIDIA GPU. <b>Vulkan</b>
              uses other GPUs, including a Mac's through a Podman machine (out of the box, Docker Desktop can't reach a
              Mac's GPU; on Docker VMM it might, and System's GPU check shows whether it does). Not every model can be built for every compute. A GPU changes speed, not the quality of the
              answers.
            </dd>
          </div>
          <div>
            <dt><Icon name="route" size={16} />Routes: “Docker → Podman”</dt>
            <dd>
              Compiling a model's runtime takes a minimum of engine memory{#if gpuFloor && floors?.cpu}: about
                {gb(gpuFloor, 0)} for a GPU image and {gb(floors.cpu, 0)} for CPU{/if}. When the engine an image is for
              has less, the build compiles on the other engine and moves the finished image across. The build plan and
              the queue show that as “Docker → Podman”.
            </dd>
          </div>
          <div>
            <dt><Icon name="builds" size={16} />The build queue and its lanes</dt>
            <dd>
              Builds wait in a queue with one lane per engine. Each engine builds one image at a time, in order, so the
              next image can reuse the compile before it; Docker and Podman build at the same time. A build still going
              after 2 hours is stopped.
            </dd>
          </div>
          <div>
            <dt><Icon name="shield" size={16} />Preflight</dt>
            <dd>
              Before anything is queued, the build plan checks each model: which engine compiles it, whether it moves
              to another engine afterwards, and what would stop it, such as too little memory.
            </dd>
          </div>
          <div>
            <dt><Icon name="gauge" size={16} />Will it fit?</dt>
            <dd>
              When you run a model, the factory checks that it fits in its engine's memory and next to what's already
              running. If it wouldn't, it says why, and you can still run it anyway.
            </dd>
          </div>
          <div>
            <dt><Icon name="trash" size={16} />Cleanup</dt>
            <dd>
              If the factory stops in the middle of a build, that build's folder — and an image it was moving between
              engines — stays on disk. The factory removes them when it starts again; you can also clear them from
              the Library's “Space nothing is using” card or with “Clean up leftover build folders” from
              {isMac ? "⌘" : "Ctrl+"}K. It won't clean up while a build is running. The same card lists files no image
              uses, which you delete one by one.
            </dd>
          </div>
          <div>
            <dt><Icon name="harness" size={16} />Harness</dt>
            <dd>
              Tools for your models in Open WebUI: one gateway for every running model, a workspace terminal (a
              container that works in one folder you pick), skills, and web search.
              <a href={href("harness")}>Set it up</a>.
            </dd>
          </div>
          <div>
            <dt><Icon name="personas" size={16} />Personas</dt>
            <dd>
              Named system prompts, and optionally a voice: a speech model's built-in voice or one cloned from a clip
              you record. Bake one into an image when you build it, apply one when you run an image, or give one to an
              ensemble. <a href={href("personas")}>Your personas</a>.
            </dd>
          </div>
          <div>
            <dt><Icon name="ensembles" size={16} />Ensembles</dt>
            <dd>
              Several built images behind one model: chat, vision, speech in and out, images and video. A small
              Conductor sends each request to the right member and starts members only when they're needed, within a
              memory budget. With a persona it answers in character and speaks in the persona's voice; ask it out loud
              with the mic in the Playground. <a href={href("ensembles")}>Make one</a>.
            </dd>
          </div>
        </dl>
      </section>

      <!-- Models -->
      <section class="card" id="help-models" tabindex="-1" aria-labelledby="help-models-t">
        <h2 id="help-models-t">Models</h2>
        <p class="sub">What a model can do is its modality; the factory picks the runtime that serves it.</p>
        <ul class="mods">
          {#each Object.entries(MODALITY).filter(([k]) => k !== "ensemble") as [id, m] (id)}
            <li>
              <span class="chip"><Icon name={m.icon} size={13} />{m.label}</span>
              <span>{MOD_TEXT[id] ?? ""}</span>
            </li>
          {/each}
        </ul>
        <h3>Runtimes</h3>
        <p>
          <b>llama.cpp</b> serves chat, code, reasoning, vision and embeddings; <b>stable-diffusion.cpp</b> makes images
          and video; <b>whisper.cpp</b> transcribes speech; <b>Piper</b> and <b>audio.cpp</b> speak. llama.cpp and
          stable-diffusion.cpp run quantized GGUF weights that can spill over to the CPU, which is why a modest GPU runs
          bigger models than you'd expect. A model with no C++ port, such as Kokoro for speech, uses the
          <b>Python/PyTorch</b> runtime instead, with full-size weights that need more memory.
        </p>
      </section>
    </div>

    <aside class="side">
      <section class="card" id="help-keys" tabindex="-1" aria-labelledby="help-keys-t">
        <h2 id="help-keys-t">Keyboard</h2>
        <table class="keys">
          <tbody>
            {#each KEYS as k (k.what)}
              <tr>
                <td>{#each k.keys as key, i (i)}<span class="kbd">{key}</span>{/each}</td>
                <td>{k.what}</td>
              </tr>
            {/each}
          </tbody>
        </table>
        <p class="fine">Single keys are ignored while a field or a button has the focus.</p>
      </section>

      <section class="card" id="help-phone" tabindex="-1" aria-labelledby="help-phone-t">
        <h2 id="help-phone-t">From a phone or another computer</h2>
        <p>
          The factory listens only on this computer (127.0.0.1) unless you start it with <span class="mono">BIND=0.0.0.0</span>:
        </p>
        <CodeLine cmd="BIND=0.0.0.0 ./scripts/macos/start.sh" />
        <p class="fine">
          Linux: the same with <span class="mono">./scripts/linux/start.sh</span>. Windows:
          <span class="mono">.\scripts\windows\start.ps1 -Bind 0.0.0.0</span>.
        </p>
        <p>
          Then open <span class="mono">http://&lt;this computer's address&gt;:8799</span> on the phone (or the port you
          started it on). The pages fit a phone screen. Do this only on a network you trust.
        </p>
      </section>

      <section class="card docs" aria-labelledby="help-docs-t">
        <h2 id="help-docs-t">More in the repository</h2>
        <ul>
          <li><span class="mono">docs/getting-started.md</span> — first build, step by step</li>
          <li><span class="mono">docs/HARNESS.md</span> — the harness, its security and troubleshooting</li>
          <li><span class="mono">docs/ENSEMBLE.md</span> — how ensembles are designed</li>
          <li><span class="mono">docs/concepts/</span> — containers, GPUs on a Mac, models, runtimes</li>
        </ul>
      </section>
    </aside>
  </div>
</div>

<style>
  .hp { display: flex; flex-direction: column; gap: 16px; }
  .intro {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 18px;
    align-items: center;
    padding: 20px 22px;
    background: linear-gradient(120deg, color-mix(in srgb, var(--pr) 10%, var(--p1)), var(--p1) 55%);
  }
  .intro h2 { font-size: 20px; margin: 0; letter-spacing: -0.01em; }
  .intro p { color: var(--mu); font-size: 13.5px; margin-top: 4px; max-width: 680px; line-height: 1.5; }
  .toc { display: flex; gap: 4px; flex-wrap: wrap; justify-content: flex-end; }
  .toc a {
    padding: 5px 10px;
    border-radius: 8px;
    border: 1px solid var(--ln2);
    color: var(--mu);
    font-size: 12.5px;
    font-weight: 500;
    background: var(--p2);
  }
  .toc a:hover { color: var(--ink); text-decoration: none; background: var(--p3); }

  .cols { display: grid; grid-template-columns: minmax(0, 1fr) 360px; gap: 16px; align-items: start; }
  .main, .side { display: flex; flex-direction: column; gap: 16px; min-width: 0; }
  section:focus { outline: none; }
  section:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .card > h2 { font-size: 16px; margin: 0 0 12px; }
  .card p { color: var(--mu); font-size: 13px; line-height: 1.55; }
  .card p + p { margin-top: 8px; }
  .card b { color: var(--ink); font-weight: 600; }
  .sub { margin-bottom: 10px; }
  h3 { font-size: 13.5px; margin: 16px 0 6px; }

  .pages { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
  .pages a {
    display: flex;
    gap: 11px;
    align-items: flex-start;
    padding: 11px 12px;
    border-radius: 11px;
    border: 1px solid var(--ln);
    background: var(--p2);
    color: var(--ink);
    height: 100%;
  }
  .pages a:hover { text-decoration: none; background: var(--p3); border-color: var(--ln2); }
  .pi { width: 32px; height: 32px; border-radius: 9px; background: var(--p3); display: grid; place-items: center; color: var(--pr); flex: none; }
  .pt { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .pt b { display: flex; align-items: center; gap: 8px; font-size: 13.5px; }
  .pt small { color: var(--mu); font-size: 12px; line-height: 1.45; }

  .concepts { margin: 0; display: flex; flex-direction: column; }
  .concepts > div { padding: 11px 0; border-top: 1px solid var(--ln); }
  .concepts > div:first-child { border-top: 0; padding-top: 0; }
  dt { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 13.5px; color: var(--ink); }
  dt :global(.ico) { color: var(--pr); }
  dd { margin: 4px 0 0 24px; color: var(--mu); font-size: 13px; line-height: 1.55; max-width: 86ch; }
  dd b { color: var(--ink); font-weight: 600; }

  .mods { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; font-size: 13px; color: var(--mu); }
  .mods li { display: grid; grid-template-columns: 124px minmax(0, 1fr); gap: 12px; align-items: center; }
  .mods .chip { justify-self: start; }

  .keys { width: 100%; border-collapse: collapse; font-size: 13px; }
  .keys td { padding: 6px 0; border-top: 1px solid var(--ln); color: var(--mu); vertical-align: middle; }
  .keys tr:first-child td { border-top: 0; }
  .keys td:first-child { width: 92px; white-space: nowrap; }
  .keys .kbd + .kbd { margin-left: 4px; }
  .card p.fine { color: var(--fa); font-size: 12px; margin-top: 10px; }
  .side :global(.code) { margin: 8px 0 0; font-size: 11.5px; }
  .side :global(.code > span) { white-space: normal; overflow-wrap: anywhere; }
  .mono { font-size: 12px; color: var(--ink); }
  .docs ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 7px; font-size: 12.5px; color: var(--mu); }
  .docs .mono { font-size: 11.5px; }

  @media (max-width: 1100px) {
    .cols { grid-template-columns: minmax(0, 1fr); }
    .intro { grid-template-columns: minmax(0, 1fr); }
    .toc { justify-content: flex-start; }
  }
  @media (max-width: 760px) {
    .hp { padding-top: 12px; gap: 12px; }
    .intro { padding: 18px 16px; }
    .intro h2 { font-size: 18px; }
    .pages { grid-template-columns: minmax(0, 1fr); }
    .mods li { grid-template-columns: minmax(0, 1fr); gap: 4px; }
    dd { margin-left: 0; }
  }
</style>
