<script lang="ts">
  // Build rules (sheet frame f13): what the server always does to keep builds
  // from running out of memory. These are explanations, not settings — none of
  // them can be switched off — so each shows its value instead of a control.
  // Kept in step with internal/builder (engines.go, preflight) and
  // internal/server/buildqueue.go.
  import { sysinfo } from "../../lib/stores";
  import { compileFloor, JOB_GIB, JOB_TIMEOUT_H } from "../../lib/system.svelte";

  const cpuFloor = $derived(compileFloor($sysinfo, "cpu"));
  const gpuFloor = $derived(compileFloor($sysinfo, "vulkan"));

  const rules = $derived([
    {
      title: "Compile jobs",
      text: `One job per ${JOB_GIB.cpu} GB of the engine's free memory (${JOB_GIB.gpu} GB for a GPU image), at most one per CPU. Each engine above shows its count.`,
      value: "Automatic",
    },
    {
      title: "When an engine is too small",
      text: "The image compiles on the other engine and moves over when it's done. If neither has enough, the build stops right away and says how to fix it.",
      value: "Always",
    },
    {
      title: "Check memory before building",
      text: `Builds that compile from source need ${cpuFloor} GB on the engine (${gpuFloor} GB for GPU), checked before anything downloads. Images that only install packages skip it.`,
      value: "Always",
    },
    {
      title: "Remove leftover build folders",
      text: "Folders and image tars from builds the factory didn't finish go when it starts. Overview and the Library clean them up whenever nothing is building.",
      value: "At start",
    },
    {
      title: "Builds at the same time",
      text: "Docker and Podman each build one image at a time, side by side. A build compiling on the other engine waits for that engine's turn.",
      value: "1 per engine",
    },
    {
      title: "Time limit",
      text: `A build still running after ${JOB_TIMEOUT_H} hours is stopped, so a stuck one can't hold its engine.`,
      value: `${JOB_TIMEOUT_H} h`,
    },
  ]);
</script>

<div class="hd"><h2>Build rules</h2><span class="sub">Always on</span></div>
<ul class="rules">
  {#each rules as r (r.title)}
    <li class="opt">
      <span class="t">{r.title}<small>{r.text}</small></span>
      <span class="chip">{r.value}</span>
    </li>
  {/each}
</ul>

<style>
  .rules { list-style: none; margin: 0; padding: 0; }
  .opt { display: flex; align-items: center; gap: 12px; padding: 11px 0; border-top: 1px solid var(--ln); font-size: 13px; }
  .opt:first-child { border-top: 0; padding-top: 0; }
  .t { min-width: 0; flex: 1; }
  .t small { display: block; color: var(--mu); font-size: 12px; margin-top: 1px; }
  .opt .chip { flex: none; }
</style>
