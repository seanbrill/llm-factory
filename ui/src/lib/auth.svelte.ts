// The factory's access token. The factory's own API needs it (a model with a
// terminal could otherwise drive the factory), so a browser holds it in an
// HttpOnly cookie, set by opening the link start.sh prints (/?token=…) or by
// pasting the token here. Any 401 {code:"auth_required"} locks the page.
import { api, post, setAuthRequiredHandler } from "./api";

export const auth = $state({
  locked: false,
  busy: false,
  error: "",
});

setAuthRequiredHandler(() => {
  auth.locked = true;
});

/** Ask the factory whether this page is signed in (older factories: no-op). */
export async function checkAuth() {
  try {
    const s = await api<{ authed: boolean; required: boolean }>("/api/auth/status");
    auth.locked = s.required && !s.authed;
  } catch {
    /* an older factory without auth, or it's restarting */
  }
}

export async function unlock(token: string): Promise<boolean> {
  auth.busy = true;
  auth.error = "";
  try {
    await post("/api/auth/login", { token: token.trim() });
    auth.locked = false;
    // Everything loaded so far failed with 401: start clean.
    location.reload();
    return true;
  } catch (e) {
    auth.error = (e as Error).message || "That didn't work.";
    return false;
  } finally {
    auth.busy = false;
  }
}
