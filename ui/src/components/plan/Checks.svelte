<script lang="ts">
  // "Before it starts" (sheet frame f9): the server's preflight for the whole
  // plan — disk space, each engine the plan uses, the weights to download and
  // the builds already waiting — each a check with its fix when it fails.
  import Icon from "../Icon.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { engineName, gb, plural } from "../../lib/format";
  import { href } from "../../lib/router";
  import { memLabel, memoryFixFor, shortName } from "../../lib/catalog";
  import { checkPlan, pre, preflightFor, PREFLIGHT_MAX, type PlanItem } from "../../lib/plan.svelte";
  import type { Model, PreflightItem, SysInfo } from "../../lib/types";

  interface Check {
    key: string;
    tone: "ok" | "wa" | "er" | "mu";
    icon: string;
    title: string;
    text: string;
    cmd?: string;
    link?: { label: string; href: string };
    actions?: { label: string; run: () => void }[];
  }
  interface Row { it: PlanItem; pf: PreflightItem; m: Model | undefined }

  let {
    items,
    byId,
    si,
    onswitch,
  }: {
    items: PlanItem[];
    byId: Map<string, Model>;
    si: SysInfo | null;
    onswitch: (from: string, to: string) => void; // move every build for one engine to the other
  } = $props();

  const res = $derived(pre.result);
  const rows = $derived(
    items
      .map((it) => ({ it, pf: preflightFor(it), m: byId.get(it.model_id) }))
      .filter((r): r is Row => !!r.pf),
  );
  const stale = $derived(rows.length < Math.min(items.length, PREFLIGHT_MAX));

  const nameOf = (r: Row) => (r.m ? shortName(r.m) : r.it.model_id);
  function names(rs: Row[]): string {
    const n = rs.map(nameOf);
    return n.length <= 2 ? n.join(" and ") : `${n.slice(0, -1).join(", ")} and ${n[n.length - 1]}`;
  }
  const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);

  function engineCheck(e: string): Check {
    const read = res?.engines?.[e];
    const sie = si?.engines?.find((x) => x.engine === e);
    const installed = read ? read.installed : !!sie;
    const reachable = read ? read.reachable : !!sie?.reachable;
    const mem = read?.mem_gb || sie?.mem_gb || 0;
    const cpus = sie?.cpus || 0;
    const name = engineName(e);
    const other = e === "podman" ? "docker" : "podman";
    const otherUp = res?.engines?.[other]?.reachable ?? !!si?.engines?.find((x) => x.engine === other)?.reachable;
    const targets = items.filter((it) => it.engine === e);
    const switchTo = targets.length && otherUp
      ? [{ label: `Build ${targets.length === 1 ? "it" : "them"} for ${engineName(other)} instead`, run: () => onswitch(e, other) }]
      : [];

    if (!installed) {
      return {
        key: e, tone: targets.length ? "er" : "mu", icon: "xmark", title: `${name} · not installed`,
        text: targets.length
          ? `${plural(targets.length, "build")} in this plan ${targets.length === 1 ? "is" : "are"} for ${name}, which isn't installed here.`
          : "Not installed here.",
        actions: switchTo,
      };
    }
    if (!reachable) {
      return {
        key: e, tone: "er", icon: "xmark", title: `${name} · not running${mem ? " · " + memLabel(mem) : ""}`,
        text: e === "podman" ? "Start the Podman machine, then check again:" : "Start Docker Desktop, then check again.",
        cmd: e === "podman" ? "podman machine start" : undefined,
        actions: [{ label: "Check again", run: () => checkPlan(0) }, ...switchTo],
      };
    }

    const builds = rows.filter((r) => !r.pf.error && r.pf.build_engine === e);
    const inbound = rows.filter((r) => !r.pf.error && r.pf.engine === e && r.pf.transfer);
    const blocked = rows.filter((r) => r.pf.engine === e && r.pf.run_block);
    const tight = rows.filter((r) => r.pf.engine === e && r.pf.run_note && !r.pf.run_block);
    const lines: string[] = [];
    let tone: Check["tone"] = "ok";
    let icon = "check";
    let cmd: string | undefined;
    if (inbound.length) {
      tone = "wa";
      icon = "bang";
      const from = engineName(inbound[0].pf.build_engine);
      const all = inbound.length === targets.length;
      lines.push(
        `Too small to compile, so ${all ? "the plan builds" : names(inbound) + (inbound.length === 1 ? " builds" : " build")} on ${from} and ${inbound.length === 1 ? "loads the image" : "loads the images"} into ${name}.`,
      );
    }
    if (blocked.length) {
      tone = "er";
      icon = "xmark";
      const need = Math.max(...blocked.map((r) => (r.pf.weights_gb || 0) + 2));
      const fix = memoryFixFor(e, need);
      cmd = fix.cmd;
      lines.push(`To run ${names(blocked)} on the GPU there, ${lower(fix.text).replace(/\.$/, fix.cmd ? ":" : ".")}`);
    } else if (tight.length) {
      if (tone === "ok") { tone = "wa"; icon = "bang"; }
      lines.push(`${names(tight)} ${tight.length === 1 ? "is a tight fit" : "are tight fits"} in ${memLabel(mem)}; the plan says what to expect.`);
    }
    if (!lines.length) {
      const compiling = builds.filter((r) => r.pf.compiles);
      const need = Math.max(0, ...compiling.map((r) => r.pf.need_gb));
      lines.push(
        builds.length
          ? `Builds ${plural(builds.length, "image")} here${need ? `; compiling needs ${need} GB and it has ${memLabel(mem)}` : ""}.`
          : "Ready.",
      );
    }
    return {
      key: e, tone, icon, cmd,
      title: `${name} · ${mem ? memLabel(mem) : "memory unknown"}${cpus ? `, ${cpus} CPUs` : ""}`,
      text: lines.join(" "),
    };
  }

  const checks = $derived.by((): Check[] => {
    if (!res) return [];
    const out: Check[] = [];

    // Disk: the downloads land in models/, and every image is also exported as a .tar.
    const free = res.disk_free_gb;
    const need = res.disk_need_gb || 0;
    if (free == null) {
      out.push({ key: "disk", tone: "mu", icon: "disk", title: "Disk", text: `This plan writes about ${gb(need)} (downloads and exported images). The factory couldn't read the free space.` });
    } else if (need > free) {
      out.push({
        key: "disk", tone: "er", icon: "xmark", title: "Disk",
        text: `${gb(free)} free, but this plan writes about ${gb(need)}. Free up at least ${gb(need - free)} first.`,
        link: { label: "Free up space in the Library", href: href("library") },
      });
    } else if (free - need < 10) {
      out.push({ key: "disk", tone: "wa", icon: "bang", title: "Disk", text: `${gb(free)} free. This plan writes about ${gb(need)}, which leaves ${gb(free - need)}.`, link: { label: "Free up space in the Library", href: href("library") } });
    } else {
      out.push({ key: "disk", tone: "ok", icon: "check", title: "Disk", text: `${gb(free)} free. This plan writes about ${gb(need)} of downloads and images.` });
    }

    // Each engine the plan builds on or for, Docker first.
    const used = [...new Set([...items.map((i) => i.engine), ...rows.map((r) => r.pf.build_engine).filter(Boolean)])].sort((a, b) =>
      a === b ? 0 : a === "docker" ? -1 : b === "docker" ? 1 : a.localeCompare(b),
    );
    for (const e of used) out.push(engineCheck(e));

    // Weights.
    const downloads = rows.filter((r, i) => r.pf.download_gb > 0 && rows.findIndex((x) => x.it.model_id === r.it.model_id) === i);
    if (!downloads.length) {
      const w = rows.reduce((n, r) => n + (r.pf.weights_gb || 0), 0);
      out.push({
        key: "weights", tone: "ok", icon: "check", title: "Weights",
        text: rows.length > 1
          ? `All ${rows.length} are already on disk${w ? ` (${gb(w)})` : ""}. Nothing to download.`
          : `Already on disk${w ? ` (${gb(w)})` : ""}. Nothing to download.`,
      });
    } else {
      const list = downloads.slice(0, 3).map((r) => `${nameOf(r)} (${gb(r.pf.download_gb)})`);
      if (downloads.length > 3) list.push(`${downloads.length - 3} more`);
      out.push({
        key: "weights", tone: "mu", icon: "download", title: "Weights",
        text: `Downloads ${gb(res.download_gb)}: ${list.join(", ")}.${downloads.length < rows.length ? ` The other ${rows.length - downloads.length === 1 ? "one is" : rows.length - downloads.length + " are"} already on disk.` : ""}`,
      });
    }

    // Builds already waiting in the lanes this plan uses.
    const lanes = [...new Set(items.map((i) => i.engine))];
    const waiting = lanes.filter((e) => (res.queued?.[e] ?? 0) > 0);
    if (waiting.length) {
      out.push({
        key: "queue", tone: "mu", icon: "clock", title: "Queue",
        text: waiting.map((e) => `${plural(res.queued[e], "build")} already waiting on ${engineName(e)}`).join("; ") + ". This plan's builds start after them.",
        link: { label: "See the queue", href: href("builds") },
      });
    }

    // What can't start at all.
    const errs = rows.filter((r) => r.pf.error);
    if (errs.length) {
      out.push({
        key: "errors", tone: "er", icon: "xmark", title: `${plural(errs.length, "build")} can't start`,
        text: `${names(errs)}: the plan shows why and how to fix it. Queuing skips ${errs.length === 1 ? "it" : "them"}, and ${errs.length === 1 ? "it stays" : "they stay"} in the plan.`,
      });
    }
    return out;
  });
</script>

<section class="card" aria-labelledby="checks-h" aria-busy={pre.loading}>
  <div class="hd">
    <h2 id="checks-h">Before it starts</h2>
    <span class="sp">
      {#if pre.loading}
        <span class="spin" aria-hidden="true"></span><span class="sr">Checking the plan…</span>
      {:else if res || pre.error}
        <button type="button" class="ghost small icon" onclick={() => checkPlan(0)} aria-label="Check the plan again" title="Check again"><Icon name="refresh" size={15} /></button>
      {/if}
    </span>
  </div>

  {#if pre.error && !res}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span>
        <b>Couldn't check the plan: {pre.error}</b>
        <p>The factory may be restarting. You can still queue the builds; each one checks memory again when it starts.</p>
        <span class="fixes"><button type="button" class="small" onclick={() => checkPlan(0)}>Check again</button></span>
      </span>
    </div>
  {:else if !res}
    <p class="empty"><span class="spin" aria-hidden="true"></span>Checking disk, engines and downloads…</p>
  {:else}
    {#if pre.error}
      <p class="stale">Couldn't check again ({pre.error}); this is the last answer.</p>
    {:else if stale && !pre.loading}
      <p class="stale">Some builds changed since the last check.</p>
    {/if}
    <ul class="checks" class:dim={pre.loading && stale}>
      {#each checks as c (c.key)}
        <li class="ck">
          <span class="mk {c.tone}"><Icon name={c.icon} size={13} stroke={2.4} /></span>
          <span class="bd">
            <b>{c.title}</b>
            <span class="tx">{c.text}</span>
            {#if c.cmd}<CodeLine cmd={c.cmd} />{/if}
            {#if c.link || c.actions?.length}
              <span class="fixes">
                {#each c.actions ?? [] as a (a.label)}<button type="button" class="small" onclick={a.run}>{a.label}</button>{/each}
                {#if c.link}<a href={c.link.href}>{c.link.label}</a>{/if}
              </span>
            {/if}
          </span>
        </li>
      {/each}
    </ul>
    {#if items.length > PREFLIGHT_MAX}
      <p class="stale">Only the first {PREFLIGHT_MAX} builds are checked at a time.</p>
    {/if}
  {/if}
</section>

<style>
  .hd { margin-bottom: 12px; }
  .hd .sp { min-height: 28px; }
  .checks { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 14px; transition: opacity 0.15s; }
  .checks.dim { opacity: 0.6; }
  .ck { display: grid; grid-template-columns: 22px minmax(0, 1fr); gap: 11px; }
  .mk {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--p3);
    color: var(--mu);
  }
  .mk.ok { background: var(--oks); color: var(--ok); }
  .mk.wa { background: var(--was); color: var(--wa); }
  .mk.er { background: var(--ers); color: var(--er); }
  .bd { display: flex; flex-direction: column; gap: 3px; min-width: 0; font-size: 13px; }
  .bd b { font-weight: 600; }
  .tx { color: var(--mu); line-height: 1.45; }
  .bd :global(.code) { margin-top: 7px; align-items: flex-start; }
  /* The whole command, wrapped, in a narrow column. */
  .bd :global(.code > span) { white-space: normal; overflow-wrap: anywhere; }
  .fixes { display: flex; flex-wrap: wrap; align-items: center; gap: 6px 12px; margin-top: 7px; font-size: 12.5px; }
  .empty { display: flex; align-items: center; gap: 10px; padding: 4px 0; }
  .stale { font-size: 12px; color: var(--fa); margin-bottom: 10px; }
  .callout .fixes { margin-top: 8px; }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
  }
</style>
