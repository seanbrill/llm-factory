<script lang="ts">
  // What an ensemble is and what its Conductor does (cmd/ensemblegate,
  // internal/builder BuildEnsemble), for the side of the Ensembles page.
  import Icon from "../Icon.svelte";
</script>

<aside class="card how" aria-labelledby="ens-how-title">
  <h2 id="ens-how-title">How an ensemble works</h2>

  <div class="flow" aria-hidden="true">
    <span class="node">Your app</span>
    <Icon name="arrowRight" size={14} />
    <span class="node pr">Conductor</span>
    <Icon name="arrowRight" size={14} />
    <span class="node">Members</span>
  </div>

  <ol>
    <li>
      <b>One model to talk to.</b> It answers OpenAI-style chat, <span class="mono">/v1/audio/transcriptions</span> and
      <span class="mono">/v1/audio/speech</span>, and shows up as one model in the Playground and the factory's /v1
      gateway. Inside, the <b>Conductor</b> sends each request to a member.
    </li>
    <li>
      <b>Routing.</b> Heuristic routing looks at the request: an image goes to vision, “draw…” to image generation,
      “make a video…” to video, “say…” to speech, and the rest to chat. Tool-calling asks a small conductor model
      instead, and falls back to the heuristic. Images and clips come back inside the chat reply.
    </li>
    <li>
      <b>Voice in, voice out.</b> A spoken question goes to speech to text, then chat, then speech: the answer comes
      back in the persona's voice, built-in or cloned from a clip you record.
    </li>
    <li>
      <b>Members start when needed.</b> Each starts the first time a request needs it; “Keep warm” ones start with
      the ensemble. To stay under the memory budget, the Conductor stops the least recently used ones first. The card
      shows each member's state while it runs.
    </li>
    <li>
      <b>Docker only.</b> The Conductor starts members with Docker, so members must be Docker images. Running an
      ensemble gives it the Docker socket for that; stopping it stops its members too.
    </li>
  </ol>

  <p class="fine">
    Build image makes <span class="mono">local-llm/ens-&lt;name&gt;</span> and a <span class="mono">.tar</span> holding
    the Conductor only: members stay separate images, and voice clips are never in it. Everything runs as separate
    containers; one big image with every member inside isn't built yet. More in <span class="mono">docs/ENSEMBLE.md</span>.
  </p>
</aside>

<style>
  .how { display: flex; flex-direction: column; gap: 12px; }
  .how h2 { font-size: 15px; margin: 0; }
  .flow { display: flex; align-items: center; gap: 6px; color: var(--fa); flex-wrap: wrap; }
  .node {
    padding: 5px 10px;
    border-radius: 8px;
    border: 1px solid var(--ln2);
    background: var(--p2);
    font-size: 12px;
    font-weight: 600;
    color: var(--ink);
  }
  .node.pr { background: var(--prs); border-color: color-mix(in srgb, var(--pr) 40%, transparent); color: var(--pr); }
  ol { margin: 0; padding: 0 0 0 18px; display: flex; flex-direction: column; gap: 10px; font-size: 13px; color: var(--mu); line-height: 1.5; }
  ol b { color: var(--ink); font-weight: 600; }
  .fine { color: var(--fa); font-size: 12px; line-height: 1.5; border-top: 1px solid var(--ln); padding-top: 10px; }
  .fine .mono { font-size: 11.5px; color: var(--mu); }
</style>
