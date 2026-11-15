// The factory's FileFreak archive, in Node: what ff.mjs does for each
// command, written so a test can hand it a fake client and a fake site.
//
// Reading needs no key. The archive is a public folder, given by its page's
// address (https://filefreak.link/public/<username>/<folder id>); a file in it
// is found by walking its folders by name through FileFreak's public answers
// (/api/public/files/<id>/items: there is no lookup by hash), and saved with
// the SDK's get(), which checks the bytes against the SHA-256 the file was
// uploaded with. The factory compares that SHA-256 with the one Hugging Face
// publishes before it uses the file.
//
// Writing needs the upload key. Folders are made with ifExists "use", the
// archive's top folder is made public with Shard Protection off (so its files
// are stored as they are and anybody can read them without a key), and files
// are uploaded in parts with the SHA-256 they must hash to; an upload that
// stops is carried on with its id.

import { FileFreakError, IntegrityError, UploadStopped, get } from "@filefreak.io/sdk";
import { fromPath, toPath } from "@filefreak.io/sdk/node";
import { rm } from "node:fs/promises";

/** Where a reader's links open, unless FILEFREAK_LINKS_ORIGIN says otherwise. */
export const DEFAULT_LINKS_ORIGIN = "https://filefreak.link";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const USERNAME = /^[A-Za-z0-9_]{3,30}$/;
const SHA256 = /^[0-9a-f]{64}$/;
/** The most a public listing gives at once (api/src/routes/reading.ts). */
const PAGE = 200;
/** A provenance record is a few hundred bytes; anything much bigger is not one. */
const MOST_PROVENANCE_BYTES = 64 * 1024;
/** How many of a repository's archived revisions a lookup without one reads. */
const MOST_REVISIONS = 10;

/** The suffix of the record kept beside each archived file. */
export const PROVENANCE_SUFFIX = ".provenance.json";

/** A failure the factory can branch on: a code that does not change, and a sentence. */
export class HelperError extends Error {
  constructor(code, message, extra = {}) {
    super(message);
    this.name = "HelperError";
    this.code = code;
    Object.assign(this, extra);
  }
}

/**
 * The archive's public folder, from the address of its page:
 * https://<host>/public/<username>/<folder id>, on https, or on http for
 * localhost and 127.0.0.1 (a local stack or a test).
 */
export function parseArchiveLink(link) {
  let u;
  try {
    u = new URL(String(link));
  } catch {
    throw new HelperError("bad_archive_link", "The archive location isn't a web address. Use the link of the archive's public folder: https://filefreak.link/public/<username>/<folder id>.");
  }
  const local = u.hostname === "localhost" || u.hostname === "127.0.0.1";
  if (!(u.protocol === "https:" || (u.protocol === "http:" && local)) || u.username || u.password) {
    throw new HelperError("bad_archive_link", "The archive location has to be an https address.");
  }
  const parts = u.pathname.split("/").filter((p) => p !== "");
  if (parts.length !== 3 || parts[0] !== "public" || !USERNAME.test(parts[1]) || !UUID.test(parts[2])) {
    throw new HelperError("bad_archive_link", "The archive location has to be a public folder's link: https://filefreak.link/public/<username>/<folder id>.");
  }
  return { origin: u.origin, username: parts[1], folderId: parts[2].toLowerCase() };
}

/** A path in the archive as its names, refusing anything that could step outside it. */
export function segmentsOf(path) {
  const segs = String(path).split("/").filter((s) => s !== "");
  if (segs.length === 0) throw new HelperError("bad_path", "No path was given.");
  for (const s of segs) {
    if (s === "." || s === ".." || s.includes("\\") || /[\u0000-\u001f]/.test(s)) throw new HelperError("bad_path", `${JSON.stringify(path)} isn't a path in the archive.`);
  }
  return segs;
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/**
 * FileFreak's public answers at the archive's own site, asked with nothing
 * that says who is asking. A read is asked again when the answer says nothing
 * was done (429, 502-504), and while the service wakes up.
 */
export class PublicSite {
  constructor(origin, { fetch: fetcher = globalThis.fetch.bind(globalThis), retries = 4, wait = sleep } = {}) {
    this.origin = origin;
    this.fetch = fetcher;
    this.retries = retries;
    this.wait = wait;
  }

  async ask(path) {
    let waking = 0;
    for (let attempt = 0; ; attempt++) {
      let res;
      try {
        res = await this.fetch(`${this.origin}${path}`, { headers: { Accept: "application/json" }, credentials: "omit" });
      } catch (e) {
        if (attempt >= this.retries) throw new HelperError("network_error", `${this.origin} could not be reached: ${e instanceof Error ? e.message : String(e)}`);
        await this.wait(500 * 2 ** attempt);
        continue;
      }
      const text = await res.text();
      let body = null;
      try {
        body = text ? JSON.parse(text) : null;
      } catch {
        body = null;
      }
      if (res.ok) {
        if (!body || typeof body !== "object") throw new HelperError("unreadable", `${this.origin} answered with something that isn't FileFreak's.`);
        return body;
      }
      const code = body && typeof body.error === "string" ? body.error : `http_${res.status}`;
      if (res.status === 503 && code === "waking_up" && waking < 60) {
        waking++;
        attempt--;
        await this.wait(5000);
        continue;
      }
      if ([429, 502, 503, 504].includes(res.status) && attempt < this.retries) {
        await this.wait(500 * 2 ** attempt);
        continue;
      }
      const message = body && typeof body.message === "string" ? body.message : `${this.origin} answered ${res.status}.`;
      throw new HelperError(res.status === 404 ? "not_found" : code, message, { status: res.status });
    }
  }

  /** Everything in a public folder, a page at a time. */
  async *items(folderId) {
    for (let offset = 0; offset !== null; ) {
      const page = await this.ask(`/api/public/files/${encodeURIComponent(folderId)}/items?offset=${offset}&limit=${PAGE}`);
      if (!Array.isArray(page.items)) throw new HelperError("unreadable", `${this.origin} answered a listing with no items.`);
      for (const item of page.items) yield item;
      const next = page.nextOffset;
      offset = typeof next === "number" && next > offset ? next : null;
    }
  }

  /** The item called `name` in a folder: exactly, or failing that ignoring case, as FileFreak's names do. */
  async child(folderId, name) {
    let loose = null;
    for await (const item of this.items(folderId)) {
      if (item.name === name) return item;
      if (!loose && typeof item.name === "string" && item.name.toLowerCase() === name.toLowerCase()) loose = item;
    }
    return loose;
  }

  /** The item at `path` under a folder, or null when any part of it isn't there. */
  async find(folderId, path) {
    const segs = segmentsOf(path);
    let at = { id: folderId, kind: "folder" };
    for (let i = 0; i < segs.length; i++) {
      if (at.kind !== "folder") return null;
      const next = await this.child(at.id, segs[i]);
      if (!next) return null;
      at = next;
    }
    return at;
  }
}

/** What the factory is told about a file in the archive. */
export function describe(item) {
  return {
    id: item.id,
    name: item.name,
    kind: item.kind,
    size: typeof item.sizeBytes === "number" ? item.sizeBytes : 0,
    sha256: typeof item.clientSha256 === "string" && SHA256.test(item.clientSha256.toLowerCase()) ? item.clientSha256.toLowerCase() : null,
    protected: item.protected === true,
    packed: item.packed === true,
    url: typeof item.publicUrl === "string" ? item.publicUrl : null,
    created_at: typeof item.createdAt === "string" ? item.createdAt : null,
  };
}

/** lookup: the file at `path` in the archive, or found: false. */
export async function lookup(site, archive, path) {
  const item = await site.find(archive.folderId, path);
  if (!item || item.kind !== "file") return { found: false };
  return { found: true, file: describe(item) };
}

/** A sink that keeps a small file in memory. */
function memorySink(most) {
  const chunks = new Map();
  let size = 0;
  return {
    async write(position, data) {
      if (position + data.byteLength > most) throw new HelperError("too_big", "That file is too big to be a provenance record.");
      chunks.set(position, new Uint8Array(data));
      size = Math.max(size, position + data.byteLength);
    },
    async read(position, length) {
      return this.bytes().subarray(position, position + length);
    },
    bytes() {
      const out = new Uint8Array(size);
      for (const [at, c] of chunks) out.set(c, at);
      return out;
    },
  };
}

/** A public file's page, which get() reads it through. */
const pageOf = (archive, id) => `${archive.origin}/public/${archive.username}/${id}`;

/** A small public file of the archive, as JSON, checked as every download is. */
async function readJSON(archive, file, getter) {
  if (file.size > MOST_PROVENANCE_BYTES) throw new HelperError("too_big", "That file is too big to be a provenance record.");
  const sink = memorySink(MOST_PROVENANCE_BYTES);
  const got = await getter(pageOf(archive, file.id), sink);
  const v = got.files[0]?.verification;
  if (!v || !v.verified) throw new HelperError("not_verified", "The provenance record has no checksum to check it against.");
  return JSON.parse(new TextDecoder().decode(sink.bytes()));
}

/**
 * candidates: for a file whose revision isn't known (Hugging Face can't be
 * reached, or no longer has it), each archived revision of the repository
 * that holds it, with its provenance record, newest first.
 */
export async function candidates(site, archive, repoDir, file, { getter = get, limit = MOST_REVISIONS } = {}) {
  const repo = await site.find(archive.folderId, repoDir);
  if (!repo || repo.kind !== "folder") return { candidates: [] };
  const revisions = [];
  for await (const item of site.items(repo.id)) if (item.kind === "folder") revisions.push(item);
  revisions.sort((a, b) => String(b.createdAt ?? "").localeCompare(String(a.createdAt ?? "")));
  const out = [];
  for (const rev of revisions.slice(0, limit)) {
    const found = await site.find(rev.id, file);
    if (!found || found.kind !== "file") continue;
    const segs = segmentsOf(file);
    const dir = segs.length > 1 ? await site.find(rev.id, segs.slice(0, -1).join("/")) : { id: rev.id };
    const record = dir ? await site.child(dir.id, segs[segs.length - 1] + PROVENANCE_SUFFIX) : null;
    let provenance = null;
    let problem = null;
    if (record && record.kind === "file") {
      try {
        provenance = await readJSON(archive, describe(record), getter);
      } catch (e) {
        problem = e instanceof Error ? e.message : String(e);
      }
    } else {
      problem = "no provenance record";
    }
    out.push({ revision: rev.name, file: describe(found), provenance, problem });
  }
  out.sort((a, b) => String(b.provenance?.archived_at ?? "").localeCompare(String(a.provenance?.archived_at ?? "")));
  return { candidates: out };
}

/**
 * download: one archived file to `out`, through its public page. The SDK
 * hashes what it writes and compares it with the SHA-256 the file was
 * uploaded with; this then holds it to `expect`, the SHA-256 the factory got
 * from Hugging Face (or the provenance record), and removes it if it differs.
 */
export async function download(archive, id, out, expect, { getter = get, emit = () => {}, signal } = {}) {
  if (!UUID.test(id)) throw new HelperError("bad_id", "That isn't a FileFreak file id.");
  if (!SHA256.test(String(expect))) throw new HelperError("bad_sha256", "Give the SHA-256 the file must have.");
  let last = 0;
  let got;
  try {
    got = await getter(pageOf(archive, id), await toPath(out), {
      signal,
      onProgress: (written, total) => {
        // Every 16 MiB or so, and at the end: enough for a progress line.
        if (written - last >= 16 << 20 || written === total) {
          last = written;
          emit({ event: "progress", done: written, total });
        }
      },
      onVerifying: (_file, checked, total) => emit({ event: "verifying", done: checked, total }),
    });
  } catch (e) {
    if (e instanceof IntegrityError) throw new HelperError("integrity_failed", e.message, { expected: e.expected, received: e.received });
    throw e;
  }
  const saved = got.files[0];
  const v = saved?.verification;
  if (!v || !v.verified || v.by !== "sha256") {
    await rm(out, { force: true });
    throw new HelperError("not_verified", "FileFreak has no SHA-256 for that file, so it couldn't be checked. It wasn't kept.");
  }
  if (v.sha256 !== expect) {
    await rm(out, { force: true });
    throw new HelperError("sha256_mismatch", `The archived file is ${v.sha256}, not ${expect}. It wasn't kept.`, { received: v.sha256 });
  }
  return { size: saved.sizeBytes, sha256: v.sha256, verification: v };
}

// -- With the upload key ------------------------------------------------------------

/** Whether a FileFreak refusal says the thing isn't there. */
const isNotFound = (e) => e instanceof FileFreakError && (e.status === 404 || e.code === "not_found");

/**
 * ensure-folder: the archive's top folder, made if it isn't there, public and
 * with Shard Protection off, and the link readers use for it.
 */
export async function ensureFolder(ff, name, linksOrigin = DEFAULT_LINKS_ORIGIN) {
  const me = await ff.me.get();
  let { item } = await ff.folders.create({ name, parentId: null, ifExists: "use" });
  if (item.visibility !== "public" || item.protection !== "off") {
    ({ item } = await ff.files.update(item.id, { visibility: "public", protection: "off" }));
  }
  const username = me.account.username;
  return {
    id: item.id,
    name: item.name,
    username,
    url: `${linksOrigin.replace(/\/+$/, "")}/public/${username}/${item.id}`,
    // false while the account's public page is switched off: the folder is
    // public, but nobody can see it until it is on.
    public: item.public === true,
    protection: item.protection,
    key_folder: me.folderName ?? null,
  };
}

/** exists: the file at `path` from the top of the key's drive (FileFreak finds names whatever their case). */
export async function exists(ff, path) {
  segmentsOf(path);
  try {
    const { item } = await ff.files.byPath({ path });
    if (item.kind !== "file") return { found: false };
    return { found: true, file: describe(item) };
  } catch (e) {
    if (isNotFound(e)) return { found: false };
    throw e;
  }
}

/** Each folder of `dir` under `parentId`, made where it isn't there; the last one's id. */
export async function ensurePath(ff, parentId, dir) {
  let at = parentId;
  for (const name of dir ? segmentsOf(dir) : []) {
    ({
      item: { id: at },
    } = await ff.folders.create({ name, parentId: at, ifExists: "use" }));
  }
  return at;
}

/** Whether an upload id can't be carried on (expired, cancelled, or gone), so it is begun again. */
const cannotCarryOn = (e) =>
  e instanceof FileFreakError && (e.status === 404 || e.status === 410 || e.code === "not_found" || e.code === "size_mismatch" || /^upload_/.test(e.code));

/**
 * upload: a file on disk into `dir` under the archive's top folder. With
 * `sha256`, an upload whose bytes hash to anything else is not completed.
 * With `resume`, the upload that stopped is carried on, or begun again when
 * it can't be. Emits begun (with the upload's id, to keep for a restart) and
 * progress.
 */
export async function upload(ff, { file, parentId, dir, name, sha256, resume, replace = false }, { emit = () => {}, signal, source = fromPath } = {}) {
  if (sha256 !== undefined && !SHA256.test(sha256)) throw new HelperError("bad_sha256", "That isn't a SHA-256.");
  if (!name || name.includes("/") || name.includes("\\")) throw new HelperError("bad_name", "Give the file's name in the archive.");
  const parent = await ensurePath(ff, parentId, dir);
  let last = 0;
  const options = {
    parentId: parent,
    name,
    onConflict: replace ? "replace" : "fail",
    signal,
    onBegun: (u) => emit({ event: "begun", upload_id: u.id }),
    onProgress: (sent, total) => {
      if (sent - last >= 16 << 20 || sent === total) {
        last = sent;
        emit({ event: "progress", done: sent, total });
      }
    },
  };
  if (sha256) options.sha256 = sha256;
  let item;
  try {
    if (resume) {
      try {
        item = await ff.upload(await source(file), { ...options, resume });
      } catch (e) {
        const cause = e instanceof UploadStopped ? e.cause : e;
        if (!cannotCarryOn(cause)) throw e;
        emit({ event: "restarted", reason: cause instanceof Error ? cause.message : String(cause) });
        item = await ff.upload(await source(file), options);
      }
    } else {
      item = await ff.upload(await source(file), options);
    }
  } catch (e) {
    if (e instanceof UploadStopped) {
      const cause = e.cause;
      throw new HelperError(cause instanceof FileFreakError ? cause.code : "upload_stopped", e.message, { upload_id: e.uploadId, status: cause instanceof FileFreakError ? cause.status : 0 });
    }
    throw e;
  }
  const out = describe(item);
  if (sha256 && out.sha256 !== sha256) {
    throw new HelperError("not_verified", out.protected ? "The file landed under Shard Protection, so it has no SHA-256 for readers to check: turn protection off on the archive folder." : "FileFreak kept a different SHA-256 for the file than the one it was sent with.");
  }
  return { file: out };
}
