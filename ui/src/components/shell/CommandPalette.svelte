<script lang="ts">
  // ⌘K palette (sheet frame f15): every action from any page. Type a model to
  // build it with the route the factory would pick (↵) or add it to the plan
  // (⌘↵); jump to a running model, a page, or run a command.
  import { tick } from "svelte";
  import Icon from "../Icon.svelte";
  import { ui, closePalette, copyText, stale } from "../../lib/ui.svelte";
  import { PAGES, go } from "../../lib/router";
  import { catalog, sysinfo, toast } from "../../lib/stores";
  import { live, watch, refresh } from "../../lib/live.svelte";
  import { plan, inPlan, addToPlan, quickBuild, defaultsFor, predictRoute } from "../../lib/plan.svelte";
  import { post, labelVal } from "../../lib/api";
  import { containerPort, isRunning, imageRef } from "../../lib/util";
  import { computeName, computeChip, gb } from "../../lib/format";
  import { routeText } from "../../lib/jobs";
  import { PODMAN_MEMORY_CMD } from "../../lib/explain";
  import type { Model } from "../../lib/types";

  interface Row {
    key: string;
    group: string;
    icon: string;
    pre?: string;
    text: string;
    hl?: [number, number];
    note?: string;
    chips?: { t: string; c: string }[];
    kb?: string;
    run: () => unknown;
    alt?: () => unknown;
    score: number;
  }

  let q = $state(ui.paletteSeed);
  let active = $state(0);
  let input: HTMLInputElement | undefined = $state();
  let list: HTMLDivElement | undefined = $state();
  const opener = document.activeElement as HTMLElement | null;

  $effect(() => watch("containers", "files", "images", "harness"));
  $effect(() => {
    input?.focus();
  });

  function close() {
    closePalette();
    opener?.focus?.();
  }

  // Substring match (word starts rank higher), then all-words, then fuzzy.
  function match(text: string, query: string): { score: number; hl?: [number, number] } | null {
    if (!query) return { score: 0 };
    const t = text.toLowerCase();
    const idx = t.indexOf(query);
    if (idx >= 0) {
      const wordStart = idx === 0 || /[\s\-_./(]/.test(t[idx - 1]);
      return { score: 100 - Math.min(idx, 50) + (wordStart ? 40 : 0) + (idx === 0 ? 20 : 0), hl: [idx, idx + query.length] };
    }
    const toks = query.split(/\s+/).filter(Boolean);
    if (toks.length > 1 && toks.every((k) => t.includes(k))) {
      const first = t.indexOf(toks[0]);
      return { score: 60, hl: [first, first + toks[0].length] };
    }
    let i = 0;
    for (const ch of t) {
      if (ch === query[i]) i++;
      if (i === query.length) return { score: 12 };
    }
    return null;
  }

  function onDisk(m: Model): boolean {
    return !!live.files.find((f) => f.id === m.id)?.downloaded;
  }

  function modelRow(m: Model, score: number, hl?: [number, number]): Row {
    const it = plan.items.find((i) => i.model_id === m.id) ?? defaultsFor(m, $sysinfo);
    const pr = predictRoute(m, it, $sysinfo);
    const chips = [{ t: computeName(pr.compute), c: computeChip(pr.compute) }];
    if (pr.blocked) chips.push({ t: "Can't build now", c: "chip wa" });
    else chips.push({ t: routeText(pr.buildEngine, it.engine), c: "chip" });
    const note = inPlan(m.id) ? "in your plan" : m.file ? (onDisk(m) ? "weights on disk" : `${gb(m.size_gb)} download`) : "";
    return {
      key: "build:" + m.id,
      group: "Build",
      icon: "builds",
      pre: "Build ",
      text: m.name,
      hl,
      note,
      chips,
      score,
      run: async () => {
        close();
        await quickBuild(m, $sysinfo);
      },
      alt: () => {
        addToPlan(m, $sysinfo);
        toast(`${m.name} added to the build plan (${plan.items.length}).`);
      },
    };
  }

  const rows = $derived.by((): Row[] => {
    let query = q.trim().toLowerCase();
    let onlyBuild = false;
    if (/^build\s/.test(query)) {
      onlyBuild = true;
      query = query.slice(6).trim();
    }
    const si = $sysinfo;
    const out: Row[] = [];

    // ---- Build: catalog models
    const models: Row[] = [];
    if (query) {
      for (const m of $catalog) {
        const byName = match(m.name, query);
        const byMeta = byName ?? match(`${m.id} ${m.modality ?? "text"} ${m.params} ${m.tier}`, query);
        if (!byMeta) continue;
        if (m.legacy && (!byName || byName.score < 100)) continue;
        models.push(modelRow(m, (byName ? byName.score : byMeta.score / 2) + (m.recommended ? 5 : 0), byName?.hl));
      }
      models.sort((a, b) => b.score - a.score);
    } else {
      // Nothing typed: a few suggestions — recommended models not built or planned yet.
      const built = new Set(live.images.map((im) => labelVal(im.Labels, "local-llm.model")).filter(Boolean));
      for (const m of $catalog) {
        if (!m.recommended || m.legacy || built.has(m.id) || inPlan(m.id)) continue;
        models.push(modelRow(m, 0));
        if (models.length >= 3) break;
      }
    }
    const topModels = models.slice(0, onlyBuild ? 10 : 6);
    out.push(...topModels.map((r) => ({ ...r, group: query ? "Build" : "Suggested builds" })));
    if (query && topModels.length) {
      const first = $catalog.find((m) => "build:" + m.id === topModels[0].key)!;
      if (!inPlan(first.id)) {
        out.push({
          key: "plan:" + first.id,
          group: "Build",
          icon: "plus",
          pre: "Add ",
          text: first.name,
          hl: topModels[0].hl,
          note: "to the build plan",
          kb: "⌘ ↵",
          score: 0,
          run: () => {
            addToPlan(first, si);
            toast(`${first.name} added to the build plan (${plan.items.length}).`);
          },
        });
      }
    }
    if (onlyBuild) return out;

    // ---- In your library: running models, then built images
    const lib: Row[] = [];
    for (const c of live.containers) {
      if (!isRunning(c)) continue;
      const mid = labelVal(c.Labels, "local-llm.model");
      const name = $catalog.find((m) => m.id === mid)?.name || labelVal(c.Labels, "local-llm.ref") || c.Names || "container";
      const port = containerPort(c);
      const hit = match(name, query);
      if (!hit) continue;
      lib.push({
        key: "run:" + (c.ID || c.Names),
        group: "In your library",
        icon: "library",
        text: name,
        hl: hit.hl,
        note: port ? ":" + port : "",
        chips: [{ t: "Running", c: "chip ok" }],
        score: hit.score + 30,
        run: () => {
          close();
          go("playground", port);
        },
      });
    }
    if (query) {
      for (const im of live.images) {
        const r = imageRef(im);
        const hit = match(r.ref, query);
        if (!hit) continue;
        lib.push({
          key: "img:" + (im.Engine ?? "") + r.ref,
          group: "In your library",
          icon: "library",
          text: r.ref,
          hl: hit.hl,
          note: im.Engine === "podman" ? "Podman" : "Docker",
          score: hit.score,
          run: () => {
            close();
            go("library", r.ref);
          },
        });
      }
    }
    lib.sort((a, b) => b.score - a.score);
    out.push(...lib.slice(0, 4));

    // ---- Go to: pages
    const pages: Row[] = [];
    for (const p of PAGES) {
      if (p.id === "plan" && !plan.items.length) continue;
      const hit = match(p.label, query) ?? (query.length > 2 ? match(p.blurb, query) : null);
      if (!hit) continue;
      pages.push({
        key: "page:" + p.id,
        group: "Go to",
        icon: p.icon,
        text: p.id === "plan" ? `Build plan (${plan.items.length})` : p.label,
        hl: match(p.label, query)?.hl,
        note: p.blurb,
        kb: p.key ? p.key.toUpperCase() : undefined,
        score: hit.score,
        run: () => {
          close();
          go(p.id);
        },
      });
    }
    pages.sort((a, b) => b.score - a.score);
    out.push(...(query ? pages.slice(0, 5) : pages));

    // ---- Commands
    const cmds: Row[] = [];
    const cmd = (key: string, icon: string, text: string, run: () => unknown, note = "") => {
      const hit = match(text, query) ?? (note ? match(note, query) : null);
      if (hit) cmds.push({ key: "cmd:" + key, group: "Commands", icon, text, hl: match(text, query)?.hl, note, score: hit.score, run });
    };
    const running = live.containers.filter(isRunning);
    const podman = si?.engines?.find((e) => e.engine === "podman");
    if (stale()) cmd("reload", "retry", "Reload to the new factory build", () => location.reload());
    if (podman?.low_memory || (podman && podman.mem_gb < 12)) {
      cmd("podman", "copy", "Copy the command that gives Podman 12 GB", async () => {
        close();
        toast((await copyText(PODMAN_MEMORY_CMD)) ? "Copied. Run it in a terminal, then restart the factory." : PODMAN_MEMORY_CMD, 7000);
      });
    }
    if (running.length) {
      cmd("stopall", "stopsq", "Stop every running model", async () => {
        close();
        if (!confirm(`Stop and remove ${running.length} running model${running.length > 1 ? "s" : ""}?`)) return;
        const results = await Promise.allSettled(
          running.map((c) => post("/api/stop", { id: c.ID || c.Names, engine: c.Engine || "docker" })),
        );
        const failed = results.filter((r) => r.status === "rejected").length;
        toast(failed ? `Stopped ${running.length - failed}; ${failed} didn't stop.` : "Stopped every running model.");
        refresh("containers", "resources");
      }, "containers");
    }
    cmd("gpu", "chip", "Check GPU access", () => { close(); go("system", "gpu"); }, "vulkan podman docker");
    cmd("archive", "download", "FileFreak archive settings", () => { close(); go("system", "archive"); }, "filefreak upload key mirror huggingface");
    cmd("cleanup", "trash", "Clean up leftover build folders", async () => {
      close();
      try {
        const r = await post<{ removed: string[]; freed_gb: number }>("/api/storage/cleanup", {});
        toast(r.removed.length ? `Removed ${r.removed.length} leftover${r.removed.length > 1 ? "s" : ""}, freed ${gb(r.freed_gb)}.` : "Nothing to clean up.");
        refresh("storage");
      } catch (e) {
        toast((e as Error).message, 6000);
      }
    }, "disk space");
    cmd("reclaim", "bolt", "Free memory held by the engine's cache", async () => {
      close();
      try {
        const r = await post<{ freed_gb: number }>("/api/system/reclaim", {});
        toast(`Freed ${gb(r.freed_gb)} of memory.`);
      } catch (e) {
        toast((e as Error).message, 6000);
      }
    }, "ram reclaim");
    cmd("connect", "harness", "Connect Open WebUI", () => { close(); go("harness", "connect"); }, "harness tools terminal gateway");
    cmd("newens", "ensembles", "New ensemble", () => { close(); go("ensembles", "new"); }, "combine multimodal conductor");
    cmd("newpersona", "personas", "New persona", () => { close(); go("personas", "new"); }, "system prompt");
    const gw = live.harness?.gateway?.base_url;
    if (gw) {
      cmd("gwurl", "chain", "Copy the Open WebUI connection URL", async () => {
        close();
        toast((await copyText(gw)) ? `Copied ${gw}` : gw, 5000);
      }, gw);
    }
    cmds.sort((a, b) => b.score - a.score);
    out.push(...cmds.slice(0, query ? 5 : 8));
    return out;
  });

  // Keep the highlighted row valid as the list changes.
  $effect(() => {
    rows;
    if (active >= rows.length) active = Math.max(0, rows.length - 1);
  });
  $effect(() => {
    q;
    active = 0;
  });

  async function scrollActive() {
    await tick();
    list?.querySelector<HTMLElement>(`[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      active = rows.length ? (active + 1) % rows.length : 0;
      scrollActive();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      active = rows.length ? (active - 1 + rows.length) % rows.length : 0;
      scrollActive();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const r = rows[active];
      if (!r) return;
      if ((e.metaKey || e.ctrlKey) && r.alt) r.alt();
      else r.run();
    }
  }

  function parts(r: Row): [string, string, string] {
    if (!r.hl) return [r.text, "", ""];
    return [r.text.slice(0, r.hl[0]), r.text.slice(r.hl[0], r.hl[1]), r.text.slice(r.hl[1])];
  }
</script>

<div class="scrim" role="presentation" onclick={close}></div>
<div class="pal" role="dialog" aria-modal="true" aria-label="Command palette">
  <div class="q">
    <Icon name="find" size={18} />
    <input
      bind:this={input}
      bind:value={q}
      onkeydown={onKey}
      placeholder="Search models, pages and commands — try “build qwen”"
      aria-label="Search"
      aria-controls="pal-list"
      aria-activedescendant={rows[active] ? "pal-" + active : undefined}
      autocomplete="off"
      spellcheck="false" />
    <span class="kbd">esc</span>
  </div>
  <div class="list" id="pal-list" role="listbox" bind:this={list}>
    {#each rows as r, i (r.key)}
      {#if i === 0 || rows[i - 1].group !== r.group}<p class="gl">{r.group}</p>{/if}
      {@const [a, b, c] = parts(r)}
      <div
        class="it"
        class:on={i === active}
        id={"pal-" + i}
        data-i={i}
        role="option"
        tabindex="-1"
        aria-selected={i === active}
        onmousemove={() => (active = i)}
        onclick={() => r.run()}
        onkeydown={() => {}}>
        <span class="ic"><Icon name={r.icon} size={16} /></span>
        <span class="tx">
          {#if r.pre}{r.pre}{/if}{a}{#if b}<mark>{b}</mark>{/if}{c}
          {#if r.note}<small>{r.note}</small>{/if}
        </span>
        <span class="r">
          {#each r.chips ?? [] as ch (ch.t)}<span class={ch.c}>{ch.t}</span>{/each}
          {#if i === active}<span class="kbd">↵</span>{:else if r.kb}<span class="kbd">{r.kb}</span>{/if}
        </span>
      </div>
    {:else}
      <p class="none">Nothing matches “{q}”.</p>
    {/each}
  </div>
  <div class="foot">
    <span><b>↑↓</b>move</span><span><b>↵</b>run</span><span><b>⌘↵</b>add to plan</span><span><b>?</b>help</span>
  </div>
</div>

<style>
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 80;
    background: color-mix(in srgb, var(--cv) 70%, transparent);
    backdrop-filter: blur(3px);
  }
  .pal {
    position: fixed;
    left: 50%;
    top: min(110px, 12vh);
    transform: translateX(-50%);
    z-index: 81;
    width: min(680px, calc(100vw - 24px));
    max-height: min(640px, calc(100dvh - 140px));
    display: flex;
    flex-direction: column;
    background: var(--p2);
    border: 1px solid var(--ln2);
    border-radius: 18px;
    box-shadow: var(--shadow);
    overflow: hidden;
  }
  .q {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 18px;
    border-bottom: 1px solid var(--ln);
    color: var(--fa);
  }
  .q input {
    flex: 1;
    border: 0;
    background: none;
    box-shadow: none;
    padding: 16px 0;
    font-size: 17px;
    color: var(--ink);
  }
  .q input:focus { box-shadow: none; }
  .list { overflow-y: auto; padding: 4px 8px 8px; flex: 1; min-height: 0; }
  .gl {
    font-size: 11px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
    color: var(--fa);
    padding: 10px 10px 6px;
  }
  .it {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 9px 10px;
    border-radius: 10px;
    font-size: 14px;
    cursor: pointer;
    min-width: 0;
  }
  .it .ic {
    width: 30px;
    height: 30px;
    border-radius: 8px;
    background: var(--p3);
    display: grid;
    place-items: center;
    color: var(--mu);
    flex: none;
  }
  .tx { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .tx small { color: var(--mu); font-size: 12.5px; margin-left: 6px; }
  .it .r { margin-left: auto; display: flex; gap: 6px; align-items: center; flex: none; }
  .it.on {
    background: color-mix(in srgb, var(--pr) 14%, var(--p2));
    box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--pr) 45%, transparent);
  }
  .it.on .ic { background: var(--pr); color: var(--onpr); }
  mark { background: none; color: var(--pr); font-weight: 700; }
  .none { color: var(--mu); padding: 18px 10px; }
  .foot {
    display: flex;
    gap: 16px;
    padding: 10px 18px;
    border-top: 1px solid var(--ln);
    background: var(--p1);
    font-size: 12px;
    color: var(--fa);
    flex-wrap: wrap;
  }
  .foot b { font-family: var(--font-mono); font-weight: 500; color: var(--mu); margin-right: 4px; }
  @media (max-width: 760px) {
    .pal { top: 12px; max-height: calc(100dvh - 100px); }
    .it .r .chip:not(.ok) { display: none; }
    .foot { display: none; }
  }
</style>
