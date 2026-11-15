// The helper against a fake FileFreak: the public reads through a local site
// (the SDK's real get(), real ranges, real checksums), and the keyed calls
// through a fake client. Run with `npm test` in tools/filefreak.

import { FileFreakError, UploadStopped } from "@filefreak.io/sdk";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { existsSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { after, before, describe, it } from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { candidates, download, ensureFolder, exists, HelperError, lookup, parseArchiveLink, PublicSite, upload } from "../lib.mjs";
import { fakeSite, sha256 } from "./fakesite.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const weights = new Uint8Array(3 * 1024 * 1024).map((_, i) => (i * 7) % 251);
const COMMIT = "0123456789abcdef0123456789abcdef01234567";
const OLDER = "fedcba9876543210fedcba9876543210fedcba98";
const provenance = (revision, archivedAt, sum = sha256(weights)) =>
  JSON.stringify({ repo: "org/model-GGUF", ref: "main", revision, file: "model-Q4.gguf", sha256: sum, size: weights.byteLength, archived_at: archivedAt });

describe("parseArchiveLink", () => {
  it("reads a public folder's link", () => {
    assert.deepEqual(parseArchiveLink("https://filefreak.link/public/Archivist/0A1B2C3D-0000-4000-8000-000000000001"), {
      origin: "https://filefreak.link",
      username: "Archivist",
      folderId: "0a1b2c3d-0000-4000-8000-000000000001",
    });
  });
  for (const bad of [
    "http://filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001",
    "https://filefreak.link/share/abcdefghijklmnopqrstuvwxyz",
    "https://filefreak.link/public/archivist",
    "https://user:pw@filefreak.link/public/archivist/0a1b2c3d-0000-4000-8000-000000000001",
    "not a link",
  ]) {
    it(`refuses ${bad}`, () => assert.throws(() => parseArchiveLink(bad), (e) => e instanceof HelperError && e.code === "bad_archive_link"));
  }
});

describe("reading the public archive", () => {
  let site;
  let archive;
  let dir;
  before(async () => {
    site = await fakeSite(
      {
        huggingface: {
          org: {
            "model-GGUF": {
              [OLDER]: { "model-Q4.gguf": weights, "model-Q4.gguf.provenance.json": provenance(OLDER, "2026-10-01T00:00:00Z") },
              [COMMIT]: {
                "model-Q4.gguf": weights,
                "model-Q4.gguf.provenance.json": provenance(COMMIT, "2026-11-01T00:00:00Z"),
                "README.md": "# model",
                sub: { "Other.gguf": "abc" },
              },
            },
            changed: { [COMMIT]: { "x.gguf": "the bytes in storage" } },
          },
        },
      },
      { sums: { "x.gguf": sha256("the bytes that were uploaded") } }
    );
    archive = parseArchiveLink(site.link);
    dir = await mkdtemp(join(tmpdir(), "ff-helper-"));
  });
  after(async () => {
    await site.close();
    await rm(dir, { recursive: true, force: true });
  });

  it("finds a file by its path, page by page", async () => {
    const got = await lookup(new PublicSite(site.origin), archive, `huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf`);
    assert.equal(got.found, true);
    assert.equal(got.file.size, weights.byteLength);
    assert.equal(got.file.sha256, sha256(weights));
    assert.equal(got.file.id, site.idOf(`huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf`));
  });

  it("finds a file in a sub-folder, and a name whatever its case", async () => {
    const got = await lookup(new PublicSite(site.origin), archive, `huggingface/ORG/model-gguf/${COMMIT}/sub/other.gguf`);
    assert.equal(got.found, true);
    assert.equal(got.file.name, "Other.gguf");
  });

  it("says when a file isn't there", async () => {
    for (const path of [`huggingface/org/model-GGUF/${COMMIT}/missing.gguf`, "huggingface/nobody/x/y/z.gguf", `huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf/deeper`]) {
      assert.deepEqual(await lookup(new PublicSite(site.origin), archive, path), { found: false }, path);
    }
  });

  it("refuses a path that steps outside", async () => {
    await assert.rejects(lookup(new PublicSite(site.origin), archive, "huggingface/../x"), (e) => e.code === "bad_path");
  });

  it("downloads a file checked against the SHA-256 it must have", async () => {
    const out = join(dir, "model.gguf");
    const events = [];
    const got = await download(archive, site.idOf(`huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf`), out, sha256(weights), { emit: (e) => events.push(e) });
    assert.equal(got.sha256, sha256(weights));
    assert.deepEqual(got.verification, { verified: true, by: "sha256", sha256: sha256(weights) });
    assert.deepEqual(new Uint8Array(await readFile(out)), weights);
    assert.ok(events.some((e) => e.event === "progress" && e.done === weights.byteLength));
    assert.equal(existsSync(out + ".part"), false);
  });

  it("keeps nothing when the archived file isn't the SHA-256 the factory expects", async () => {
    const out = join(dir, "wrong.gguf");
    await assert.rejects(download(archive, site.idOf(`huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf`), out, "a".repeat(64)), (e) => e.code === "sha256_mismatch");
    assert.equal(existsSync(out), false);
    assert.equal(existsSync(out + ".part"), false);
  });

  it("keeps nothing when storage doesn't hold what was uploaded", async () => {
    const out = join(dir, "changed.gguf");
    await assert.rejects(download(archive, site.idOf(`huggingface/org/changed/${COMMIT}/x.gguf`), out, sha256("the bytes that were uploaded")), (e) => e.code === "integrity_failed");
    assert.equal(existsSync(out), false);
    assert.equal(existsSync(out + ".part"), false);
  });

  it("lists the archived revisions of a file with their provenance, newest first", async () => {
    const got = await candidates(new PublicSite(site.origin), archive, "huggingface/org/model-GGUF", "model-Q4.gguf");
    assert.deepEqual(
      got.candidates.map((c) => [c.revision, c.provenance?.revision, c.file.sha256]),
      [
        [COMMIT, COMMIT, sha256(weights)],
        [OLDER, OLDER, sha256(weights)],
      ]
    );
  });

  it("lists no candidates for a repository the archive doesn't have", async () => {
    assert.deepEqual(await candidates(new PublicSite(site.origin), archive, "huggingface/nobody/nothing", "model-Q4.gguf"), { candidates: [] });
  });

  it("runs as a command: JSON lines on stdout", async () => {
    const out = join(dir, "cli.gguf");
    const { stdout } = await promisify(execFile)(process.execPath, [
      join(here, "..", "ff.mjs"),
      "download",
      "--archive",
      site.link,
      "--id",
      site.idOf(`huggingface/org/model-GGUF/${COMMIT}/model-Q4.gguf`),
      "--out",
      out,
      "--sha256",
      sha256(weights).toUpperCase(),
    ]);
    const lines = stdout.trim().split("\n").map((l) => JSON.parse(l));
    assert.equal(lines.at(-1).event, "result");
    assert.equal(lines.at(-1).data.sha256, sha256(weights));
  });

  it("a failing command says why, with its code, and exits 1", async () => {
    await assert.rejects(
      promisify(execFile)(process.execPath, [join(here, "..", "ff.mjs"), "lookup", "--archive", "https://example.com/nope", "--path", "a"]),
      (e) => e.code === 1 && JSON.parse(e.stdout.trim()).code === "bad_archive_link"
    );
  });

  it("a usage mistake exits 2", async () => {
    await assert.rejects(promisify(execFile)(process.execPath, [join(here, "..", "ff.mjs"), "lookup", "--archive", site.link]), (e) => e.code === 2);
  });

  it("the keyed commands need the key from the environment", async () => {
    const env = { ...process.env };
    delete env.FILEFREAK_KEY;
    await assert.rejects(
      promisify(execFile)(process.execPath, [join(here, "..", "ff.mjs"), "exists", "--path", "a/b"], { env }),
      (e) => e.code === 1 && JSON.parse(e.stdout.trim()).code === "no_key"
    );
  });
});

/** A fake client: folders by name under their parent, and uploads that do what they're told. */
function fakeClient({ uploadImpl, folderPublic = true } = {}) {
  const folders = new Map(); // `${parent}/${name}` -> item
  const calls = [];
  let n = 0;
  const folder = (name, parentId, extra = {}) => ({ id: `f${++n}`, kind: "folder", name, parentId, visibility: "inherit", protection: "inherit", public: false, ...extra });
  const ff = {
    calls,
    me: { get: async () => ({ via: "key", account: { username: "archivist" }, folderId: null, folderName: null }) },
    folders: {
      create: async (body) => {
        calls.push(["folders.create", body]);
        const key = `${body.parentId}/${body.name}`;
        if (!folders.has(key)) folders.set(key, folder(body.name, body.parentId));
        return { item: folders.get(key) };
      },
    },
    files: {
      update: async (id, body) => {
        calls.push(["files.update", id, body]);
        const f = [...folders.values()].find((x) => x.id === id);
        Object.assign(f, body, { public: folderPublic });
        return { item: f };
      },
      byPath: async (query) => {
        calls.push(["files.byPath", query]);
        if (query.path === "open-model-archive/huggingface/org/m/rev/model.gguf") {
          return { item: { id: "file1", kind: "file", name: "model.gguf", sizeBytes: 3, clientSha256: "b".repeat(64), protected: false } };
        }
        throw new FileFreakError(404, "not_found", "That file or folder does not exist.");
      },
    },
    upload: async (source, options) => {
      calls.push(["upload", options]);
      return uploadImpl(source, options);
    },
  };
  return ff;
}

describe("writing with the key", () => {
  it("makes the archive folder public with protection off, and gives its link", async () => {
    const ff = fakeClient();
    const got = await ensureFolder(ff, "open-model-archive", "https://filefreak.link/");
    assert.equal(got.url, `https://filefreak.link/public/archivist/${got.id}`);
    assert.equal(got.public, true);
    assert.deepEqual(ff.calls[0], ["folders.create", { name: "open-model-archive", parentId: null, ifExists: "use" }]);
    assert.deepEqual(ff.calls[1], ["files.update", got.id, { visibility: "public", protection: "off" }]);
  });

  it("says when the folder isn't visible yet (the account's public page is off)", async () => {
    const got = await ensureFolder(fakeClient({ folderPublic: false }), "open-model-archive");
    assert.equal(got.public, false);
  });

  it("finds a file by its path, and says when it isn't there", async () => {
    const ff = fakeClient();
    assert.equal((await exists(ff, "open-model-archive/huggingface/org/m/rev/model.gguf")).file.sha256, "b".repeat(64));
    assert.deepEqual(await exists(ff, "open-model-archive/huggingface/org/m/rev/other.gguf"), { found: false });
  });

  it("uploads into its folders, made as needed, with the SHA-256 it must hash to", async () => {
    const sum = "c".repeat(64);
    const ff = fakeClient({
      uploadImpl: async (_source, options) => {
        options.onBegun({ id: "up1" });
        options.onProgress(3, 3);
        return { id: "file9", kind: "file", name: options.name, sizeBytes: 3, clientSha256: sum, protected: false };
      },
    });
    const events = [];
    const got = await upload(ff, { file: "/x/model.gguf", parentId: "root", dir: "huggingface/org/m/rev/sub", name: "model.gguf", sha256: sum }, { emit: (e) => events.push(e), source: async (p) => p });
    assert.equal(got.file.sha256, sum);
    const made = ff.calls.filter((c) => c[0] === "folders.create").map((c) => c[1].name);
    assert.deepEqual(made, ["huggingface", "org", "m", "rev", "sub"]);
    const up = ff.calls.find((c) => c[0] === "upload")[1];
    assert.equal(up.sha256, sum);
    assert.equal(up.onConflict, "fail");
    assert.equal(up.parentId, "f5");
    assert.deepEqual(events, [
      { event: "begun", upload_id: "up1" },
      { event: "progress", done: 3, total: 3 },
    ]);
  });

  it("carries on a stopped upload, and begins again when it can't be", async () => {
    const tries = [];
    const ff = fakeClient({
      uploadImpl: async (_s, options) => {
        tries.push(options.resume ?? null);
        if (options.resume) throw new FileFreakError(404, "not_found", "That upload does not exist.");
        return { id: "f", kind: "file", name: options.name, sizeBytes: 1, clientSha256: "d".repeat(64), protected: false };
      },
    });
    const events = [];
    await upload(ff, { file: "/x", parentId: "root", dir: "", name: "a.gguf", sha256: "d".repeat(64), resume: "old-upload" }, { emit: (e) => events.push(e), source: async (p) => p });
    assert.deepEqual(tries, ["old-upload", null]);
    assert.equal(events[0].event, "restarted");
  });

  it("an upload that stops says which upload to carry on", async () => {
    const ff = fakeClient({
      uploadImpl: async () => {
        throw new UploadStopped("up7", new FileFreakError(0, "network_error", "The API could not be reached."));
      },
    });
    await assert.rejects(upload(ff, { file: "/x", parentId: "root", name: "a.gguf" }, { source: async (p) => p }), (e) => e instanceof HelperError && e.upload_id === "up7" && e.code === "network_error");
  });

  it("refuses a file that landed without the SHA-256 it was sent with", async () => {
    const ff = fakeClient({ uploadImpl: async (_s, o) => ({ id: "f", kind: "file", name: o.name, sizeBytes: 1, clientSha256: null, protected: true }) });
    await assert.rejects(upload(ff, { file: "/x", parentId: "root", name: "a.gguf", sha256: "e".repeat(64) }, { source: async (p) => p }), (e) => e.code === "not_verified" && /Shard Protection/.test(e.message));
  });
});
