<script lang="ts">
  // Overview (sheet frames f1, f16): the machine as it really is — both
  // engines, their memory and GPU — the build lanes, what's running, the
  // harness and the disk at a glance, and what needs the user, each problem
  // with its fix. On a phone it narrows to what's building, the one thing that
  // needs you, and what's running.
  import { MediaQuery } from "svelte/reactivity";
  import MachineCard from "../components/overview/MachineCard.svelte";
  import EngineCard from "../components/overview/EngineCard.svelte";
  import BuildsCard from "../components/overview/BuildsCard.svelte";
  import RunningCard from "../components/overview/RunningCard.svelte";
  import HarnessCard from "../components/overview/HarnessCard.svelte";
  import DiskCard from "../components/overview/DiskCard.svelte";
  import QuickActions from "../components/overview/QuickActions.svelte";
  import PhoneOverview from "../components/overview/PhoneOverview.svelte";
  import { api } from "../lib/api";
  import { loadSysInfo, sysinfo } from "../lib/stores";
  import { build } from "../lib/build.svelte";
  import { watch } from "../lib/live.svelte";
  import { failureLog, failuresToShow } from "../lib/overview";

  $effect(() => watch("containers", "storage", "resources", "harness"));

  const phone = new MediaQuery("max-width: 760px");

  // The engines were read at startup; read them again so this page is current.
  let siState = $state<"loading" | "ok" | "error">($sysinfo ? "ok" : "loading");
  $effect(() => {
    loadSysInfo().then(() => (siState = $sysinfo ? "ok" : "error"));
  });

  // The shell keeps the queue in build.jobs; this read only says whether the
  // queue answered (so an empty list isn't mistaken for "nothing building").
  let queue = $state<"loading" | "ok" | "error">(build.jobs.length ? "ok" : "loading");
  let queueErr = $state("");
  $effect(() => {
    api("/api/builds")
      .then(() => (queue = "ok"))
      .catch((e: Error) => {
        queue = "error";
        queueErr = e.message || "no answer";
      });
  });

  // Failed builds that still need the user, with their logs (read once each)
  // so the explanation can say what ran out.
  const failures = $derived(failuresToShow(build.jobs));
  let logs = $state<Record<string, string>>({});
  const asked = new Set<string>();
  $effect(() => {
    for (const f of failures) {
      const id = f.job.id;
      if (asked.has(id)) continue;
      asked.add(id);
      failureLog(id).then((l) => (logs[id] = l));
    }
  });

  const engines = $derived($sysinfo?.engines ?? []);
</script>

{#if phone.current}
  <PhoneOverview {failures} {logs} {queue} {queueErr} />
{:else}
  <div class="ov">
    <section class="machine" class:n3={engines.length === 2} aria-label="This machine and its engines">
      <MachineCard state={siState} />
      {#each engines as e (e.engine)}<EngineCard {e} />{/each}
    </section>
    <section class="two" aria-label="Builds and running models">
      <BuildsCard {failures} {logs} {queue} {queueErr} />
      <RunningCard />
    </section>
    <section class="three" aria-label="Harness, disk and quick actions">
      <HarnessCard />
      <DiskCard />
      <QuickActions />
    </section>
  </div>
{/if}

<style>
  .ov { display: flex; flex-direction: column; gap: 18px; }
  .machine { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 14px; align-items: stretch; }
  .machine.n3 { grid-template-columns: 1fr 1fr 1.25fr; }
  .two { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr); gap: 14px; }
  .three { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 14px; }
  @media (max-width: 1240px) {
    .machine.n3 { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  }
  @media (max-width: 1000px) {
    .machine.n3 { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
    .machine.n3 > :global(:first-child) { grid-column: 1 / -1; }
    .two { grid-template-columns: minmax(0, 1fr); }
    .three { grid-template-columns: minmax(0, 1fr) minmax(0, 1fr); }
    .three > :global(:last-child) { grid-column: 1 / -1; }
  }
</style>
