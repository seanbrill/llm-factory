<script lang="ts">
  // Circular progress indicator for in-flight media generation. The backend runs
  // as one long request with no progress stream, so the % is an ESTIMATE from
  // elapsed time vs. a typical duration (eta), capped at 95% until the result
  // lands (which unmounts this component). Honest about being an estimate.
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

<div class="gen">
  <div class="ring">
    <svg viewBox="0 0 64 64" width="58" height="58">
      <circle class="track" cx="32" cy="32" r={R} />
      <circle class="prog" cx="32" cy="32" r={R} style="stroke-dasharray:{CIRC};stroke-dashoffset:{offset}" />
    </svg>
    <span class="pct">{pct}%</span>
  </div>
  <div class="meta">
    <div class="label">{label}</div>
    <div class="sub">{time}{#if over} · taking longer than usual…{/if}</div>
  </div>
</div>

<style>
  .gen { display: flex; align-items: center; gap: 14px; padding: 6px 2px; }
  .ring { position: relative; width: 58px; height: 58px; flex: none; }
  .ring svg { transform: rotate(-90deg); display: block; }
  .track { fill: none; stroke: #232838; stroke-width: 5; }
  .prog { fill: none; stroke: var(--accent); stroke-width: 5; stroke-linecap: round; transition: stroke-dashoffset 0.4s linear; }
  .pct { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; font-size: 12.5px; font-weight: 600; color: var(--text); font-variant-numeric: tabular-nums; }
  .meta { display: flex; flex-direction: column; gap: 3px; }
  .label { font-size: 13.5px; color: var(--text); }
  .sub { font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; }
</style>
