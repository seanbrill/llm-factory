<script lang="ts">
  import Icon from "./Icon.svelte";
  import { modMeta } from "../lib/icons";
  let { mod }: { mod: string } = $props();
</script>

<span class="badge mod mod-{mod}"><Icon name={modMeta(mod).icon} size={13} /> {modMeta(mod).label}</span>

<style>
  .badge.mod {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 2px 8px;
    border-radius: 6px;
    font-size: 11.5px;
    line-height: 1.6;
    background: #1c2436;
    border: 1px solid var(--border);
    color: #c7d0e0;
    white-space: nowrap;
  }
  .mod-vision { border-color: #6d5ae0; color: #c4b8ff; }
  .mod-image { border-color: #c062a0; color: #f3b8e0; }
  .mod-video { border-color: #c0623a; color: #f3c8a8; }
  .mod-audio-stt, .mod-tts { border-color: #4a9c8c; color: #a8f0e0; }
  .mod-embedding { border-color: #b08a3a; color: #f0d8a0; }
  .mod-code, .mod-reasoning { border-color: #3a78b0; color: #a8d0f0; }
</style>
