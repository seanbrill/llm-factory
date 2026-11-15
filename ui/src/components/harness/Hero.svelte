<script lang="ts">
  // The harness at a glance (sheet frame f12, top): what it gives a model, the
  // four parts with their state, and the one thing to do next.
  import Icon from "../Icon.svelte";
  import { href } from "../../lib/router";
  import { plural } from "../../lib/format";
  import {
    terminalView,
    startLabel,
    skillCounts,
    reportVerdict,
    whenText,
    browserURL,
    type HarnessStatus,
    type Tone,
  } from "../../lib/harness";
  import { hs, start, jump, memoCurrent } from "./state.svelte";

  let { st }: { st: HarnessStatus } = $props();

  interface Step {
    key: string;
    title: string;
    hint: string;
    tone: Tone;
    target: string;
    focus?: string;
  }

  const tv = $derived(terminalView(st.terminal));
  const current = $derived(memoCurrent(st));
  const verdict = $derived(hs.memo ? reportVerdict(hs.memo.report) : null);
  const owuiOK = $derived(!!verdict && verdict.tone === "ok" && current);

  const steps = $derived.by((): Step[] => {
    const n = st.gateway.models?.length ?? 0;
    const gw: Step = st.gateway.error
      ? { key: "gw", title: "Gateway", hint: "Can't list the running models", tone: "err", target: "h-gateway" }
      : n
        ? { key: "gw", title: "Gateway", hint: `${plural(n, "model")} running`, tone: "ok", target: "h-gateway" }
        : { key: "gw", title: "Gateway", hint: "No models running yet", tone: "idle", target: "h-gateway" };

    const term: Step = {
      key: "term",
      title: "Workspace terminal",
      hint: st.terminal.restart_required && st.terminal.container?.running && !st.terminal.op?.active
        ? "Restart it to apply new settings"
        : tv.hint,
      tone: st.terminal.restart_required && tv.tone === "ok" ? "warn" : tv.tone,
      target: "h-terminal",
    };

    const sc = skillCounts(st.skills);
    const sk: Step = st.skills.error
      ? { key: "sk", title: "Skills", hint: "The skills folder isn't visible", tone: "warn", target: "h-skills" }
      : sc.found
        ? {
            key: "sk",
            title: "Skills",
            hint: sc.fix
              ? `${sc.found} found, ${sc.fix} need${sc.fix === 1 ? "s" : ""} a fix`
              : sc.found === 1
                ? "1 found, ready"
                : `${sc.found} found, all ready`,
            tone: sc.fix ? "warn" : "ok",
            target: "h-skills",
          }
        : { key: "sk", title: "Skills", hint: "None yet", tone: "idle", target: "h-skills" };

    let ow: Step;
    if (!hs.memo) {
      ow = { key: "ow", title: "Open WebUI", hint: "Not set up from here yet", tone: "idle", target: "h-owui", focus: "input[type=password]" };
    } else if (!current) {
      ow = { key: "ow", title: "Open WebUI", hint: "Out of date · set it up again", tone: "warn", target: "h-owui", focus: "input[type=password]" };
    } else if (verdict!.tone === "ok") {
      ow = {
        key: "ow",
        title: "Open WebUI",
        hint: `${hs.memo.kind === "connect" ? "Set up" : "Checked"} ${whenText(hs.memo.at)}`,
        tone: "ok",
        target: "h-owui",
      };
    } else {
      ow = { key: "ow", title: "Open WebUI", hint: verdict!.chip, tone: verdict!.tone, target: "h-owui", focus: "input[type=password]" };
    }
    return [gw, term, sk, ow];
  });

  // The first part that isn't done is the next one.
  const nextKey = $derived(steps.find((s) => s.tone !== "ok")?.key ?? "");

  const termRunning = $derived(!!st.terminal.container?.running);
  const nModels = $derived(st.gateway.models?.length ?? 0);
  const owuiLink = $derived(browserURL(hs.memo?.url || st.openwebui.url || ""));
</script>

<section class="card hero" aria-labelledby="h-hero-title">
  <div class="intro">
    <h2 id="h-hero-title">Tools for your models in Open WebUI</h2>
    <p>
      Your local models can search the web, read and edit files in a workspace folder, run commands in a sandbox,
      and follow skills. The models and the terminal run on this computer.
    </p>
  </div>

  <div class="cta">
    {#if st.terminal.op?.active}
      <button class="primary" disabled><span class="spin dark" aria-hidden="true"></span>Starting the terminal…</button>
    {:else if !termRunning && st.terminal.spec_error}
      <button class="primary" onclick={() => jump("h-terminal")}><Icon name="warn" size={15} />Fix the terminal settings</button>
    {:else if !termRunning}
      <button class="primary" onclick={() => start(false)} disabled={!!hs.busy}>
        {#if hs.busy === "start"}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="play" size={14} />{/if}
        {startLabel(st.terminal)}
      </button>
    {:else if st.terminal.restart_required}
      <button class="primary" onclick={() => start(false)} disabled={!!hs.busy}>
        {#if hs.busy === "start"}<span class="spin dark" aria-hidden="true"></span>{:else}<Icon name="retry" size={15} />{/if}
        Restart the terminal
      </button>
    {:else if !owuiOK}
      <button class="primary" onclick={() => jump("h-owui", "input[type=password]")}><Icon name="plug" size={15} />Set up Open WebUI</button>
    {:else if !nModels}
      <a class="btn pri" href={href("library")}><Icon name="library" size={15} />Run a model</a>
    {:else if owuiLink}
      <a class="btn pri" href={owuiLink} target="_blank" rel="noreferrer"><Icon name="external" size={15} />Open Open WebUI</a>
    {/if}
  </div>

  <ol class="steps" aria-label="Setup">
    {#each steps as s, i (s.key)}
      <li>
        <button class="step" class:next={s.key === nextKey} onclick={() => jump(s.target, s.focus)}>
          <span class="n {s.tone}" aria-hidden="true">
            {#if s.tone === "ok"}<Icon name="check" size={13} stroke={2.6} />
            {:else if s.tone === "busy"}<span class="spin"></span>
            {:else if s.tone === "err"}<Icon name="xmark" size={12} stroke={2.6} />
            {:else if s.tone === "warn"}<Icon name="warn" size={13} stroke={2.2} />
            {:else}{i + 1}{/if}
          </span>
          <span class="tx">
            <b>{s.title}</b>
            <small>{s.hint}</small>
          </span>
          <span class="sr">{s.tone === "ok" ? "done" : s.tone === "err" ? "needs a fix" : s.tone === "warn" ? "needs you" : s.tone === "busy" ? "in progress" : "to do"}</span>
        </button>
      </li>
    {/each}
  </ol>
</section>

<style>
  .hero {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 18px;
    align-items: center;
    padding: 20px 22px;
    background: linear-gradient(120deg, color-mix(in srgb, var(--pr) 12%, var(--p1)), var(--p1) 55%);
  }
  .intro h2 { font-size: 20px; letter-spacing: -0.01em; margin: 0; }
  .intro p { color: var(--mu); font-size: 13.5px; margin-top: 4px; max-width: 640px; }
  .cta { display: flex; justify-content: flex-end; }
  .spin.dark { border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); width: 14px; height: 14px; }
  .steps {
    grid-column: 1 / -1;
    list-style: none;
    margin: 4px 0 0;
    padding: 0;
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
  }
  .step {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: flex-start;
    gap: 10px;
    padding: 10px 12px;
    border-radius: 11px;
    background: var(--p2);
    border: 1px solid var(--ln);
    text-align: left;
    white-space: normal;
    font-weight: 400;
  }
  .step:hover { background: var(--p3); border-color: var(--ln2); }
  .step.next { border-color: color-mix(in srgb, var(--pr) 50%, var(--ln)); }
  .n {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    font-size: 12px;
    font-weight: 700;
    flex: none;
    background: var(--prs);
    color: var(--pr);
  }
  .n.ok { background: var(--oks); color: var(--ok); }
  .n.warn { background: var(--was); color: var(--wa); }
  .n.err { background: var(--ers); color: var(--er); }
  .n.busy { background: var(--prs); }
  .n .spin { width: 13px; height: 13px; }
  .tx { display: flex; flex-direction: column; min-width: 0; }
  .tx b { font-size: 13px; font-weight: 600; color: var(--ink); }
  .tx small { color: var(--mu); font-size: 12px; line-height: 1.45; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }

  @media (max-width: 1100px) {
    .steps { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  @media (max-width: 760px) {
    .hero { grid-template-columns: minmax(0, 1fr); padding: 18px 16px; gap: 14px; }
    .intro h2 { font-size: 18px; }
    .cta { justify-content: stretch; }
    .cta > :global(*) { flex: 1; }
    .steps { grid-template-columns: minmax(0, 1fr); gap: 8px; }
  }
</style>
