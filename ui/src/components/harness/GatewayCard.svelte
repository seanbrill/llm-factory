<script lang="ts">
  // The model gateway: one OpenAI-compatible connection for every running model
  // (sheet frame f12). Its address and key for Open WebUI, and what it serves now.
  import Icon from "../Icon.svelte";
  import Field from "./Field.svelte";
  import SecretField from "./SecretField.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { sentence, type HarnessStatus } from "../../lib/harness";
  import { hs, newGatewayKey } from "./state.svelte";

  let { st }: { st: HarnessStatus } = $props();

  const gw = $derived(st.gateway);
  const models = $derived(gw.models ?? []);
</script>

<section class="card" id="h-gateway" tabindex="-1" aria-labelledby="h-gw-title">
  <div class="hd">
    <h2 id="h-gw-title">Gateway</h2>
    {#if gw.error}<span class="chip wa">Can't list models</span>{:else}<span class="chip ok">On</span>{/if}
  </div>
  <p class="sub">One OpenAI-compatible connection for every running model. Open WebUI lists each model by name.</p>

  <Field label="Address, as Open WebUI reaches it" value={gw.base_url || "—"}>
    {#if gw.base_url}<CopyButton text={gw.base_url} label="Copy" title="Copy the gateway address" />{/if}
  </Field>
  <SecretField label="Key" secret={gw.key} what="gateway key" onnew={newGatewayKey} busy={hs.busy === "gwkey"} />

  {#if hs.error && hs.errorFrom === "gwkey"}
    <div class="callout er" role="alert">
      <Icon name="errc" size={16} />
      <span>
        <b>No new key was made.</b>
        <p>{sentence(hs.error)}</p>
        <button class="small ghost dismiss" onclick={() => (hs.error = "")}>Dismiss</button>
      </span>
    </div>
  {/if}

  {#if gw.error}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span><b>The gateway can't see the model containers.</b><p>{sentence(gw.error)}</p></span>
    </div>
  {/if}

  {#if !gw.error}
  <div class="serving">
    <p class="lbl">Serving now</p>
    {#if models.length}
      <ul>
        {#each models as m (m.id)}
          <li>
            <a class="mid mono" href={href("playground", String(m.port))} title="Try {m.name || m.id} in the Playground">{m.id}</a>
            <span class="chip">{modMeta(m.modality).label}</span>
            {#if m.persona}<span class="per" title="Persona">{m.persona}</span>{/if}
            <span class="port mono">:{m.port}</span>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="none">
        No models are running. <a href={href("library")}>Run one from the Library</a> and Open WebUI lists it right away.
        Chat, code, reasoning, vision and embedding models are served.
      </p>
    {/if}
  </div>
  {/if}

  {#if gw.local_url}
    <p class="local">
      From this computer: <span class="mono">{gw.local_url}</span>
      <CopyButton text={gw.local_url} title="Copy the local address" />
    </p>
  {/if}
</section>

<style>
  .card { display: flex; flex-direction: column; min-width: 0; }
  .card:focus { outline: none; }
  .card:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .hd { margin-bottom: 8px; }
  .callout { margin-top: 12px; }
  .callout button { margin: 6px 0 0 -10px; }
  .serving { margin-top: 16px; }
  .serving .lbl { margin-bottom: 4px; }
  ul { list-style: none; margin: 0; padding: 0; }
  li {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 7px 0;
    border-top: 1px solid var(--ln);
    min-width: 0;
  }
  li:first-child { border-top: 0; }
  .mid {
    color: var(--ink);
    font-weight: 500;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mid:hover { color: var(--pr); }
  .per { color: var(--mu); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .port { margin-left: auto; color: var(--fa); font-size: 12px; }
  .none { color: var(--mu); font-size: 12.5px; line-height: 1.5; }
  .local {
    margin-top: auto;
    padding-top: 14px;
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    color: var(--fa);
    font-size: 12px;
  }
  .local .mono { color: var(--mu); font-size: 11.5px; }
  .local :global(.btn) { min-height: 24px; height: 24px; width: 24px; }
</style>
