<script lang="ts">
  // Copies `text`; shows a check for a moment. `label` = visible text (omit for icon-only).
  import Icon from "./Icon.svelte";
  import { copyText } from "../lib/ui.svelte";
  import { toast } from "../lib/stores";

  let { text, label = "", cls = "sm", title = "Copy" }: { text: string; label?: string; cls?: string; title?: string } = $props();
  let done = $state(false);
  let timer: ReturnType<typeof setTimeout> | undefined;

  async function copy(e: MouseEvent) {
    e.stopPropagation();
    if (await copyText(text)) {
      done = true;
      clearTimeout(timer);
      timer = setTimeout(() => (done = false), 1400);
    } else {
      toast("Couldn't copy here — select the text and copy it yourself.");
    }
  }
</script>

<button class="btn {cls}" class:icon={!label} onclick={copy} {title} aria-label={label ? undefined : title}>
  <Icon name={done ? "check" : "copy"} size={14} />{#if label}<span>{done ? "Copied" : label}</span>{/if}
</button>
