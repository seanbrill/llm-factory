// A fake FileFreak site for the helper's tests: the public answers the SDK's
// get() and the helper's lookups read (/api/public/files/...), and a storage
// address that answers ranges. Nothing leaves the machine.

import { createHash, randomUUID } from "node:crypto";
import { createServer } from "node:http";

export const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

/**
 * A site holding a tree of folders and files. `tree` is nested objects: a
 * string or Uint8Array is a file, an object a folder. `sums` overrides the
 * SHA-256 a file is listed with (to fake a file changed in storage).
 */
export async function fakeSite(tree, { username = "archivist", sums = {} } = {}) {
  const items = new Map(); // id -> { item, parent, bytes }
  const children = new Map(); // folder id -> ids
  let clock = Date.parse("2026-11-01T00:00:00Z");
  const add = (name, parent, value) => {
    const id = randomUUID();
    const at = new Date((clock += 1000)).toISOString();
    if (typeof value === "string" || value instanceof Uint8Array) {
      const bytes = typeof value === "string" ? new TextEncoder().encode(value) : value;
      items.set(id, {
        parent,
        bytes,
        item: { id, kind: "file", name, sizeBytes: bytes.byteLength, contentType: "application/octet-stream", protected: false, packed: false, clientSha256: sums[name] ?? sha256(bytes), publicUrl: null, createdAt: at },
      });
    } else {
      items.set(id, { parent, item: { id, kind: "folder", name, sizeBytes: 0, contentType: null, protected: false, packed: false, clientSha256: null, publicUrl: null, createdAt: at } });
      children.set(id, []);
      for (const [n, v] of Object.entries(value)) children.get(id).push(add(n, id, v));
    }
    return id;
  };
  const root = add("open-model-archive", null, tree);
  const requests = [];

  const server = createServer((req, res) => {
    const url = new URL(req.url, "http://x");
    requests.push(`${req.method} ${url.pathname}${url.search}`);
    const json = (status, body) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };
    let m;
    if ((m = /^\/api\/public\/files\/([^/]+)$/.exec(url.pathname))) {
      const it = items.get(m[1]);
      if (!it) return json(404, { error: "not_found", message: "That file is not public, or does not exist." });
      return json(200, { item: it.item, crumbs: [], owner: { username, displayName: null } });
    }
    if ((m = /^\/api\/public\/files\/([^/]+)\/items$/.exec(url.pathname))) {
      const kids = children.get(m[1]);
      if (!kids) return json(404, { error: "not_found", message: "That file is not public, or does not exist." });
      const offset = Number(url.searchParams.get("offset") ?? 0);
      const limit = Math.min(2, Number(url.searchParams.get("limit") ?? 100)); // small pages, to walk them
      const page = kids.slice(offset, offset + limit).map((id) => items.get(id).item);
      return json(200, { folder: items.get(m[1]).item, crumbs: [], items: page, total: kids.length, nextOffset: offset + page.length < kids.length ? offset + page.length : null });
    }
    if ((m = /^\/api\/public\/files\/([^/]+)\/url$/.exec(url.pathname))) {
      const it = items.get(m[1]);
      if (!it || it.item.kind !== "file") return json(404, { error: "not_found", message: "Not found." });
      return json(200, { url: `${site.origin}/storage/${m[1]}`, expiresAt: new Date(Date.now() + 600_000).toISOString(), inline: false, contentType: "application/octet-stream" });
    }
    if ((m = /^\/storage\/([^/]+)$/.exec(url.pathname))) {
      const it = items.get(m[1]);
      if (!it) return json(404, { error: "not_found" });
      const r = /^bytes=(\d+)-(\d+)$/.exec(req.headers.range ?? "");
      if (!r) {
        res.writeHead(200, { "Content-Length": it.bytes.byteLength });
        return res.end(Buffer.from(it.bytes));
      }
      const from = Number(r[1]);
      const to = Math.min(Number(r[2]), it.bytes.byteLength - 1);
      res.writeHead(206, { "Content-Range": `bytes ${from}-${to}/${it.bytes.byteLength}`, "Content-Length": to - from + 1 });
      return res.end(Buffer.from(it.bytes.subarray(from, to + 1)));
    }
    json(404, { error: "not_found", message: `no route ${url.pathname}` });
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const site = {
    origin: `http://127.0.0.1:${server.address().port}`,
    root,
    username,
    requests,
    link: "",
    close: () => new Promise((resolve) => server.close(resolve)),
    /** The id of the item at a path under the root. */
    idOf(path) {
      let at = root;
      for (const seg of path.split("/")) {
        at = children.get(at).find((id) => items.get(id).item.name === seg);
        if (!at) return null;
      }
      return at;
    },
  };
  site.link = `${site.origin}/public/${username}/${root}`;
  return site;
}
