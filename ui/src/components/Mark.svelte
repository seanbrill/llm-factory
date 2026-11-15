<script lang="ts">
  // The LLM Factory mark: an orange crate (the container image) with a cyan
  // spark (the model). Knitted yarn yn_01M7EKYVDH49NCXARPQF.
  let { size = 32 }: { size?: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 64 64" role="img" aria-label="LLM Factory">
  <path d="M32 8 L54 20.5 L32 33 L10 20.5 Z" fill="#ffb08a" />
  <path d="M10 20.5 L32 33 L32 57 L10 44.5 Z" fill="#ff6a1f" />
  <path d="M54 20.5 L32 33 L32 57 L54 44.5 Z" fill="#c4470c" />
  <path d="M14.5 30 L27.5 37.4 M14.5 36.5 L27.5 43.9" stroke="#101216" stroke-width="2.5" stroke-linecap="round" opacity="0.55" />
  <path d="M32 12.5 C32.9 17.6 34.6 19.6 40 20.5 C34.6 21.4 32.9 23.4 32 28.5 C31.1 23.4 29.4 21.4 24 20.5 C29.4 19.6 31.1 17.6 32 12.5 Z" fill="#101216" />
  <path d="M32 15.5 C32.6 18.6 33.7 19.8 37 20.5 C33.7 21.2 32.6 22.4 32 25.5 C31.4 22.4 30.3 21.2 27 20.5 C30.3 19.8 31.4 18.6 32 15.5 Z" fill="#3ccbf4" />
</svg>
