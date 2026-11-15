<script lang="ts">
  // "What models can do" (sheet frame f12): the four tools a model gets in Open
  // WebUI, each with where it stands now. The sheet draws on/off switches; the
  // factory has no per-tool switch, so each row shows its real state instead and
  // says where it's turned on.
  import Icon from "../Icon.svelte";
  import { skillCounts, homeOf, tildePath, type HarnessStatus } from "../../lib/harness";
  import { hs, memoCurrent, jump } from "./state.svelte";

  let { st }: { st: HarnessStatus } = $props();

  const t = $derived(st.terminal);
  const termOK = $derived(!!t.container?.running && t.healthy);
  const termBusy = $derived(!!t.op?.active);
  const sc = $derived(skillCounts(st.skills));
  const ws = $derived(hs.memo?.report.web_search);
  const fresh = $derived(memoCurrent(st));
  const folder = $derived(tildePath(t.workspace_path, homeOf(t)));

  // Open WebUI's engine ids, as its settings name them.
  const ENGINE: Record<string, string> = {
    duckduckgo: "DuckDuckGo",
    google_pse: "Google PSE",
    brave: "Brave",
    searxng: "SearXNG",
    tavily: "Tavily",
    bing: "Bing",
    kagi: "Kagi",
  };
  const engineText = (e: string) => ENGINE[e] ?? e;

  interface Row {
    icon: string;
    title: string;
    hint: string;
    chip: string;
    cls: string;
    target: string;
  }

  const termChip = $derived(
    termBusy ? { chip: "Starting", cls: "pr" } : termOK ? { chip: "Ready", cls: "ok" } : { chip: "Terminal off", cls: "" },
  );

  const rows = $derived.by((): Row[] => [
    {
      icon: "globe",
      title: "Search the web and read pages",
      hint: "Open WebUI's built-in search · DuckDuckGo needs no key",
      ...(ws
        ? ws.enabled
          ? { chip: ws.engine ? `On · ${engineText(ws.engine)}` : "On", cls: fresh ? "ok" : "" }
          : { chip: "Off", cls: "wa" }
        : { chip: "Not checked", cls: "" }),
      target: "h-owui",
    },
    {
      icon: "folder",
      title: "Read and edit files",
      hint: folder ? `Only inside the workspace folder, ${folder}` : "Only inside the workspace folder",
      ...termChip,
      target: "h-terminal",
    },
    {
      icon: "terminal",
      title: "Run commands",
      hint: "In the terminal container, not on this computer",
      ...termChip,
      target: "h-terminal",
    },
    {
      icon: "sparkle",
      title: "Use skills",
      hint: "Instructions the model loads when a task matches",
      ...(st.skills.error
        ? { chip: "Folder missing", cls: "wa" }
        : sc.listed
          ? { chip: `${sc.listed} ready`, cls: termOK ? "ok" : "" }
          : { chip: "None yet", cls: "" }),
      target: "h-skills",
    },
  ]);
</script>

<section class="card" aria-labelledby="h-tools-title">
  <div class="hd">
    <h2 id="h-tools-title">What models can do</h2>
  </div>
  <p class="sub top">In a chat in Open WebUI, once the parts above are set up.</p>

  <ul>
    {#each rows as r (r.title)}
      <li class="tool">
        <span class="ti" aria-hidden="true"><Icon name={r.icon} size={16} /></span>
        <span class="tx">
          <b>{r.title}</b>
          <small>{r.hint}</small>
        </span>
        <button class="chip {r.cls}" onclick={() => jump(r.target)} title="Go to its settings">{r.chip}</button>
      </li>
    {/each}
  </ul>

  <div class="how">
    <p class="lbl">In the chat</p>
    <p>
      Pick a factory model, select the <b>LLM Factory workspace</b> terminal, and turn on <b>Web Search</b> when you want
      it to search. Try:
    </p>
    <p class="prompt">
      “Search the web for the latest llama.cpp release, then write a summary to notes/llama.md in the workspace.”
    </p>
    <p class="fine">
      Models that are good at tool calling work best, such as Qwen 3.x, Gemma 4 or a recent Mistral or Llama. Very small
      models often describe a tool call instead of making it. Slim Open WebUI images can't run DuckDuckGo: pick another
      engine under Admin Panel → Settings → Web Search.
    </p>
  </div>
</section>

<style>
  .card { display: flex; flex-direction: column; min-width: 0; }
  .hd { margin-bottom: 2px; }
  .sub.top { font-size: 12.5px; margin-bottom: 10px; }
  ul { list-style: none; margin: 0; padding: 0; }
  .tool {
    display: grid;
    grid-template-columns: 32px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    padding: 10px 0;
    border-top: 1px solid var(--ln);
  }
  .tool:first-child { border-top: 0; padding-top: 4px; }
  .ti {
    width: 32px;
    height: 32px;
    border-radius: 9px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
  }
  .tx { min-width: 0; }
  .tx b { display: block; font-size: 13.5px; font-weight: 600; color: var(--ink); }
  .tx small { display: block; color: var(--mu); font-size: 12px; overflow-wrap: anywhere; }
  /* The chip is a button to the part's settings. */
  button.chip {
    min-height: 0;
    height: 22px;
    padding: 0 8px;
    border: 0;
    border-radius: 99px;
    font-size: 11.5px;
    font-weight: 600;
  }
  button.chip:hover { filter: brightness(1.15); }
  button.chip:not(.ok):not(.wa):not(.er):not(.pr) { background: var(--p3); color: var(--mu); }
  button.chip.ok { background: var(--oks); color: var(--ok); }
  button.chip.wa { background: var(--was); color: var(--wa); }
  button.chip.pr { background: var(--prs); color: var(--pr); }

  .how { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--ln); font-size: 13px; color: var(--mu); line-height: 1.5; }
  .how .lbl { margin-bottom: 4px; }
  .how b { color: var(--ink); font-weight: 600; }
  .prompt {
    margin-top: 8px;
    padding: 10px 12px;
    border-radius: 10px;
    background: var(--cv);
    border: 1px solid var(--ln);
    font-size: 13px;
    line-height: 1.5;
    color: var(--ink);
  }
  .fine { color: var(--fa); font-size: 12px; margin-top: 8px; line-height: 1.5; }
</style>
