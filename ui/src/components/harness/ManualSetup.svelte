<script lang="ts">
  // Setting Open WebUI up by hand: the same four things Connect does, with the
  // values to paste (docs/HARNESS.md, Manual setup).
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import type { HarnessStatus } from "../../lib/harness";

  let { st }: { st: HarnessStatus } = $props();
  const uid = $props.id();
</script>

<section class="card" aria-labelledby="{uid}-title">
  <details>
    <summary>
      <Icon name="chevronRight" size={15} />
      <h2 id="{uid}-title">Set it up by hand</h2>
      <span class="sub">The same steps as Connect, with the values to paste</span>
    </summary>

    <ol class="steps">
      <li>
        <b>Models.</b> Admin Panel → Settings → Connections → OpenAI API → <b>+</b>. Optionally set the connection type to
        <i>Local</i>.
        <div class="vals">
          <span class="k">URL</span>
          <span class="code"><span>{st.gateway.base_url || "—"}</span>{#if st.gateway.base_url}<CopyButton text={st.gateway.base_url} title="Copy the gateway address" />{/if}</span>
          <span class="k">Key</span>
          <span class="code"><span>the gateway key</span>{#if st.gateway.key}<CopyButton text={st.gateway.key} title="Copy the gateway key" />{/if}</span>
        </div>
      </li>
      <li>
        <b>Terminal.</b> Start the terminal on this page first. Then Admin Panel → Settings → Integrations → Open Terminal →
        <b>+</b>, with auth <i>Bearer</i>. A new terminal connection is admin-only; to share it, grant access in the same
        dialog.
        <div class="vals">
          <span class="k">URL</span>
          <span class="code"><span>{st.terminal.url || "—"}</span>{#if st.terminal.url}<CopyButton text={st.terminal.url} title="Copy the terminal address" />{/if}</span>
          <span class="k">API key</span>
          <span class="code"><span>the terminal key</span>{#if st.terminal.key}<CopyButton text={st.terminal.key} title="Copy the terminal key" />{/if}</span>
        </div>
      </li>
      <li>
        <b>Web search.</b> Admin Panel → Settings → Web Search: turn it on and choose an engine. DuckDuckGo needs no key.
      </li>
      <li>
        <b>Chat.</b> Pick a factory model and select the <i>LLM Factory workspace</i> terminal. Turn on Web Search when you
        need it.
      </li>
    </ol>
    <p class="fine">
      Open WebUI 0.13 uses native tool calling by default. If a model's or a chat's <i>Function Calling</i> setting is
      <i>legacy</i>, set it back to the default. More detail, the security model and troubleshooting are in
      <span class="mono">docs/HARNESS.md</span>.
    </p>
  </details>
</section>

<style>
  .card { padding: 0; }
  summary {
    list-style: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 18px;
    border-radius: var(--rl);
    flex-wrap: wrap;
  }
  summary::-webkit-details-marker { display: none; }
  summary :global(.ico) { color: var(--mu); transition: transform 0.15s ease; }
  details[open] summary :global(.ico) { transform: rotate(90deg); }
  summary h2 { font-size: 15px; margin: 0; }
  summary .sub { font-size: 12.5px; }
  summary:hover h2 { color: var(--pr); }

  .steps {
    margin: 0;
    padding: 0 18px 0 42px;
    font-size: 13px;
    line-height: 1.6;
    color: var(--mu);
  }
  .steps li + li { margin-top: 10px; }
  .steps b { color: var(--ink); font-weight: 600; }
  .vals {
    display: grid;
    grid-template-columns: max-content minmax(0, 420px);
    gap: 6px 10px;
    align-items: center;
    margin-top: 6px;
  }
  .k { font-size: 12px; color: var(--fa); }
  .vals .code { padding: 3px 4px 3px 10px; font-size: 11.5px; }
  .vals .code > span { overflow: hidden; text-overflow: ellipsis; }
  .fine { color: var(--fa); font-size: 12px; line-height: 1.5; padding: 12px 18px 16px 42px; }
  .fine .mono { font-size: 11.5px; color: var(--mu); }

  @media (max-width: 760px) {
    summary { padding: 14px 16px; }
    .steps { padding: 0 16px 0 34px; }
    .fine { padding: 12px 16px 16px 34px; }
    .vals { grid-template-columns: max-content minmax(0, 1fr); }
  }
</style>
