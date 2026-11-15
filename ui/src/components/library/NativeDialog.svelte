<script lang="ts">
  // Make a native package (docs/NATIVE.md): the model and its agent harness as
  // one zip for macOS, Linux or Windows that runs without Docker or Podman.
  // Prefilled from the image's build settings (or the catalog's defaults); then
  // the packaging log, and the download when it's done. Closing the dialog
  // doesn't stop the job: the Library lists it until it's finished.
  import Dialog from "./Dialog.svelte";
  import Icon from "../Icon.svelte";
  import { gb } from "../../lib/format";
  import { toolsText, toolsetsOf, DEFAULT_TOOLS } from "../../lib/baked";
  import {
    nativeTargets, nativeSource, startPackage, packageState, cancelPackage, downloadURL, packPercent, startHow, runtimeMB,
    type NativeTarget, type NativeSource, type NativeJobState, type NativeRequest,
  } from "../../lib/native";
  import type { LibImage } from "../../lib/library";

  let {
    open = $bindable(false),
    name = "",
    image = null,
    modelId = "",
    jobId = "",
    onchange,
  }: {
    open?: boolean;
    name?: string;            // the model's display name
    image?: LibImage | null;  // package this image's settings…
    modelId?: string;         // …or the catalog entry's defaults
    jobId?: string;           // or show a job that's already running
    onchange?: () => void;    // a job started or finished
  } = $props();

  type Phase = "loading" | "form" | "job" | "failed";
  let phase = $state<Phase>("loading");
  let loadErr = $state("");
  let targets = $state<NativeTarget[]>([]);
  let src = $state<NativeSource | null>(null);

  let target = $state("");
  let pkgName = $state("");
  let nameEdited = $state(false);
  let ctx = $state(8192);
  let prompt = $state("");
  let always = $state(false);
  let withTools = $state(true);
  let busy = $state(false);
  let formErr = $state("");

  let cur = $state(""); // the job being watched
  let st = $state<NativeJobState | null>(null);
  let lines = $state<string[]>([]);
  let pollErr = $state("");
  let offset = 0;

  let loadedFor = "";
  $effect(() => {
    if (!open) {
      loadedFor = "";
      return;
    }
    const key = jobId ? "j:" + jobId : image ? "i:" + image.engine + image.ref : "m:" + modelId;
    if (loadedFor === key) return;
    loadedFor = key;
    formErr = loadErr = pollErr = "";
    if (jobId) {
      watchJob(jobId);
      return;
    }
    cur = "";
    phase = "loading";
    const from = image ? { ref: image.ref, engine: image.engine } : { model_id: modelId };
    Promise.all([nativeTargets(), nativeSource(from)])
      .then(([t, s]) => {
        if (loadedFor !== key) return;
        targets = t.targets;
        src = s;
        target = t.targets.find((x) => x.ready)?.id ?? "";
        nameEdited = false;
        ctx = s.ctx_size;
        prompt = s.system_prompt;
        always = s.inject_mode === "always";
        withTools = !!s.harness;
        phase = "form";
      })
      .catch((e) => {
        if (loadedFor !== key) return;
        loadErr = (e as Error).message || "The factory didn't answer.";
        phase = "failed";
      });
  });

  // The default name follows the target until it's edited.
  $effect(() => {
    if (!src || nameEdited) return;
    const i = targets.findIndex((t) => t.id === target);
    pkgName = src.default_names[i] ?? "";
  });

  const sel = $derived(targets.find((t) => t.id === target) ?? null);
  const harnessTools = $derived(src?.harness ? toolsetsOf(src.harness.tools ?? DEFAULT_TOOLS) : []);
  const nameOK = $derived(/^[A-Za-z0-9][A-Za-z0-9._-]{0,119}$/.test(pkgName.trim()) && !pkgName.includes("..") && !pkgName.trim().endsWith("."));
  const ctxOK = $derived(Number.isInteger(ctx) && ctx >= 512 && ctx <= 1048576);
  const canMake = $derived(!!src?.capable && !!sel?.ready && nameOK && ctxOK && !busy);

  async function make() {
    if (!src || !sel) return;
    busy = true;
    formErr = "";
    const body: NativeRequest = {
      target: sel.id,
      name: pkgName.trim(),
      ctx_size: ctx,
      system_prompt: prompt,
      inject_mode: always ? "always" : "missing",
      harness: withTools && src.harness ? src.harness : false,
    };
    if (image) {
      body.ref = image.ref;
      body.engine = image.engine;
    } else {
      body.model_id = src.model_id;
    }
    try {
      const r = await startPackage(body);
      watchJob(r.id);
      onchange?.();
    } catch (e) {
      formErr = (e as Error).message || "The factory didn't answer.";
    } finally {
      busy = false;
    }
  }

  function watchJob(id: string) {
    lines = [];
    offset = 0;
    st = null;
    pollErr = "";
    cur = id;
    phase = "job";
  }

  // Follow the job's log until it finishes.
  $effect(() => {
    if (!open || !cur) return;
    const id = cur;
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      try {
        const s = await packageState(id, offset);
        if (stopped) return;
        offset = s.next_offset;
        if (s.lines.length) lines = [...lines, ...s.lines].slice(-500);
        st = s;
        pollErr = "";
        if (!s.active) {
          onchange?.();
          return;
        }
      } catch (e) {
        if (stopped) return;
        pollErr = (e as Error).message || "The factory didn't answer.";
      }
      timer = setTimeout(tick, 1000);
    };
    tick();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  });

  let logEl: HTMLPreElement | undefined = $state();
  $effect(() => {
    void lines.length;
    if (logEl) logEl.scrollTop = logEl.scrollHeight;
  });

  const pct = $derived(packPercent(lines));
  const lineAfter = (prefix: string) => lines.find((l) => l.startsWith(prefix))?.slice(prefix.length).trim() ?? "";
  const zipSum = $derived(lineAfter("SHA-256 of the zip:"));
  const license = $derived(lineAfter("Model license:").replace(/\.$/, "").replace(/\s*\(.*\)$/, ""));
  const shareWarn = $derived(lineAfter("Check the model's license before you share this package:").replace(/\.$/, ""));
  const ready = $derived(lines.find((l) => l.startsWith("Package ready:"))?.match(/\(([\d.]+ GB)\)/)?.[1] ?? "");
  const lastLine = $derived([...lines].reverse().find((l) => l.trim() && l !== "DONE") ?? "Waiting for the job to start…");
  const jobTarget = $derived(targets.find((t) => t.id === st?.target) ?? (st ? { os: st.target.split("-")[0] } : null));

  async function stop() {
    if (!cur) return;
    try {
      await cancelPackage(cur);
    } catch (e) {
      pollErr = (e as Error).message;
    }
  }

  const title = $derived(phase === "job" && st ? `Native package · ${st.target_label}` : "Make a native package");
  const sub = $derived(phase === "job" && st ? `${st.package}.zip` : name || image?.ref || "");
</script>

<Dialog bind:open {title} {sub} wide>
  {#if phase === "loading"}
    <p class="empty"><span class="spin"></span> Reading the model's settings…</p>
  {:else if phase === "failed"}
    <div class="callout er" role="alert">
      <Icon name="errc" />
      <span><b>Couldn't read what goes into the package.</b><p>{loadErr}. If the image was just deleted, refresh the Library.</p></span>
    </div>
  {:else if phase === "form" && src}
    {#if !src.capable}
      <div class="callout wa">
        <Icon name="warn" />
        <span><b>{src.model_name} can't be packaged yet.</b><p>{src.why}. Its image still runs in Docker or Podman.</p></span>
      </div>
    {:else}
      <p class="lead">
        One zip that runs {src.model_name} on its own: the model, llama.cpp built for that system and a small launcher with a chat page
        and the OpenAI API{src.harness ? ", plus the model's tools" : ""}. No Docker or Podman, and on a Mac the model runs on the GPU with Metal.
      </p>

      <fieldset class="targets">
        <legend class="lbl">For</legend>
        {#each targets as t (t.id)}
          <label class="tgt" class:on={target === t.id} class:off={!t.ready}>
            <input type="radio" name="native-target" value={t.id} bind:group={target} disabled={!t.ready} />
            <span class="tt">
              <span class="tn"><Icon name={t.os === "macos" ? "mac" : "chip"} size={15} />{t.label}</span>
              <span class="cs">
                <span class="chip gpu">{t.accel === "metal" ? "Metal GPU" : "Vulkan GPU or CPU"}</span>
                {#if t.preview}<span class="chip wa" title="Packaged the same way as macOS, but not yet tried on such a computer">Preview</span>{/if}
              </span>
              <small>{t.ready ? t.requirements : t.why}</small>
            </span>
          </label>
        {/each}
      </fieldset>

      <div class="grid">
        <label class="f">
          <span class="lbl">Package name</span>
          <input class="mono" type="text" bind:value={pkgName} oninput={() => (nameEdited = true)} spellcheck="false" aria-invalid={!nameOK} />
          <small class:bad={!nameOK}>{nameOK ? `Saved as packages/${pkgName.trim()}.zip` : "Letters, digits, dots, dashes and underscores."}</small>
        </label>
        <label class="f">
          <span class="lbl">Context</span>
          <input type="number" min="512" max="1048576" step="512" bind:value={ctx} aria-invalid={!ctxOK} />
          <small class:bad={!ctxOK}>{ctxOK ? "Tokens. The launcher's --ctx-size changes it later." : "512 tokens or more."}</small>
        </label>
      </div>

      <label class="f">
        <span class="lbl">System prompt</span>
        <textarea rows="4" bind:value={prompt} placeholder="None: the model answers as it was trained."></textarea>
      </label>
      {#if prompt.trim()}
        <label class="ck"><input type="checkbox" class="tg" bind:checked={always} />Use it even when a client sends its own system message</label>
      {/if}

      {#if src.harness}
        <label class="ck"><input type="checkbox" class="tg" bind:checked={withTools} />
          Include the tools: {harnessTools.length ? toolsText(harnessTools) : "none on"}
        </label>
        {#if withTools}
          <p class="hint">
            The file tools work in the package's workspace folder, on the computer it runs on.
            {#if src.shell_dropped}The image's shell tool is left out: nothing sandboxes it outside a container.{:else}The shell tool is never included: nothing sandboxes it outside a container.{/if}
          </p>
        {/if}
      {/if}

      <div class="callout">
        <Icon name="info" />
        <span>
          <b>{src.size_gb ? `About ${gb(src.size_gb + runtimeMB(sel) / 1000)} unzipped.` : "The weights, plus llama.cpp and the launcher."}</b>
          <p>
            The weights go in as they are; llama.cpp and the launcher add about {runtimeMB(sel)} MB.
            {#if sel?.os === "macos"}The launcher isn't signed by an Apple developer, so the first start needs your OK in System Settings; the README in the zip shows how.{/if}
          </p>
        </span>
      </div>
      {#if formErr}
        <div class="callout er" role="alert"><Icon name="errc" /><span><b>The package wasn't started.</b><p>{formErr}</p></span></div>
      {/if}
    {/if}
  {:else if phase === "job"}
    {#if !st}
      <p class="empty"><span class="spin"></span> Starting…</p>
    {:else if st.status === "done"}
      <div class="callout ok">
        <Icon name="check" />
        <span>
          <b>{st.package}.zip is ready{ready ? ` (${ready})` : ""}.</b>
          <p>{jobTarget ? startHow(jobTarget) : ""} The README inside says the rest.</p>
        </span>
      </div>
      {#if shareWarn}
        <div class="callout wa">
          <Icon name="warn" />
          <span><b>Check the model's license before you share this package.</b><p>{shareWarn.replace(/^./, (c) => c.toUpperCase())}. The license and model card are in its licenses folder.</p></span>
        </div>
      {/if}
      <dl>
        {#if license}<dt>Model license</dt><dd>{license}</dd>{/if}
        {#if zipSum}<dt>SHA-256</dt><dd class="mono sum">{zipSum}</dd>{/if}
      </dl>
    {:else if st.status === "error"}
      <div class="callout er" role="alert">
        <Icon name="errc" />
        <span><b>The package wasn't made.</b><p>{st.error || "See the log below."}</p></span>
      </div>
    {:else if st.status === "canceled"}
      <div class="callout"><Icon name="info" /><span><b>Stopped.</b><p>Nothing was saved; make it again whenever you like.</p></span></div>
    {:else}
      <div class="prog" role="status" aria-live="polite">
        <div class="bar" class:busy={pct == null || st.status === "queued"}><i style="width:{Math.round((pct ?? 0.3) * 100)}%"></i></div>
        <span class="sub">{st.status === "queued" ? "Waiting for the package before it to finish…" : lastLine.trim()}</span>
      </div>
      <p class="hint">You can close this: the package keeps being made, and the Library lists it.</p>
    {/if}
    {#if pollErr}<p class="hint bad">Lost touch with the factory ({pollErr}); retrying.</p>{/if}
    {#if lines.length}
      <details open={st?.status !== "done"}>
        <summary class="lbl">Log</summary>
        <pre class="log" bind:this={logEl}>{lines.join("\n")}</pre>
      </details>
    {/if}
  {/if}

  {#snippet actions()}
    {#if phase === "form"}
      <button class="ghost" type="button" onclick={() => (open = false)}>Cancel</button>
      {#if src?.capable}
        <button class="primary" type="button" disabled={!canMake} onclick={make}>
          {#if busy}<span class="spin"></span>{:else}<Icon name="box" size={14} />{/if}Make package
        </button>
      {/if}
    {:else if phase === "job" && st?.active}
      <button class="ghost" type="button" onclick={() => (open = false)}>Close</button>
      <button class="danger" type="button" onclick={stop}><Icon name="stopsq" size={14} />Stop</button>
    {:else if phase === "job" && st?.status === "done"}
      <button class="ghost" type="button" onclick={() => (open = false)}>Close</button>
      <a class="btn pri" href={downloadURL(st.package)} download><Icon name="download" size={14} />Download {st.package}.zip</a>
    {:else}
      {#if phase === "job" && src && st && !st.active}
        <button class="ghost" type="button" onclick={() => { cur = ""; phase = "form"; }}>Change settings</button>
      {/if}
      <button type="button" onclick={() => (open = false)}>Close</button>
    {/if}
  {/snippet}
</Dialog>

<style>
  .lead { margin: 0 0 14px; color: var(--mu); font-size: 13.5px; line-height: 1.5; }
  .targets { border: 0; padding: 0; margin: 0 0 14px; display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
  .targets legend { margin-bottom: 6px; padding: 0; }
  .tgt {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--ln2);
    border-radius: var(--r);
    background: var(--p1);
    cursor: pointer;
  }
  .tgt.on { border-color: var(--pr); box-shadow: 0 0 0 3px var(--prs); }
  .tgt.off { opacity: 0.6; cursor: not-allowed; }
  .tgt input { margin-top: 3px; }
  .tt { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .tn { display: flex; align-items: center; gap: 6px; font-weight: 600; font-size: 13.5px; }
  .cs { display: flex; gap: 5px; flex-wrap: wrap; }
  .tt small { color: var(--mu); font-size: 12px; line-height: 1.4; }
  .grid { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: 12px; }
  .f { display: flex; flex-direction: column; gap: 5px; margin-bottom: 12px; }
  .f small { color: var(--fa); font-size: 12px; }
  .bad { color: var(--er) !important; }
  .ck { display: flex; align-items: center; gap: 10px; font-size: 13.5px; margin: 0 0 10px; }
  .hint { margin: -2px 0 12px; }
  .callout { margin-top: 4px; }
  .prog { display: flex; flex-direction: column; gap: 8px; margin-bottom: 10px; }
  .prog .sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; font-family: var(--font-mono); font-size: 12px; }
  details { margin-top: 12px; }
  summary { cursor: pointer; margin-bottom: 6px; }
  .log {
    margin: 0;
    max-height: 260px;
    overflow: auto;
    padding: 10px 12px;
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: var(--r);
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.5;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  dl { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 6px 14px; margin: 12px 0 0; font-size: 13px; }
  dt { color: var(--mu); }
  dd { margin: 0; min-width: 0; }
  .sum { font-size: 11.5px; overflow-wrap: anywhere; }
  .empty { display: flex; align-items: center; gap: 8px; }
  @media (max-width: 760px) {
    .targets { grid-template-columns: 1fr; }
    .grid { grid-template-columns: 1fr; gap: 0; }
    dl { grid-template-columns: 1fr; gap: 2px; }
  }
</style>
