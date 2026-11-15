# The FileFreak model archive

The factory downloads model files from Hugging Face. The **FileFreak archive** is a
public folder on [FileFreak.io](https://filefreak.io) that holds copies of the
open-licensed files among them. It works like a mirror: if a repository is renamed,
a file is replaced or Hugging Face is down, a build can still get the exact file it
asked for.

- **Reading** needs no account and no key. Before each download the factory looks
  in the archive. It uses the archive's copy only when that copy's SHA-256 matches
  the one Hugging Face publishes. Anything else downloads from Hugging Face, as
  before.
- **Uploading** happens only on installs with a FileFreak **upload key**. After a
  build downloads a file from Hugging Face that the archive doesn't have yet, the
  factory uploads it in the background, but only if the model's license allows
  redistribution.

Everything is on the **System** page, in the **FileFreak archive** card
(`#system/archive`, or search "FileFreak" with ⌘K).

Contents: [Reading](#reading-from-the-archive) · [Uploading](#uploading-with-a-key) ·
[License rule](#the-license-rule) · [What FileFreak sees](#what-filefreak-sees) ·
[Layout](#archive-layout) · [Setup](#setup) · [Settings and environment](#settings-and-environment) ·
[Troubleshooting](#troubleshooting)

## Reading from the archive

For every model file in the catalog (every `huggingface.co/<org>/<repo>/resolve/…` URL):

1. The factory asks Hugging Face for the file's current **revision** (commit),
   **SHA-256** and size with a `HEAD` request. Nothing is downloaded yet.
2. It looks for `huggingface/<org>/<repo>/<revision>/<file>` in the archive. If the
   archived file's SHA-256 and size match Hugging Face's, it downloads that copy
   with the FileFreak SDK. The SDK checks the bytes against the SHA-256 the file
   was uploaded with, and the factory checks that this is Hugging Face's SHA-256.
3. If the file isn't archived, doesn't match, or anything about the archive fails
   (FileFreak down, Node.js missing, a timeout), the build downloads from Hugging
   Face. That download is now checked against Hugging Face's SHA-256 too: a file
   that doesn't match is deleted and the build stops with a message that says so.

**When Hugging Face can't answer** (it's down, or the file or repository is gone),
the factory looks in the archive for the newest revision of that file that has a
**provenance record**. It uses the copy only when the record names the same
repository and file, the record's SHA-256 and size match the archived file, and the
SDK's check passes. The build log says plainly that Hugging Face didn't confirm it.

The build log always says which source was used:

```
Hugging Face: Qwen3.5-4B-Q4_K_M.gguf is at revision 8f3a2c1, SHA-256 1d6c0e9a4b2f…, 2.7 GB.
Downloading Qwen3.5-4B-Q4_K_M.gguf from the FileFreak archive (2.7 GB) ...
Saved Qwen3.5-4B-Q4_K_M.gguf to models/ from the FileFreak archive: its SHA-256 matches the one Hugging Face publishes.
```

or `FileFreak archive: … isn't archived yet. Downloading from Hugging Face.`
followed by `Saved … to models/ from huggingface.co (SHA-256 checked)`.

The archive doesn't depend on the license: any file in it can be read. Turn reading
off with **Download from the archive first** on the System page (it's on by
default). With no archive location and no key, the factory doesn't contact FileFreak
at all.

## Uploading with a key

Paste a FileFreak key on the System page (**Upload key**). From then on:

1. After a build downloads a file from Hugging Face, and its SHA-256 matched what
   Hugging Face publishes, the file is **queued** if the archive doesn't have it.
   The build doesn't wait: uploads run in the background, one at a time.
2. The upload first checks the [license rule](#the-license-rule) for the model at
   that revision. A file that may not be redistributed is marked **Not archived**,
   with the reason.
3. It uploads the repository's LICENSE / NOTICE / README.md files, then the model
   file, then the provenance record, all into
   `open-model-archive/huggingface/<org>/<repo>/<revision>/`. Every file is
   **public with no Shard Protection**, so anyone can download it with the SDK and no
   key.

The queue is saved in `config/filefreak-queue.json`. An upload that was cut off
resumes where it stopped after a restart. One that fails is tried again after 1,
4, 16 and 64 minutes, then 4 hours: six tries in all, then it shows as **Failed**. The card shows each file's
progress and errors, with **Retry** and **Remove**. Removing the key stops uploading;
reading carries on.

The key's folder (`open-model-archive` at the top of the key's space) is created
public if it isn't there. Its public link becomes the archive this install reads
from, and the card shows it.

## The license rule

A file is uploaded only when **both** are true of its Hugging Face repository at
that revision:

- it isn't **gated** (no "agree to access" form) and isn't private, and
- its license is on this list:
  `apache-2.0`, `mit`, `bsd-2-clause`, `bsd-3-clause`, `cc-by-4.0`, `cc-by-sa-4.0`,
  `cc0-1.0`, and the OpenRAIL family (`openrail`, `openrail++`,
  `creativeml-openrail-m`, `bigscience-openrail-m`, `bigcode-openrail-m`).

Custom licenses are never uploaded: Llama, Gemma, `other`, a repository with no
license, or anything else not on the list. The license is read from the
repository's model card on Hugging Face (its `license:` field). It goes into the
provenance record, next to the LICENSE and README files.

The list is `Redistributable` in `internal/filefreak/hf.go`. Reading from the
archive isn't limited by it.

## What FileFreak sees

**Reading (any install, no key).** The factory reads the archive's public folder
listing and downloads files from it. From those requests FileFreak can tell which
model repositories, revisions and files this install looks up, and from which IP
address. It gets no account, key, or anything about your computer or your models'
use.

**Uploading (only with a key).** As well as the above, it sends the names of the
models being downloaded (as folder and file names), the files themselves, their
LICENSE / NOTICE / README.md files, and a provenance record per file:

```json
{
  "schema": "llm-factory.archive.provenance/1",
  "source": "huggingface",
  "repo": "unsloth/Qwen3.5-4B-GGUF",
  "ref": "main",
  "revision": "8f3a2c1d…",
  "file": "Qwen3.5-4B-Q4_K_M.gguf",
  "sha256": "1d6c0e9a…",
  "size": 2700000000,
  "source_url": "https://huggingface.co/unsloth/Qwen3.5-4B-GGUF/resolve/8f3a2c1d…/Qwen3.5-4B-Q4_K_M.gguf",
  "license": "apache-2.0",
  "notices": ["LICENSE", "README.md"],
  "archived_at": "2026-11-15T19:02:11Z",
  "archived_by": "llm-factory 1.0.41"
}
```

The factory also asks **Hugging Face** for each file's revision and SHA-256 (a
`HEAD` request) before downloading, and, before an upload, for the model's license.
Hugging Face sees the download anyway.

## Archive layout

```
open-model-archive/                          the archive's public folder
  huggingface/
    <org>/<repo>/<revision>/                 one folder per commit
      <file>                                 e.g. Qwen3.5-4B-Q4_K_M.gguf, or split_files/vae/…
      <file>.provenance.json                 where it came from (above)
      LICENSE…, NOTICE…, README.md           the repository's notices at that revision
```

Paths keep Hugging Face's own names and sub-folders. A new revision of a file is a
new folder, so older revisions stay.

## Setup

**Node.js.** The archive runs through a small Node.js helper,
`tools/filefreak/ff.mjs`, which uses the FileFreak SDK
(`@filefreak.io/sdk`, pinned to 0.20.0 by `package-lock.json`).

- **Docker (start.sh / start.ps1):** nothing to do. The factory image includes
  Node.js and the helper.
- **Dev (start-dev):** the dev image includes Node.js; the start script installs
  the SDK into `tools/filefreak/node_modules`.
- **Native (start-native):** install Node.js 20 or later. The start script runs
  `npm ci` in `tools/filefreak` when needed. Without Node.js it prints a warning and
  the factory downloads from Hugging Face only.

By hand: `cd tools/filefreak && npm ci --omit=dev`.

**An archive to read.** Builds read from, in this order:

1. `FILEFREAK_ARCHIVE_URL` in the factory's environment (the start scripts pass it
   into the container when it's set),
2. the upload key's own archive folder, when a key is set,
3. the location set on the System page,
4. the built-in default, `DefaultArchiveURL` in `internal/filefreak/archive.go`.

Each is the folder's **public link**: `https://filefreak.link/public/<username>/<folder id>`.
The built-in default is empty until there is a public archive to point at; set it
there, or with any of the first three.

**An upload key.** In FileFreak, make an API key that can create folders and upload
(it looks like `ffk_live_…`). The account's **public page must be on**, or its
public files can't be listed. Paste the key into **Upload key** on the System page.
The card then shows the key's folder link. If it says the folder isn't ready, follow
what it says, then click **Check again**.

The key is stored only in `config/filefreak.json`, written with mode `0600`. The
factory never sends it back to the browser (the card shows `ffk_live_…` and the last
four characters), never writes it to a log, and passes it to the helper in an
environment variable, not on its command line. **Replace** swaps it; **Remove**
deletes it from the file.

## Settings and environment

| Setting | Where | Default |
|---|---|---|
| Download from the archive first | System page | on |
| Archive location | System page, or `FILEFREAK_ARCHIVE_URL` | `DefaultArchiveURL` |
| Upload key | System page (`config/filefreak.json`, 0600) | none: read only |

Environment variables, mostly for development and tests:

| Variable | Use |
|---|---|
| `FILEFREAK_ARCHIVE_URL` | The archive to read; overrides everything else. |
| `FILEFREAK_NODE` | The Node.js binary (default: `node` on `PATH`). |
| `FILEFREAK_HELPER` | Another `ff.mjs` (default: `tools/filefreak/ff.mjs`). |
| `FILEFREAK_API_URL` | FileFreak's API for keyed calls (default: the SDK's, `https://api.filefreak.io`). |
| `FILEFREAK_LINKS_ORIGIN` | The origin of public links (default `https://filefreak.link`). |

The System page's API (behind the factory's access token, same-origin JSON only):
`GET /api/filefreak/status`, and `POST /api/filefreak/settings`
`{read_from_archive?, archive_url?}`, `/api/filefreak/key {key}`,
`/api/filefreak/key/remove`, `/api/filefreak/folder/check`,
`/api/filefreak/queue/retry {id}`, `/api/filefreak/queue/remove {id}`.

## Troubleshooting

- **"FileFreak archive: not checked (…)" in a build log.** The helper can't run.
  Usually Node.js is missing or older than 20, or `npm ci` hasn't run in
  `tools/filefreak`. The System card says which, with the command to fix it.
- **The key's folder isn't ready.** FileFreak didn't answer, the key can't create
  folders, or the account's public page is off. Fix that, then **Check again**.
  Uploads wait until the folder is found.
- **"Not archived: …" in the queue.** The license rule kept the file out (or the
  repository is gated or gone). That's expected; the reason says which.
- **An upload keeps failing.** The reason is on the card. **Retry** starts it again
  now; **Remove** drops it. Builds are never affected.
- **A file in the archive is wrong.** It won't be used: the factory uses an archived
  file only when its SHA-256 is the one Hugging Face publishes, or, with Hugging
  Face unreachable, when it matches its provenance record and the SDK's check.
