#!/usr/bin/env node
// The factory's FileFreak helper. The Go factory (internal/filefreak) runs it
// as a child process, one command per run:
//
//   node ff.mjs lookup        --archive <link> --path <a/b/file>
//   node ff.mjs candidates    --archive <link> --repo-dir <huggingface/org/repo> --file <path in repo>
//   node ff.mjs download      --archive <link> --id <file id> --out <path> --sha256 <hex>
//   node ff.mjs ensure-folder --name <folder> [--links-origin <https://filefreak.link>]
//   node ff.mjs exists        --path <folder/a/b/file>
//   node ff.mjs upload        --file <path> --parent <folder id> --dir <a/b> --name <name>
//                             [--sha256 <hex>] [--resume <upload id>] [--replace]
//
// The first three read the public archive with no key. The last three need
// the upload key, which comes only from the environment (FILEFREAK_KEY), never
// the command line, where other processes could read it. FILEFREAK_API_URL
// points the client at another API (a local stack).
//
// Stdout is one JSON object per line: {"event":"progress","done":n,"total":n},
// {"event":"verifying",...}, {"event":"begun","upload_id":...},
// {"event":"restarted","reason":...}, then {"event":"result","data":{...}}
// (exit 0) or {"event":"error","code":...,"message":...} (exit 1). A usage
// mistake exits 2.

import { FileFreak, FileFreakError } from "@filefreak.io/sdk";
import { parseArgs } from "node:util";
import { candidates, download, DEFAULT_LINKS_ORIGIN, ensureFolder, exists, HelperError, lookup, parseArchiveLink, PublicSite, upload } from "./lib.mjs";

const emit = (event) => process.stdout.write(JSON.stringify(event) + "\n");

const COMMANDS = {
  lookup: { archive: "string", path: "string" },
  candidates: { archive: "string", "repo-dir": "string", file: "string" },
  download: { archive: "string", id: "string", out: "string", sha256: "string" },
  "ensure-folder": { name: "string", "links-origin": "string" },
  exists: { path: "string" },
  upload: { file: "string", parent: "string", dir: "string", name: "string", sha256: "string", resume: "string", replace: "boolean" },
};
const OPTIONAL = new Set(["links-origin", "sha256:upload", "resume", "replace", "dir"]);

function usage(message) {
  process.stderr.write(`ff.mjs: ${message}\n`);
  process.exit(2);
}

function client() {
  const apiKey = process.env.FILEFREAK_KEY;
  if (!apiKey) throw new HelperError("no_key", "This needs the FileFreak upload key.");
  const baseUrl = process.env.FILEFREAK_API_URL || undefined;
  return new FileFreak({ apiKey, ...(baseUrl ? { baseUrl } : {}) });
}

async function run(command, v, signal) {
  switch (command) {
    case "lookup": {
      const archive = parseArchiveLink(v.archive);
      return await lookup(new PublicSite(archive.origin), archive, v.path);
    }
    case "candidates": {
      const archive = parseArchiveLink(v.archive);
      return await candidates(new PublicSite(archive.origin), archive, v["repo-dir"], v.file);
    }
    case "download": {
      const archive = parseArchiveLink(v.archive);
      return await download(archive, v.id, v.out, v.sha256.toLowerCase(), { emit, signal });
    }
    case "ensure-folder":
      return await ensureFolder(client(), v.name, v["links-origin"] || process.env.FILEFREAK_LINKS_ORIGIN || DEFAULT_LINKS_ORIGIN);
    case "exists":
      return await exists(client(), v.path);
    case "upload":
      return await upload(
        client(),
        { file: v.file, parentId: v.parent, dir: v.dir ?? "", name: v.name, sha256: v.sha256?.toLowerCase(), resume: v.resume, replace: v.replace === true },
        { emit, signal }
      );
  }
  throw new Error(`unknown command ${command}`);
}

async function main() {
  const [command, ...rest] = process.argv.slice(2);
  const spec = COMMANDS[command];
  if (!spec) usage(`the command is one of ${Object.keys(COMMANDS).join(", ")}`);
  let values;
  try {
    ({ values } = parseArgs({ args: rest, options: Object.fromEntries(Object.entries(spec).map(([k, type]) => [k, { type }])), strict: true }));
  } catch (e) {
    usage(e.message);
  }
  for (const [k, type] of Object.entries(spec)) {
    const optional = OPTIONAL.has(k) || OPTIONAL.has(`${k}:${command}`) || type === "boolean";
    if (!optional && !values[k]) usage(`${command} needs --${k}`);
  }

  // The factory stops a command by ending the process: stop what is in flight
  // so a download's .part is removed and an upload keeps what it sent.
  const stop = new AbortController();
  for (const sig of ["SIGTERM", "SIGINT"]) process.once(sig, () => stop.abort(new HelperError("stopped", "Stopped.")));

  try {
    emit({ event: "result", data: await run(command, values, stop.signal) });
    process.exitCode = 0;
  } catch (e) {
    const out = { event: "error", code: "failed", message: e instanceof Error ? e.message : String(e) };
    if (e instanceof HelperError || e instanceof FileFreakError) {
      out.code = e.code;
      if (typeof e.status === "number") out.status = e.status;
      if (typeof e.upload_id === "string") out.upload_id = e.upload_id;
      if (typeof e.received === "string") out.received = e.received;
    }
    if (stop.signal.aborted) out.code = "stopped";
    emit(out);
    process.exitCode = 1;
  }
}

await main();
