<script lang="ts">
  // Progress ring for a media generation in flight. The factory answers in one
  // long request with no progress stream, so the % is an estimate from the time
  // elapsed against a typical duration (eta), held at 95% until the result
  // lands (which removes this). It says so.
  let { eta = 100000, label = "Generating…" }: { eta?: number; label?: string } = $props();

  const start = Date.now();
  let elapsed = $state(0);
  $effect(() => {
    const id = setInterval(() => (elapsed = Date.now() - start), 400);
    return () => clearInterval(id);
  });

  const pct = $derived(Math.min(95, Math.floor((elapsed / eta) * 100)));
  const time = $derived.by(() => {
    const s = Math.floor(elapsed / 1000);
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
  });
  const over = $derived(elapsed > eta * 1.5);

  const R = 26;
  const CIRC = 2 * Math.PI * R;
  const offset = $derived(CIRC * (1 - pct / 100));
</script>

<div class="gen" role="status">
  <div class="ring" title="An estimate from the usual time; the factory doesn't report progress">
    <svg viewBox="0 0 64 64" width="56" height="56" aria-hidden="true">
      <circle class="track" cx="32" cy="32" r={R} />
      <circle class="prog" cx="32" cy="32" r={R} style="stroke-dasharray:{CIRC};stroke-dashoffset:{offset}" />
    </svg>
    <span class="pct">~{pct}%</span>
  </div>
  <div class="meta">
    <div class="label">{label}</div>
    <div class="sub">{time}{over ? " · taking longer than usual" : ""}</div>
  </div>
</div>

<style>
  .gen { display: flex; align-items: center; gap: 14px; padding: 4px 0; }
  .ring { position: relative; width: 56px; height: 56px; flex: none; }
  .ring svg { transform: rotate(-90deg); display: block; }
  .track { fill: none; stroke: var(--p3); stroke-width: 5; }
  .prog { fill: none; stroke: var(--pr); stroke-width: 5; stroke-linecap: round; transition: stroke-dashoffset 0.4s linear; }
  .pct {
    position: absolute;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    font: 600 11.5px var(--font-mono);
    color: var(--ink);
    font-variant-numeric: tabular-nums;
  }
  .meta { display: flex; flex-direction: column; gap: 3px; }
  .label { font-size: 13.5px; color: var(--ink); }
  .sub { font: 12px var(--font-mono); color: var(--mu); font-variant-numeric: tabular-nums; }
</style>
