<script lang="ts">
  // FileFreak archive (System, below the memory plan; #system/archive): a
  // public FileFreak folder that holds the open-licensed model files the
  // factory downloads from Hugging Face. Builds look there first and use its
  // copy only when the SHA-256 matches the one Hugging Face publishes. With an
  // upload key, files the archive lacks are queued for upload after the build
  // downloads them. The key is sent once and only its hint comes back.
  // internal/filefreak, docs/FILEFREAK.md.
  import Icon from "../Icon.svelte";
  import CopyButton from "../CopyButton.svelte";
  import CodeLine from "../CodeLine.svelte";
  import { toast } from "../../lib/stores";
  import {
    archiveStatus,
    canRetry,
    checkArchiveFolder,
    jobChip,
    removeArchiveKey,
    removeUpload,
    retryUpload,
    saveArchiveSettings,
    sentence,
    sentText,
    setArchiveKey,
    SOURCE_TEXT,
    whenText,
    type ArchiveAnswer,
    type ArchiveJob,
    type ArchiveStatus,
  } from "../../lib/filefreak";

  const uid = $props.id();

  let st = $state<ArchiveStatus | null>(null);
  let loadError = $state("");
  let folderError = $state("");
  let busy = $state(""); // which action is in flight

  let linkInput = $state("");
  let editingLink = $state(false);
  let keyInput = $state("");
  let replacing = $state(false);

  function take(a: ArchiveAnswer) {
    st = a.status;
    loadError = "";
    if (a.folder_error !== undefined || !a.status.key.set || a.status.key_folder) folderError = a.folder_error ?? "";
  }

  async function load() {
    try {
      take(await archiveStatus());
    } catch (e) {
      loadError = (e as Error).message;
    }
  }

  // Poll: often while an upload runs, rarely otherwise.
  $effect(() => {
    let stop = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const tick = async () => {
      await load();
      if (stop) return;
      timer = setTimeout(tick, st?.worker.active ? 3000 : 20000);
    };
    tick();
    return () => {
      stop = true;
      clearTimeout(timer);
    };
  });

  async function act(name: string, fn: () => Promise<ArchiveAnswer>, done?: string): Promise<boolean> {
    busy = name;
    try {
      take(await fn());
      if (done) toast(done);
      return true;
    } catch (e) {
      toast(`${(e as Error).message}`, 7000);
      return false;
    } finally {
      busy = "";
    }
  }

  const toggleRead = (on: boolean) =>
    act("read", () => saveArchiveSettings({ read_from_archive: on }), on ? "Builds look in the archive first." : "Builds download from Hugging Face only.");

  async function saveLink() {
    if (await act("link", () => saveArchiveSettings({ archive_url: linkInput.trim() }), linkInput.trim() ? "Archive location saved." : "Archive location cleared.")) {
      editingLink = false;
    }
  }

  async function saveKey() {
    const ok = await act("key", () => setArchiveKey(keyInput.trim()));
    if (!ok) return;
    keyInput = "";
    replacing = false;
    toast(folderError ? "Key saved. Its archive folder isn't ready yet: see the card." : "Key saved. This install now uploads to the archive.");
  }

  async function removeKey() {
    if (!confirm("Remove the upload key? This install stops uploading; reading from the archive carries on.")) return;
    await act("unkey", removeArchiveKey, "Upload key removed.");
  }

  const reading = $derived(!!st && st.read_from_archive && !!st.archive.url);
  const uploading = $derived(!!st?.key.set);
  const active = $derived(st ? st.queue.filter((j) => j.state === "queued" || j.state === "checking" || j.state === "uploading").length : 0);

  const chip = $derived.by((): { text: string; cls: string } => {
    if (!st) return { text: loadError ? "Unknown" : "…", cls: "" };
    if (!st.helper.ok && (st.key.set || st.archive.url)) return { text: "Needs Node.js", cls: "wa" };
    if (st.worker.active && st.worker.current) return { text: "Uploading", cls: "pr" };
    if (reading && uploading) return { text: "Reading and uploading", cls: "ok" };
    if (reading) return { text: "Reading", cls: "ok" };
    if (uploading) return { text: "Uploading only", cls: "ok" };
    return { text: "Off", cls: "" };
  });

  function pct(j: ArchiveJob) {
    return j.size > 0 ? Math.min(100, Math.round((j.sent / j.size) * 100)) : 0;
  }
  const short = (rev: string) => rev.slice(0, 7);
</script>

<div class="hd">
  <h2>FileFreak archive</h2>
  <span class="chip {chip.cls}">{chip.text}</span>
</div>

<p class="sub intro">
  A public FileFreak folder with the open-licensed model files the factory downloads from Hugging Face. A build looks there first
  and uses the archive's copy only when its SHA-256 matches the one Hugging Face publishes. Anything else downloads from Hugging
  Face as before, and the build log says which source it used.
</p>

{#if !st}
  {#if loadError}
    <div class="callout er">
      <Icon name="errc" size={16} />
      <span><b>Couldn't read the archive settings.</b><p>{sentence(loadError)} This card tries again on its own.</p></span>
    </div>
  {:else}
    <p class="sub st"><span class="spin"></span>Reading the archive settings…</p>
  {/if}
{:else}
  {#if !st.helper.ok}
    <div class="callout wa">
      <Icon name="warn" size={16} />
      <span>
        <b>The archive helper can't run, so builds download from Hugging Face.</b>
        <p>{sentence(st.helper.error)} It needs Node.js 20 or later and its SDK in tools/filefreak; the start scripts install it. By hand:</p>
        <CodeLine cmd="cd tools/filefreak && npm ci --omit=dev" />
      </span>
    </div>
  {/if}

  <!-- Reading -->
  <label class="opt">
    <span class="t">
      Download from the archive first
      <small>
        {#if !st.archive.url}
          No archive location yet, so builds download from Hugging Face. Add one below, or set an upload key.
        {:else if st.read_from_archive}
          On: each model file is looked up by name and used when its SHA-256 matches Hugging Face's.
        {:else}
          Off: builds download from Hugging Face only.
        {/if}
      </small>
    </span>
    <input
      type="checkbox"
      class="tg"
      checked={st.read_from_archive}
      disabled={busy === "read"}
      onchange={(e) => toggleRead(e.currentTarget.checked)} />
  </label>

  <div class="block">
    <div class="bh"><span class="lbl">Archive location</span>{#if st.archive.source !== "none"}<span class="chip">{SOURCE_TEXT[st.archive.source]}</span>{/if}</div>
    {#if st.archive.url}
      <div class="link">
        <a class="mono" href={st.archive.url} target="_blank" rel="noreferrer">{st.archive.url}</a>
        <CopyButton text={st.archive.url} title="Copy the archive link" />
      </div>
    {:else}
      <p class="sub">None. Builds download from Hugging Face.</p>
    {/if}
    {#if st.env_url}
      <p class="hint">FILEFREAK_ARCHIVE_URL sets the location for this run, so the setting below applies once it's unset.</p>
    {:else if st.archive.source === "key" && st.archive_setting}
      <p class="hint">With an upload key, builds read from the key's folder, so the setting below applies once the key is removed.</p>
    {/if}

    {#if editingLink}
      <div class="row">
        <input
          type="url"
          bind:value={linkInput}
          placeholder="https://filefreak.link/public/<username>/<folder id>"
          spellcheck="false"
          autocomplete="off"
          aria-label="Archive location (a public FileFreak folder link)"
          onkeydown={(e) => e.key === "Enter" && saveLink()} />
        <button class="small primary" onclick={saveLink} disabled={busy === "link"}>Save</button>
        <button class="small ghost" onclick={() => (editingLink = false)}>Cancel</button>
      </div>
      <p class="hint">The public link of the archive's folder on FileFreak. Leave it empty and save to clear it.</p>
    {:else}
      <div class="btns">
        <button class="small" onclick={() => ((linkInput = st?.archive_setting ?? ""), (editingLink = true))}>
          <Icon name="link" size={14} />{st.archive_setting ? "Change the location" : "Set a location"}
        </button>
        {#if st.archive_setting}
          <button class="small ghost" disabled={busy === "link"} onclick={() => ((linkInput = ""), saveLink())}>Clear</button>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Uploading -->
  <div class="block">
    <div class="bh"><span class="lbl">Upload key</span>{#if st.key.set}<span class="chip ok">Set</span>{/if}</div>
    {#if st.key.set && !replacing}
      <div class="keyrow">
        <Icon name="key" size={15} />
        <span class="mono">{st.key.hint}</span>
        <span class="sp"></span>
        <button class="small" onclick={() => (replacing = true)}>Replace</button>
        <button class="small danger" onclick={removeKey} disabled={busy === "unkey"}>Remove</button>
      </div>
      {#if st.key_folder}
        <p class="sub fold">
          <span>Uploads go to <a href={st.key_folder.url} target="_blank" rel="noreferrer">{st.key_folder.username}/{st.key_folder.name}</a>{st.key_folder.public ? ", a public folder without Shard Protection." : "."}</span>
          <CopyButton text={st.key_folder.url} title="Copy the folder link" />
        </p>
      {:else}
        <div class="callout wa">
          <Icon name="warn" size={16} />
          <span>
            <b>The key's archive folder isn't ready.</b>
            <p>
              {sentence(folderError) || "It hasn't been looked up yet."} Uploads wait until it is. If FileFreak says the account's public page is
              off, switch it on in FileFreak's settings, then check again.
            </p>
            <button class="small" onclick={() => act("folder", checkArchiveFolder)} disabled={busy === "folder"}>
              {#if busy === "folder"}<span class="spin"></span>{:else}<Icon name="retry" size={14} />{/if}Check again
            </button>
          </span>
        </div>
      {/if}
    {:else}
      <div class="row">
        <input
          id="{uid}-key"
          type="password"
          bind:value={keyInput}
          placeholder="ffk_live_…"
          autocomplete="off"
          spellcheck="false"
          aria-label="FileFreak upload key"
          aria-describedby="{uid}-keyhint"
          onkeydown={(e) => e.key === "Enter" && keyInput.trim() && saveKey()} />
        <button class="small primary" onclick={saveKey} disabled={!keyInput.trim() || busy === "key"}>
          {#if busy === "key"}<span class="spin dark"></span>{/if}Save key
        </button>
        {#if replacing}<button class="small ghost" onclick={() => ((replacing = false), (keyInput = ""))}>Cancel</button>{/if}
      </div>
      <p class="hint" id="{uid}-keyhint">
        Optional: only installs with a key upload. A FileFreak key that may create folders and upload. It's saved in
        <span class="mono">config/filefreak.json</span> on the factory's computer, readable only by its user, and never shown again.
      </p>
    {/if}
  </div>

  <!-- The queue -->
  <div class="block">
    <div class="bh">
      <span class="lbl">Uploads</span>
      {#if active}<span class="chip pr">{active} to upload</span>{/if}
    </div>
    {#if !st.queue.length}
      <p class="sub">
        {st.key.set
          ? "Nothing yet. When a build downloads an open-licensed file the archive lacks, it's queued here and uploaded in the background."
          : "Uploads need an upload key. Builds never wait for them."}
      </p>
    {:else}
      {#if !st.key.set && active}
        <p class="hint">These wait for an upload key.</p>
      {/if}
      <ul class="jobs">
        {#each st.queue as j (j.id)}
          {@const c = jobChip(j)}
          <li class="job">
            <div class="jh">
              <span class="jn">
                <b title={j.file}>{j.file}</b>
                <small class="mono">{j.repo} @ {short(j.revision)}{j.license ? ` · ${j.license}` : ""}</small>
              </span>
              <span class="chip {c.cls}">{c.text}</span>
            </div>
            {#if j.state === "uploading" || (j.state === "checking" && j.sent > 0)}
              <div
                class="bar"
                role="progressbar"
                aria-label="Upload of {j.file}"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={pct(j)}>
                <i style="width:{pct(j)}%"></i>
              </div>
              <small class="mono prog">{pct(j)}% · {sentText(j)}</small>
            {:else if j.state === "checking"}
              <div class="bar busy" role="progressbar" aria-label="Checking {j.file}"><i></i></div>
            {/if}
            {#if j.reason}
              <p class="why" class:er={j.state === "failed"}>
                {sentence(j.reason)}{j.state === "queued" && j.next_try ? ` Trying again ${whenText(j.next_try)}.` : ""}
              </p>
            {/if}
            <div class="jb">
              {#if j.url}
                <a class="btn sm" href={j.url} target="_blank" rel="noreferrer"><Icon name="external" size={14} />Open in the archive</a>
              {/if}
              {#if canRetry(j)}
                <button class="small" onclick={() => act(`r${j.id}`, () => retryUpload(j.id))} disabled={busy === `r${j.id}`}>
                  <Icon name="retry" size={14} />Retry
                </button>
              {/if}
              {#if st.worker.current !== j.id}
                <button class="small ghost" onclick={() => act(`x${j.id}`, () => removeUpload(j.id))} disabled={busy === `x${j.id}`}>
                  Remove
                </button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </div>

  <details class="about">
    <summary>What FileFreak sees, and which files are uploaded</summary>
    <div class="more">
      <p>
        <b>Reading</b> needs no key or account. To find a file, the factory asks FileFreak for the archive's public folder listing,
        by the names of the model repositories, revisions and files being downloaded. That is all it sends.
      </p>
      <p>
        <b>Uploading</b> (only with a key) sends the model file, the repository's LICENSE and README files, and a small provenance
        record: where the file came from, its revision and SHA-256, and its license. Files are public, without Shard Protection, so
        anyone can download them with the FileFreak SDK.
      </p>
      <p>
        <b>The license rule.</b> A file is uploaded only when its Hugging Face repository isn't gated and carries one of these
        licenses: <span class="mono">{st.licenses.join(", ")}</span>. Llama, Gemma and other custom licenses ("other", or none) are
        never uploaded; the queue says why. Downloading from the archive doesn't depend on the license.
      </p>
    </div>
  </details>
{/if}

<style>
  .intro { margin-bottom: 12px; font-size: 12.5px; line-height: 1.45; }
  .st { display: flex; align-items: center; gap: 8px; }
  .callout { margin-bottom: 12px; }
  .callout :global(.code) { margin-top: 8px; }
  .callout .small { margin-top: 8px; }

  .opt { display: flex; align-items: center; gap: 12px; padding: 4px 0 12px; font-size: 13px; cursor: pointer; }
  .t { min-width: 0; flex: 1; }
  .t small { display: block; color: var(--mu); font-size: 12px; margin-top: 1px; }
  .opt .tg { flex: none; }

  .block { padding: 12px 0; border-top: 1px solid var(--ln); display: flex; flex-direction: column; gap: 8px; min-width: 0; }
  .block:last-of-type { padding-bottom: 4px; }
  .bh { display: flex; align-items: center; gap: 8px; }
  .link { display: flex; align-items: center; gap: 6px; min-width: 0; }
  .link a { font-size: 12px; color: var(--pr); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; min-width: 0; }
  .hint { font-size: 12px; line-height: 1.45; }
  .row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
  .row input { flex: 1; min-width: 200px; font-size: 13px; padding: 7px 10px; }
  .btns { display: flex; gap: 8px; flex-wrap: wrap; }
  .keyrow { display: flex; align-items: center; gap: 8px; font-size: 13px; flex-wrap: wrap; }
  .keyrow :global(.ico) { color: var(--fa); }
  .keyrow .sp { flex: 1; }
  .fold { font-size: 12.5px; display: flex; align-items: center; gap: 4px; flex-wrap: wrap; }
  .fold a { color: var(--pr); font-weight: 500; }

  .jobs { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; }
  .job { padding: 10px 0; border-top: 1px solid var(--ln); display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .job:first-child { border-top: 0; padding-top: 2px; }
  .jh { display: flex; align-items: flex-start; gap: 10px; min-width: 0; }
  .jn { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  .jn b { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .jn small { font-size: 11.5px; color: var(--mu); overflow-wrap: anywhere; }
  .jh .chip { flex: none; }
  .prog { font-size: 11.5px; color: var(--mu); font-variant-numeric: tabular-nums; }
  .why { font-size: 12px; color: var(--mu); line-height: 1.45; overflow-wrap: anywhere; }
  .why.er { color: var(--er); }
  .jb { display: flex; gap: 8px; flex-wrap: wrap; }
  .jb:empty { display: none; }

  .about { margin-top: 8px; padding-top: 10px; border-top: 1px solid var(--ln); font-size: 12.5px; color: var(--mu); }
  .about summary { cursor: pointer; font-weight: 500; width: fit-content; border-radius: 4px; }
  .about summary:hover { color: var(--ink); }
  .about .more { display: flex; flex-direction: column; gap: 8px; margin-top: 8px; line-height: 1.5; }
  .about b { color: var(--ink); font-weight: 600; }
  .about .mono { font-size: 11.5px; }
  .spin { width: 13px; height: 13px; }
  .spin.dark { width: 12px; height: 12px; border-color: color-mix(in srgb, var(--onpr) 30%, transparent); border-top-color: var(--onpr); }
  @media (max-width: 760px) {
    .row input { min-width: 0; width: 100%; flex-basis: 100%; }
  }
</style>
