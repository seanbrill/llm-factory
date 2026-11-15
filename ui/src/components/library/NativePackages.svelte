<script lang="ts">
  // The native packages in packages/ (docs/NATIVE.md), and the ones being
  // made: each with its download, or its progress.
  import Icon from "../Icon.svelte";
  import { gb, ago } from "../../lib/format";
  import { toolsText } from "../../lib/baked";
  import { downloadURL, type NativePackage, type NativeJobSummary } from "../../lib/native";

  let {
    packages,
    jobs,
    onopen,
    ondelete,
  }: {
    packages: NativePackage[];
    jobs: NativeJobSummary[];   // queued or running
    onopen: (jobId: string) => void;
    ondelete: (p: NativePackage) => void;
  } = $props();

  const dots = (...xs: (string | false | null | undefined)[]) => xs.filter(Boolean).join(" · ");
</script>

<section class="card" aria-label="Native packages">
  <div class="hd">
    <h2>Native packages</h2>
    <span class="chip">{packages.length}</span>
    <span class="sub">Run without Docker or Podman · packages/</span>
  </div>
  <div class="list">
    {#each jobs as j (j.id)}
      <div class="it">
        <span class="ico"><span class="spin"></span></span>
        <div class="iw">
          <span><b>{j.package}.zip</b> · {j.target_label}</span>
          <span class="meta ln">{j.status === "queued" ? "Waiting for the package before it" : j.last_line?.trim() || "Starting…"}</span>
        </div>
        <div class="ia"><button class="small" type="button" onclick={() => onopen(j.id)}>Show progress</button></div>
      </div>
    {/each}
    {#each packages as p (p.name)}
      <div class="it">
        <span class="ico"><Icon name={p.target.startsWith("macos") ? "mac" : "box"} size={15} /></span>
        <div class="iw">
          <span><b>{p.model_name || p.name}</b> · {p.target_label || p.target}</span>
          <span class="meta">
            {dots(
              `${p.file}, ${gb(p.size_bytes / 1e9)}`,
              p.toolsets.length ? `tools: ${toolsText(p.toolsets)}` : "no tools",
              p.license ? `license ${p.license}` : "license unknown",
              p.created && `made ${ago(Date.parse(p.created))}`,
            )}
          </span>
          {#if !p.shareable}<span class="meta"><b class="wa">Check the model's license before you share it.</b></span>{/if}
        </div>
        <div class="ia">
          <a class="btn sm" href={downloadURL(p.name)} download title="Download {p.file}"><Icon name="download" size={14} />Download</a>
          <button class="small danger" type="button" onclick={() => ondelete(p)} title="Delete {p.file}"><Icon name="trash" size={14} />Delete</button>
        </div>
      </div>
    {/each}
  </div>
</section>

<style>
  .hd .sub { margin-left: auto; }
  .list { display: flex; flex-direction: column; gap: 6px; }
  .it {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 10px 12px;
    border: 1px solid var(--ln);
    border-radius: var(--r);
    background: var(--p1);
  }
  .ico { width: 28px; height: 28px; border-radius: 8px; background: var(--p3); display: grid; place-items: center; color: var(--mu); flex: none; }
  .iw { display: flex; flex-direction: column; gap: 3px; min-width: 0; flex: 1; font-size: 13px; }
  .meta { color: var(--mu); font-size: 12.5px; overflow-wrap: anywhere; }
  .meta.ln { font-family: var(--font-mono); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  b.wa { color: var(--wa); font-weight: 600; }
  .ia { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
  @media (max-width: 760px) {
    .hd .sub { display: none; }
    .it { flex-wrap: wrap; }
    .ia { width: 100%; justify-content: flex-start; padding-left: 40px; }
  }
</style>
