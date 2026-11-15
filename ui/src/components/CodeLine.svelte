<script lang="ts">
  // A host command to run, with a copy button.
  import CopyButton from "./CopyButton.svelte";
  let { cmd }: { cmd: string } = $props();
</script>

<div class="code"><span>{cmd}</span><CopyButton text={cmd} title="Copy command" /></div>
