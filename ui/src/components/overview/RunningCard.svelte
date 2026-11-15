<script lang="ts">
  // Running now (sheet frame f1): every running model with its port, compute,
  // engine, uptime and the factory's memory estimate; try it in the
  // Playground, stop it, or open it in the Library. Ends with the one gateway
  // link Open WebUI needs.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import { post } from "../../lib/api";
  import { catalog, resources, toast } from "../../lib/stores";
  import { live, refresh } from "../../lib/live.svelte";
  import { href } from "../../lib/router";
  import { computeName, engineName, gb } from "../../lib/format";
  import { modMeta } from "../../lib/icons";
  import { runRows, type RunRow } from "../../lib/overview";

  const rows = $derived(runRows(live.containers, $catalog, $resources));
  const gw = $derived(live.harness?.gateway?.base_url || "");
  let stopping = $state<Record<string, boolean>>({});

  async function stop(r: RunRow) {
    if (!confirm(`Stop ${r.name}${r.port ? ` on :${r.port}` : ""}? Its container is removed; the image stays in your Library.`)) return;
    stopping[r.key] = true;
    try {
      await post("/api/stop", { id: r.id, engine: r.engine });
      toast(`Stopped ${r.name}.`);
      refresh("containers", "resources", "harness");
    } catch (e) {
      toast(`Couldn't stop ${r.name}: ${(e as Error).message}`, 7000);
    } finally {
      delete stopping[r.key];
    }
  }
</script>

<div class="card running">
  <div class="hd">
    <h2>Running now</h2>
    {#if rows.length}<span class="chip ok">{rows.length}</span>{/if}
    <div class="sp"><a class="btn sm" href={href("library")}>Library</a></div>
  </div>

  {#if !live.loaded.containers}
    <p class="sub state"><span class="spin"></span>Looking for running models…</p>
  {:else if live.errors.containers && !rows.length}
    <p class="sub state">Couldn't list the running models ({live.errors.containers}). Check that Docker is running.</p>
  {:else if !rows.length}
    <p class="idle">Nothing is running. Start a built model from the <a href={href("library")}>Library</a>.</p>
  {:else}
    <div class="rows">
      {#each rows as r (r.key)}
        <div class="run">
          <div class="nm">
            <span class="dot"></span>
            <a href={href("library", r.ref)} title="Open {r.name} in the Library">{r.name}</a>
            {#if r.modality !== "text"}<span class="chip">{modMeta(r.modality).label}</span>{/if}
          </div>
          <div class="btns">
            {#if r.port}
              <a class="btn sm" href={href("playground", r.port)}><Icon name="playground" size={14} />Try it</a>
            {/if}
            <button class="small icon" onclick={() => stop(r)} disabled={stopping[r.key]} aria-label="Stop {r.name}" title="Stop {r.name}">
              {#if stopping[r.key]}<span class="spin"></span>{:else}<Icon name="stopsq" size={15} />{/if}
            </button>
          </div>
          <p class="meta">
            {#if r.port}<span class="mono">:{r.port}</span>{" · "}{/if}{[computeName(r.compute), engineName(r.engine), r.up].filter(Boolean).join(" · ")}{#if r.memGB != null}<span title="The factory's estimate from the catalog">{` · ~${gb(r.memGB)}`}</span>{/if}
          </p>
        </div>
      {/each}
    </div>
  {/if}

  {#if gw}
    <div class="gw">
      <Icon name="chain" size={15} />
      <span class="gl">One link for Open WebUI: <span class="mono">{gw.replace(/^https?:\/\//, "")}</span></span>
      <CopyButton text={gw} title="Copy the gateway URL" />
    </div>
  {/if}
</div>

<style>
  .running { min-width: 0; display: flex; flex-direction: column; }
  .rows { display: flex; flex-direction: column; }
  .run {
    display: grid;
    grid-template-columns: minmax(0, 1fr) auto;
    gap: 4px 10px;
    padding: 12px 0;
    border-top: 1px solid var(--ln);
  }
  .run:first-child { border-top: 0; padding-top: 2px; }
  .nm { display: flex; align-items: center; gap: 8px; font-weight: 600; min-width: 0; }
  .nm a { color: var(--ink); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta { color: var(--mu); font-size: 12.5px; min-width: 0; }
  .meta .mono { color: var(--ink); font-size: 12px; }
  .btns { grid-row: 1 / 3; grid-column: 2; display: flex; gap: 6px; align-items: center; }
  .idle { color: var(--mu); font-size: 13px; }
  .state { display: flex; align-items: center; gap: 8px; }
  .gw {
    margin-top: 12px;
    padding: 7px 7px 7px 11px;
    border-radius: var(--r);
    background: var(--p2);
    font-size: 12.5px;
    color: var(--mu);
    display: flex;
    gap: 7px;
    align-items: center;
    min-width: 0;
  }
  .gl { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .gw .mono { color: var(--ink); font-size: 12px; }
  .spin { width: 13px; height: 13px; }
</style>
