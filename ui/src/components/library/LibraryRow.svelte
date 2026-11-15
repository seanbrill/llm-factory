<script lang="ts">
  // One model in the Library (sheet frame f11): what it has on this machine —
  // image, container, weights, saved .tar, builds — and the next thing to do.
  // "More" opens everything else: each image, container and file with its own
  // actions (run, logs, build settings, template, download, delete).
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { href } from "../../lib/router";
  import { modMeta } from "../../lib/icons";
  import { gb, engineName, computeName, computeChip, upFor } from "../../lib/format";
  import { progress, phase, timeText } from "../../lib/jobs";
  import { kindLabel, num, ordinal, type LibRow, type LibContainer, type RowActions } from "../../lib/library";
  import { searchName, toolsText } from "../../lib/baked";
  import { homeOf, tildePath } from "../../lib/harness";
  import { live } from "../../lib/live.svelte";
  import type { ImageHarness } from "../../lib/types";

  let {
    row,
    expanded = false,
    flash = false,
    est,
    owui = "",
    gatewayPorts,
    ontoggle,
    act,
  }: {
    row: LibRow;
    expanded?: boolean;
    flash?: boolean;
    est: (c: LibContainer) => { gb: number; gpu: boolean } | null;
    owui?: string;
    gatewayPorts: Set<number>;
    ontoggle: () => void;
    act: RowActions;
  } = $props();

  const rid = $derived("row-" + row.key.replace(/[^a-z0-9-]/gi, "_"));
  const mid = $derived(rid + "-more");
  const running = $derived(row.containers.filter((c) => c.running));
  const stopped = $derived(row.containers.filter((c) => !c.running));
  const job = $derived(row.jobs[0] ?? null);
  const runningJob = $derived(row.jobs.find((j) => j.status === "running") ?? null);
  const img = $derived(row.images[0] ?? null);
  const failed = $derived(row.lastJob?.status === "error" && !row.images.length && !row.jobs.length ? row.lastJob : null);
  const weightsOnly = $derived(row.states.has("weights"));
  const icon = $derived(row.modality === "ensemble" ? "ensembles" : modMeta(row.modality).icon);
  const sub = $derived([kindLabel(row.modality), row.model?.params].filter(Boolean).join(" · "));

  // position = unfinished builds ahead of it on its engine, the running one included.
  function queueChip(): string {
    if (!job) return "";
    if (job.status === "running") return "Building";
    if (job.position <= 0) return "Queued · starting";
    if (job.position === 1) return "Queued · next";
    return `Queued · ${ordinal(job.position + 1)} in line`;
  }

  function memText(c: LibContainer): string {
    const e = est(c);
    return e ? `~${num(e.gb)} GB ${e.gpu ? "GPU " : ""}est.` : "";
  }

  const breakdown = $derived(
    [
      row.disk.weights > 0 ? `weights ${num(row.disk.weights)}` : "",
      row.disk.images > 0 ? `image${row.images.length > 1 ? "s" : ""} ${num(row.disk.images)}` : "",
      row.disk.tars > 0 ? `tar ${num(row.disk.tars)}` : "",
    ].filter(Boolean),
  );
  /** "Docker · CPU · 3.5 GB", skipping the empty parts. */
  const dots = (...xs: (string | number | false | null | undefined)[]) => xs.filter(Boolean).join(" · ");
  const firstLine = (s?: string) => (s || "").split("\n").find((l) => l.trim())?.trim() ?? "";

  // A baked harness: the tools the image runs itself.
  const toolsTitle = (h: ImageHarness) =>
    h.tools.length ? `Baked tools: ${toolsText(h.tools)}${h.tools.includes("shell") ? " (the shell is on)" : ""}` : "Baked harness with every tool off";
  const toolsLine = (h: ImageHarness) =>
    dots(
      h.tools.length ? `Tools: ${toolsText(h.tools)}` : "Tools: none on",
      h.search && `search with ${searchName(h.search)}`,
      h.max_steps && `up to ${h.max_steps} steps`,
    );
  const home = $derived(homeOf(live.harness?.terminal));
</script>

<article class="row" class:open={expanded} class:flash id={rid} aria-label={row.name}>
  <div class="tr" class:dim={weightsOnly}>
    <div class="mdl">
      <span class="ico"><Icon name={icon} size={16} /></span>
      <div class="mt">
        <button class="nm" type="button" onclick={ontoggle} aria-expanded={expanded} aria-controls={mid} title="Show everything for {row.name}">{row.name}</button>
        <small>{sub}</small>
      </div>
    </div>

    <div class="cell img">
      <span class="cl">Image</span>
      {#if runningJob && !img}
        {@const p = progress(runningJob)}
        <span class="cs"><span class="chip pr">Building</span><span class={computeChip(runningJob.compute)}>{computeName(runningJob.compute)}</span></span>
        <div class="bar" class:busy={p == null} title="{phase(runningJob)} · {timeText(runningJob)}"><i style="width:{Math.round((p ?? 0.3) * 100)}%"></i></div>
        <span class="ph">{phase(runningJob)} · {timeText(runningJob)}</span>
      {:else if job && !img}
        <span class="cs"><span class="chip">{queueChip()}</span><span class={computeChip(job.compute)}>{computeName(job.compute)}</span></span>
        {#if job.wait_seconds > 0}<span class="ph">{timeText(job)}</span>{/if}
      {:else if img}
        <span class="mono" title={img.ref}>{img.repo}{img.ref.endsWith(":latest") ? "" : ":" + img.ref.split(":").pop()}</span>
        <span class="cs">
          <span class="chip">{engineName(img.engine)}</span>
          <span class={computeChip(img.compute)}>{computeName(img.compute)}</span>
          {#if img.harness}<span class="chip tl" class:wa={img.harness.tools.includes("shell")} title={toolsTitle(img.harness)}><Icon name="wrench" size={11} />Tools</span>{/if}
          {#if img.tar}<span class="chip" title="Exported to images/{img.tar.name}">.tar</span>{/if}
          {#if row.images.length > 1}<span class="chip" title={row.images.slice(1).map((i) => i.ref + " (" + engineName(i.engine) + ")").join(", ")}>+{row.images.length - 1} more</span>{/if}
          {#if job}<a class="chip pr" href={href("builds", job.id)} title="A new build of this model is {job.status === "running" ? "running" : "queued"}: open it in Builds">{job.status === "running" ? "Rebuilding" : "Rebuild queued"}</a>{/if}
        </span>
      {:else if row.tars.length}
        <span class="mono" title={row.tars[0].name}>{row.tars[0].name}</span>
        <span>Saved, not loaded into an engine</span>
      {:else if failed}
        <span>No image yet</span>
        <a class="fail" href={href("builds", failed.id)}><Icon name="errc" size={13} />Last build failed · see why</a>
      {:else}
        <span>No image yet</span>
      {/if}
    </div>

    <div class="cell ctr" class:none={!row.containers.length && !img && !job}>
      <span class="cl">Container</span>
      {#if running.length}
        {@const c = running[0]}
        <span class="run"><span class="dot"></span>Running {#if c.port}<span class="mono">:{c.port}</span>{/if}{#if running.length > 1}<span class="chip">+{running.length - 1}</span>{/if}</span>
        <span title={est(c) ? "The factory's estimate for this model from its size, not a live reading." : ""}>{[upFor(c.status), memText(c)].filter(Boolean).join(" · ") || c.status}</span>
      {:else if stopped.length}
        {@const c = stopped[0]}
        <span class="run"><span class="dot er"></span>Stopped</span>
        <span title={c.status}>{c.status.replace(/^Exited \((\d+)\)/, "Exited with code $1,")}</span>
      {:else if job && !img}
        <span>Will run on {engineName(job.engine)}</span>
      {:else if img}
        <span class="run"><span class="dot off"></span>Not running</span>
        {#if img.created}<span>built {img.created}</span>{/if}
      {:else}
        <span>—</span>
      {/if}
    </div>

    <div class="size">
      <span class="cl">On disk</span>
      {gb(row.disk.total)}
      {#if breakdown.length}<small>{#each breakdown as part, i (part)}{#if i}{" · "}{/if}<span class="bd">{part}</span>{/each}</small>{/if}
    </div>

    <div class="acts">
      {#if running.length}
        <a class="btn sm" href={href("playground", String(running[0].port))} title="Try {row.name} in the Playground"><Icon name="playground" size={14} />Playground</a>
        <button class="small" type="button" onclick={() => act.stop(row, running[0])}>Stop</button>
      {:else if img}
        <button class="small primary" type="button" onclick={() => act.run(row, img)}><Icon name="play" size={13} />Run</button>
      {:else if job}
        <a class="btn sm" href={href("builds", job.id)}>{job.status === "running" ? "View build" : "View queue"}</a>
      {:else if row.tars.length}
        <button class="small" type="button" onclick={ontoggle} aria-expanded={expanded} aria-controls={mid}><Icon name="upload" size={14} />Load…</button>
      {:else if row.model}
        <button class="small" type="button" onclick={() => act.build(row)}>{failed ? "Build again" : "Build"}</button>
        {#if row.weightsFile || row.weightsGB > 0}
          <button class="small" type="button" onclick={() => act.deleteWeights(row)}>Delete weights</button>
        {/if}
      {/if}
      {#if !(row.model && weightsOnly && !failed)}
        <button class="small icon more-btn" class:on={expanded} type="button" onclick={ontoggle} aria-expanded={expanded} aria-controls={mid} aria-label="More for {row.name}" title="More for {row.name}"><Icon name="dots" size={15} /></button>
      {/if}
    </div>
  </div>

  {#if expanded}
    <div class="more" id={mid}>
      {#if row.images.length}
        <section>
          <h3 class="lbl">Image{row.images.length > 1 ? "s" : ""}</h3>
          {#each row.images as im (im.engine + im.ref)}
            {@const inUse = row.containers.some((c) => c.running && (c.ref === im.ref || c.ref === im.ref.replace(/^localhost\//, "")))}
            <div class="it">
              <div class="iw">
                <span class="mono">{im.ref}</span>
                <span class="meta">{dots(engineName(im.engine), computeName(im.compute), gb(im.sizeGB), im.created && `built ${im.created}`, im.tar && `saved as images/${im.tar.name} (${gb(im.tar.gb)})`)}</span>
                {#if im.harness}
                  <span class="meta tools">
                    <Icon name="wrench" size={13} />
                    <span>{toolsLine(im.harness)}{#if im.harness.tools.includes("shell")}{" · "}<b class="wa">the shell is on</b>{/if}</span>
                  </span>
                {/if}
              </div>
              <div class="ia">
                <button class="small" type="button" onclick={() => act.run(row, im)}><Icon name="play" size={13} />Run</button>
                <button class="small" type="button" onclick={() => act.config(im)}><Icon name="system" size={14} />Build settings</button>
                <button class="small" type="button" onclick={() => act.template(im)} title="Start a new build with this image's settings"><Icon name="copy" size={14} />Use as template</button>
                <button class="small" type="button" onclick={() => act.download(im)} title="Download the image as a .tar (docker save)"><Icon name="download" size={14} />Download .tar</button>
                <button class="small danger" type="button" disabled={inUse} title={inUse ? "Stop its container first" : "Delete the image and its saved .tar"} onclick={() => act.deleteImage(row, im)}><Icon name="trash" size={14} />Delete</button>
              </div>
            </div>
          {/each}
        </section>
      {/if}

      {#if row.containers.length}
        <section>
          <h3 class="lbl">Container{row.containers.length > 1 ? "s" : ""}</h3>
          {#each row.containers as c (c.engine + c.id)}
            <div class="it">
              <div class="iw">
                <span class="run"><span class="dot" class:er={!c.running}></span><span class="mono">{c.name}</span>{#if c.persona}<span class="chip">{c.persona}</span>{/if}</span>
                <span class="meta">{dots(engineName(c.engine), c.port && `http://localhost:${c.port}`, c.status, c.running && memText(c))}</span>
                {#if c.workspace}
                  <span class="meta tools" title={c.workspace}>
                    <Icon name="folder" size={13} />
                    <span>Workspace: <span class="mono">{tildePath(c.workspace, home)}</span> at /workspace</span>
                  </span>
                {/if}
              </div>
              <div class="ia">
                {#if c.running && c.port}
                  <a class="btn sm" href={href("playground", String(c.port))}><Icon name="playground" size={14} />Open in Playground</a>
                  {#if owui && gatewayPorts.has(c.port)}
                    <a class="btn sm" href={owui} target="_blank" rel="noopener" title="Open WebUI lists this model through the factory's gateway"><Icon name="external" size={14} />Open WebUI</a>
                  {/if}
                {/if}
                <button class="small" type="button" onclick={() => act.logs(row, c)}><Icon name="doc" size={14} />Logs</button>
                <button class="small" class:danger={!c.running} type="button" onclick={() => act.stop(row, c)}><Icon name={c.running ? "stopsq" : "trash"} size={14} />{c.running ? "Stop" : "Remove"}</button>
              </div>
            </div>
          {/each}
        </section>
      {/if}

      {#if row.weightsGB > 0 || row.weightsFile}
        <section>
          <h3 class="lbl">Weights</h3>
          <div class="it">
            <div class="iw">
              <span class="mono">models/{row.model?.file || row.weightsFile?.name}</span>
              <span class="meta">
                {gb(row.weightsGB || row.weightsFile?.gb)} on disk.
                {#if row.images.length}Baked into the image too, so they're only needed to rebuild.{:else}A build reuses them instead of downloading again.{/if}
                {#if row.model?.mmproj_file && !row.model.downloaded}{" The vision projector isn't downloaded yet; the next build gets it."}{/if}
              </span>
            </div>
            <div class="ia">
              {#if row.model}
                <button class="small danger" type="button" disabled={!!row.jobs.length} title={row.jobs.length ? "A build of this model needs them" : "Delete the weights file"} onclick={() => act.deleteWeights(row)}><Icon name="trash" size={14} />Delete weights</button>
              {/if}
            </div>
          </div>
        </section>
      {/if}

      {#if row.tars.length}
        <section>
          <h3 class="lbl">Saved image{row.tars.length > 1 ? "s" : ""}</h3>
          {#each row.tars as t (t.name)}
            <div class="it col">
              <div class="iw">
                <span class="mono">images/{t.name}</span>
                <span class="meta">{gb(t.gb)}, not loaded into an engine. To use it, load it in the factory folder on this Mac; it shows up here as an image.</span>
              </div>
              <CodeLine cmd={`docker load -i "images/${t.name}"`} />
            </div>
          {/each}
        </section>
      {/if}

      {#if row.jobs.length || row.lastJob}
        <section>
          <h3 class="lbl">Builds</h3>
          {#each row.jobs as j (j.id)}
            <div class="it">
              <div class="iw">
                <span>{j.status === "running" ? phase(j) : "Queued"} · {engineName(j.engine)} · {computeName(j.compute)}</span>
                <span class="meta">{timeText(j)}</span>
              </div>
              <div class="ia"><a class="btn sm" href={href("builds", j.id)}>{j.status === "running" ? "View build" : "View queue"}</a></div>
            </div>
          {/each}
          {#if row.lastJob && !row.jobs.length}
            {@const lj = row.lastJob}
            <div class="it">
              <div class="iw">
                <span>
                  {#if lj.status === "error"}<span class="chip er">Failed</span>{:else if lj.status === "done"}<span class="chip ok">Built</span>{:else}<span class="chip">Canceled</span>{/if}
                  Last build · {engineName(lj.engine)} · {computeName(lj.compute)}
                </span>
                {#if lj.status === "error" && lj.error}<span class="meta">{firstLine(lj.error)}</span>{/if}
              </div>
              <div class="ia"><a class="btn sm" href={href("builds", lj.id)}>{lj.status === "error" ? "See why" : "Open"}</a></div>
            </div>
          {/if}
        </section>
      {/if}
    </div>
  {/if}
</article>

<style>
  .row { border-top: 1px solid var(--ln); }
  .row.flash { animation: flash 2.4s ease-out; }
  @keyframes flash {
    0%, 30% { background: var(--prs); box-shadow: inset 3px 0 0 var(--pr); }
    100% { background: transparent; box-shadow: inset 3px 0 0 transparent; }
  }
  .row.open { background: color-mix(in srgb, var(--ink) 2%, var(--p1)); }
  .tr {
    display: grid;
    grid-template-columns: var(--cols);
    gap: 16px;
    align-items: center;
    padding: 11px 18px;
    min-height: 62px;
  }
  .mdl { display: flex; align-items: center; gap: 11px; min-width: 0; }
  .ico {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .mt { min-width: 0; display: flex; flex-direction: column; align-items: flex-start; }
  .nm {
    all: unset;
    display: block;
    max-width: 100%;
    font-weight: 600;
    font-size: 13.5px;
    color: var(--ink);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    cursor: pointer;
    border-radius: 4px;
  }
  .nm:hover { text-decoration: underline; text-underline-offset: 2px; }
  .nm:focus-visible { outline: 2px solid var(--pr); outline-offset: 2px; }
  .mt small { color: var(--mu); font-size: 12px; }
  .tr.dim .nm { color: var(--mu); }
  .cell { font-size: 12.5px; color: var(--mu); display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .cell > span { min-width: 0; }
  .cell .mono { color: var(--ink); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .cs { display: flex; gap: 5px; align-items: center; flex-wrap: wrap; }
  a.chip { text-decoration: none; }
  a.chip:hover { text-decoration: underline; text-underline-offset: 2px; }
  .bar { width: min(150px, 100%); height: 5px; }
  .ph { font-size: 11.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .fail { display: inline-flex; align-items: center; gap: 5px; color: var(--er); font-size: 12px; }
  .run { display: flex; align-items: center; gap: 8px; color: var(--ink); font-weight: 500; font-size: 13px; min-width: 0; }
  .run .mono { color: var(--ink); font-size: 12px; }
  .size { font-family: var(--font-mono); font-size: 12.5px; display: flex; flex-direction: column; gap: 2px; white-space: nowrap; min-width: 0; }
  .size small { font-family: var(--font-body); color: var(--mu); font-size: 11.5px; white-space: normal; line-height: 1.35; }
  .bd { white-space: nowrap; }
  .acts { display: flex; gap: 6px; justify-content: flex-end; flex-wrap: wrap; }
  .more-btn.on { background: var(--p3); border-color: var(--ln2); color: var(--ink); }
  .cl { display: none; }

  /* Everything for the model, one line per thing it has. */
  .more { padding: 2px 18px 16px 59px; display: flex; flex-direction: column; gap: 14px; }
  .more section { display: flex; flex-direction: column; gap: 6px; }
  .more h3 { margin: 0; }
  .it {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--ln);
    border-radius: var(--r);
    background: var(--p1);
  }
  .it.col { flex-direction: column; align-items: stretch; }
  .iw { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1; }
  .iw .mono { font-size: 12px; overflow-wrap: anywhere; }
  .iw .chip { margin-right: 6px; }
  .meta { color: var(--mu); font-size: 12.5px; }
  .meta.tools { display: grid; grid-template-columns: 13px minmax(0, 1fr); gap: 6px; }
  .meta.tools :global(.ico) { margin-top: 2px; color: var(--fa); }
  .meta.tools .mono { font-size: 12px; color: var(--ink); }
  .meta b.wa { color: var(--wa); font-weight: 600; }
  .chip.tl { gap: 4px; }
  .ia { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }

  /* Narrow desktop: image and container share a column. */
  @media (max-width: 1240px) {
    .tr { grid-template-areas: "m i s a" "m c s a"; row-gap: 6px; }
    .mdl { grid-area: m; }
    .img { grid-area: i; }
    .ctr { grid-area: c; }
    .size { grid-area: s; }
    .acts { grid-area: a; }
    /* Each on one line when it fits: the ref then its chips, the state then its uptime. */
    .img, .ctr { flex-direction: row; flex-wrap: wrap; align-items: center; gap: 4px 8px; }
    .ctr.none { display: none; }
    .more { padding-left: 18px; }
  }

  /* Phone: a card per model. */
  @media (max-width: 760px) {
    .tr {
      grid-template-columns: minmax(0, 1fr);
      grid-template-areas: "m" "i" "c" "s" "a";
      gap: 8px;
      padding: 14px;
    }
    .nm { white-space: normal; }
    .cell, .size { display: grid; grid-template-columns: 74px minmax(0, 1fr); column-gap: 8px; row-gap: 2px; align-items: center; }
    .cell > :not(.cl), .size > :not(.cl) { grid-column: 2; }
    .cl { display: block; grid-row: 1 / span 3; align-self: start; padding-top: 1px; font-size: 11px; letter-spacing: 0.06em; text-transform: uppercase; color: var(--fa); }
    .size { font-size: 12px; }
    .acts { justify-content: flex-start; padding-top: 2px; }
    .more { padding: 0 14px 14px; }
    .it { flex-direction: column; align-items: stretch; }
    .ia { justify-content: flex-start; }
  }
</style>
