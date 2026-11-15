<script lang="ts">
  // Shown when the factory answers 401: this browser doesn't hold the factory's
  // access token yet.
  import Mark from "../Mark.svelte";
  import Icon from "../Icon.svelte";
  import { auth, unlock } from "../../lib/auth.svelte";

  let token = $state("");
  const isWin = typeof navigator !== "undefined" && /Win/.test(navigator.platform || navigator.userAgent);
  const script = isWin ? "scripts\\windows\\start.ps1" : "scripts/macos/start.sh (or scripts/linux/start.sh)";
</script>

<div class="lock">
  <form
    class="box"
    onsubmit={(e) => {
      e.preventDefault();
      if (token.trim()) unlock(token);
    }}>
    <Mark size={44} />
    <h1>Open the factory with its access link</h1>
    <p class="sub">
      The factory only answers browsers that hold its access token, so a model with a terminal can't drive it.
      You need to do this once per browser.
    </p>
    <ol>
      <li>Open the link that <code>{script}</code> printed when it started the factory, <b>or</b></li>
      <li>paste the token from <code>config/factory-token</code> in the factory folder:</li>
    </ol>
    <div class="row">
      <input
        type="password"
        bind:value={token}
        placeholder="Access token"
        autocomplete="off"
        spellcheck="false"
        aria-label="Access token" />
      <button class="primary" type="submit" disabled={auth.busy || !token.trim()}>
        <Icon name="key" size={15} />Unlock
      </button>
    </div>
    {#if auth.error}<p class="callout er"><Icon name="errc" size={16} /><span>{auth.error}</span></p>{/if}
  </form>
</div>

<style>
  .lock {
    position: fixed;
    inset: 0;
    z-index: 100;
    display: grid;
    place-items: center;
    padding: 16px;
    background: var(--cv);
  }
  .box {
    width: min(520px, 100%);
    background: var(--p1);
    border: 1px solid var(--ln);
    border-radius: 18px;
    padding: 28px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  h1 { font-size: 22px; margin-top: 6px; }
  ol { margin: 0; padding-left: 20px; color: var(--mu); font-size: 13.5px; display: flex; flex-direction: column; gap: 4px; }
  code { font-size: 12px; color: var(--ink); background: var(--p2); padding: 1px 5px; border-radius: 5px; }
  .row { display: flex; gap: 8px; }
  .row input { flex: 1; }
</style>
