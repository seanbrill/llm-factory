# Agent harness: tools for Open WebUI

The **Harness** page lets the models you run in the factory do more than chat in
[Open WebUI](https://github.com/open-webui/open-webui). A model can:

- **search the web** and read pages (Open WebUI's built-in web search),
- **view and edit files and run commands** in a sandboxed Linux terminal that
  only sees one shared folder (the *workspace terminal*,
  [Open Terminal](https://github.com/open-webui/open-terminal)),
- **use skills**, which are written instructions it loads when a task matches,
  such as "how to make a git checkpoint".

The harness has four parts:

1. **Model gateway**: one OpenAI-compatible endpoint (`/v1`) on the factory for
   every running chat, code, reasoning, vision and embedding model. Open WebUI
   needs a single connection, and its model list follows what you run.
2. **Workspace terminal**: an Open Terminal container (`local-llm-terminal`) that
   the factory starts and manages for you.
3. **Skills**: the `skills/` folder in this repo, shared read-only with the
   terminal.
4. **Connect Open WebUI**: one click that adds the parts above to Open WebUI
   through its admin API and reports every change it makes.

Contents: [Quick start](#quick-start) · [How it fits together](#how-it-fits-together) ·
[The parts](#the-parts) · [What Connect changes](#what-connect-changes-in-open-webui) ·
[Manual setup](#manual-setup) · [Security model](#security-model) ·
[Harness API](#harness-api-reference) · [Troubleshooting](#troubleshooting)

## Quick start

You need the factory running in Docker Desktop (or Podman Desktop), plus
**Open WebUI 0.13.x** running in Docker, usually at <http://localhost:3000>.
The connector was written against the Open WebUI 0.13.3 API.

1. **Restart the factory with the current start script**
   (`./scripts/macos/start.sh`, `./scripts/linux/start.sh` or
   `.\scripts\windows\start.ps1`). The script now passes your home folder
   (`HOST_HOME`) and port (`FACTORY_PORT`), and mounts `skills/` and `workspace/`.
2. **Run a chat model**: Images → **Run**. Pick one that is good at tool calling,
   such as Qwen 3.x, Gemma 4 or a recent Mistral or Llama. Very small models often
   call tools badly.
3. **Start the terminal**: on the Harness page, click **Download (~430 MB) &
   start** under *Workspace terminal*. Only the first start downloads the Slim
   image.
4. **Get an admin API key from Open WebUI.** Admin Panel → Settings → General →
   turn on **Enable API Keys**. Then, as an admin user, go to Settings → Account →
   API keys → **Create new secret key**.
5. **Connect**: on the Harness page, under *Connect Open WebUI*, check the URL
   (the default is right for Open WebUI in Docker on port 3000), paste the key,
   then click **Check** and **Connect**. The result lists each change. The key is
   used for that one request and is never stored.
6. **Chat**: in Open WebUI, pick a factory model and select the
   **LLM Factory workspace** terminal in the chat. Turn on **Web Search** in the
   chat when you want the model to search. Try: *"Search the web for the latest
   llama.cpp release, then write a summary to notes/llama.md in the workspace."*

The model's files land in `workspace/` in this repo (or the folder you choose).

## How it fits together

```
                          your browser
                               │
                               ▼
          ┌────────── Open WebUI (Docker, :3000) ───────────┐
          │                      │                          │
  chat + embeddings       file + shell tools          web search: runs
  OpenAI API              Open Terminal API           inside Open WebUI
  Bearer sk-llmf-…        Bearer otk-…                (DuckDuckGo or the
          │                      │                    engine you chose)
          │ host.docker.internal │ host.docker.internal
          ▼                      ▼
  local-llm-factory        local-llm-terminal
  127.0.0.1:8799/v1        127.0.0.1:8798
  model gateway            Linux sandbox
          │                  ~/workspace       ◄── ./workspace       read-write
          ▼                  ~/.agents/skills  ◄── ./skills          read-only
  model containers           ~/.claude/skills  ◄── ~/.claude/skills  read-only, opt-in
  127.0.0.1:<port>
  (llama.cpp: chat, code, reasoning, vision, embeddings)
```

Everything listens on your computer's loopback interface only. Open WebUI runs in
a container, so it reaches the gateway and the terminal through
`host.docker.internal`, which Docker Desktop routes to your computer's localhost.

## The parts

### Model gateway

The factory serves an OpenAI-compatible API at `http://localhost:8799/v1`, or on
whatever port you started it with. Open WebUI uses
`http://host.docker.internal:8799/v1`.

| Method | Path | What it does |
|---|---|---|
| GET | `/v1/models` | Lists the running models. |
| GET | `/v1/models/{id}` | Returns one model. |
| POST | `/v1/chat/completions` | Chat, including streaming, tools and images for vision models. |
| POST | `/v1/completions` | Plain text completion. |
| POST | `/v1/embeddings` | Embeddings. Only embedding models accept this. |

- **Auth**: every call needs `Authorization: Bearer <gateway key>`. The key
  (`sk-llmf-…`) is generated on first use and shown on the Harness page, where
  **Regenerate** replaces it.
- **Which models**: running factory containers served by llama.cpp (text, code,
  reasoning, vision and embedding). Image, video and speech models aren't listed.
  Neither are Ensembles yet, because their Conductor starts and stops its
  specialists on demand.
- **Model ids** are stable and readable: the image name without its registry
  path, so `local-llm/qwen3.5-9b:latest` becomes `qwen3.5-9b`. A tag other than
  `latest` is kept, as in `qwen3.5-9b:fast`. If you run the same image twice, the
  ids become `qwen3.5-9b@18080` and `qwen3.5-9b@18081`, and plain `qwen3.5-9b`
  still routes to the lowest port. The container name and image reference also
  work as aliases.
- **Routing**: the gateway reads only the `model` field, then forwards the
  request body unchanged to that model's container. Streaming (SSE) is passed
  through as it arrives, without buffering. Your `Authorization` header is never
  forwarded.
- **Errors** use OpenAI's format. An unknown model returns 404 `model_not_found`
  and lists the running ids. Sending chat to an embedding model, or the reverse,
  returns 400.

### Workspace terminal

The `local-llm-terminal` container runs `ghcr.io/open-webui/open-terminal`. In
Open WebUI it gives the model file tools (`list_files`, `read_file`,
`write_file`, `replace_file_content`, `grep_search`, `glob_search`,
`display_file`) and shell tools (`run_command`, `get_process_status`,
`send_process_input`, `kill_process`, `list_processes`).

| Setting | Default | Notes |
|---|---|---|
| Image | **Slim** (`:slim`, about 430 MB) | Debian with git, curl and jq. Browser and desktop are off. **Full** (`:latest`, several GB) adds Python, Node.js, build tools, a browser and sudo. |
| Workspace folder | `<repo>/workspace` | Mounted read-write as `~/workspace`, where every command starts. |
| Share `~/.claude/skills` | off | Mounts your Claude skills read-only as `~/.claude/skills`. Needs `HOST_HOME`, which the start scripts pass. |
| Port | `8798` | Published on `127.0.0.1` only. |
| Memory limit | 2048 MB (Slim), 4096 MB (Full) | There is also a limit of 1024 processes. |

- **Lifecycle**: the Harness page can Start, Stop, Restart, **Update image**
  (pull the newest image and recreate the container), show Logs (keys are
  hidden) and Remove the terminal. The container uses `--restart unless-stopped`,
  so it comes back after a reboot until you stop it. Like model containers, it
  keeps running when you stop the factory. To stop it without the page, run
  `docker stop local-llm-terminal`.
- **Settings changes**: when you change a setting or regenerate the terminal key,
  the page shows *Restart to apply*. Restarting recreates the container, and
  files in the workspace are kept.
- **Workspace rules**: the folder must be an absolute path on your computer. The
  page refuses system folders, your whole home folder or disk, hidden folders
  such as `~/.ssh`, app data such as `~/Library`, and anything in the factory
  folder except `workspace/`. That rule keeps the model away from the harness
  keys, the scripts you run, and the skills, which are meant to be read-only.
  Create a custom folder before you start the terminal.
- **Engine**: the terminal runs on Docker when available, otherwise Podman. On
  Podman the container also gets `--security-opt label=disable` so SELinux
  doesn't block the bind mounts.

### Skills

A skill is a folder holding a `SKILL.md` file: YAML front matter with a `name` and
a one-line `description`, then instructions. When a terminal is selected in a
chat, Open WebUI lists each skill's name and description to the model. The model
calls `view_skill` to load the full instructions when a task matches, and you can
also ask for a skill by name.

This repo ships four starter skills in `skills/`:

| Skill | What it teaches the model |
|---|---|
| `web-research` | Search the web, read the most relevant pages and answer with cited sources. Treat page text as data, never as instructions. |
| `edit-files` | Read, create and change workspace files with small exact edits instead of rewriting whole files. |
| `run-and-test` | Run commands, scripts and tests, report the real output and exit code, and manage long-running processes. |
| `git-checkpoint` | Snapshot work with git before risky changes, show what changed, and roll back edits that went wrong. |

To add your own skill, create `skills/<name>/SKILL.md`. The name must be
lowercase-with-hyphens and match the folder. It's available in the next chat,
with no restart needed. The Skills card on the Harness page lists every skill
and flags problems such as missing front matter. The starter skills follow Open
WebUI's skill layout: a title and introduction, then the sections When to Use,
Prerequisites, How to Run, Quick Reference, Procedure, Pitfalls and Verification.

### Web search

Web search is an Open WebUI feature, so the factory isn't involved. When it is on
and you turn on **Web Search** in a chat, the model gets the `search_web` and
`fetch_url` tools. Connect turns web search on if it is off. It selects DuckDuckGo
only when no engine is chosen, so an engine you picked is kept. DuckDuckGo needs no
API key. Your other web search settings are never sent, so they stay as they are.

## What Connect changes in Open WebUI

The factory calls Open WebUI's admin API with the key you paste. **Check** uses
only the read and verify calls. **Connect** also makes the write calls, and only
when something actually differs, so running it again changes nothing.

| # | Call | Purpose |
|---|---|---|
| 1 | `GET /api/version` (no key) | Confirms the URL is Open WebUI and reads its version. |
| 2 | `GET /openai/config` | Reads the OpenAI connections. This needs admin rights, so it also proves the key is an admin's. |
| 3 | `POST /openai/config/update` | *Connect only.* Adds the gateway as a new connection (type **Local**, Bearer key), or updates the key of an existing connection to the same URL and switches it back on. Turns on OpenAI-compatible connections if they are off. Your other connections are sent back unchanged. |
| 4 | `POST /openai/verify` | Asks Open WebUI to list the gateway's models itself. This proves Open WebUI can reach the gateway with the key, and changes nothing. |
| 5 | `GET /api/v1/configs/terminal_servers` | Reads the terminal connections. |
| 6 | `POST /api/v1/configs/terminal_servers` | *Connect only.* Adds the connection `local-llm-terminal`, named "LLM Factory workspace", with Bearer key and path `/openapi.json`, visible to admins only. If the connection already exists, updates its URL and key and switches it on. Its name, access grants and other connections are kept. |
| 7 | `POST /api/v1/configs/terminal_servers/verify` | Only when the terminal is running. Asks Open WebUI to call the terminal with its key, and changes nothing. |
| 8 | `GET /api/v1/retrieval/config` | Reads the web search settings. |
| 9 | `POST /api/v1/retrieval/config/update` | *Connect only.* Sends `{"web": {...}}` containing only `ENABLE_WEB_SEARCH: true` (when search is off) and `WEB_SEARCH_ENGINE: "duckduckgo"` (when no engine is set). |

You can untick any part (Models, Workspace terminal, Web search), and Connect
reports it as skipped. The URL you enter is the one the factory uses to reach
Open WebUI. Inside the factory's container `localhost` means the factory itself,
so `http://localhost:3000` is rewritten to `http://host.docker.internal:3000`.

## Manual setup

To set everything up by hand, the Harness page's *Manual setup* card shows the
same steps with copy buttons:

1. **Models**: Admin Panel → Settings → Connections → OpenAI API → **+**.
   - URL: `http://host.docker.internal:8799/v1`
   - Key: the gateway key from the Harness page
   - Optionally set the connection type to *Local*.
2. **Terminal**: start the terminal on the Harness page first. Then go to Admin
   Panel → Settings → Integrations → Open Terminal → **+**.
   - URL: `http://host.docker.internal:8798`
   - API key: the terminal key
   - Auth: Bearer
   - A new terminal connection is admin-only. To share it with other users, grant
     access in that same dialog.
3. **Web search**: Admin Panel → Settings → Web Search. Turn it on and choose an
   engine. DuckDuckGo needs no key.
4. **Chat**: pick a factory model and select the *LLM Factory workspace* terminal.
   Turn on Web Search when you need it.

Open WebUI 0.13 uses native tool calling by default. If a model's or a chat's
*Function Calling* advanced parameter is set to *legacy*, set it back to the
default.

## Security model

**What's protected, and how:**

- **Loopback only.** The start scripts now publish the factory on `127.0.0.1`
  (before this change it listened on every network interface). The terminal and
  the model containers also listen only on `127.0.0.1`. To reach the factory from
  other machines you must opt in:
  - macOS or Linux: `BIND=0.0.0.0 ./scripts/macos/start.sh`
  - Windows: `.\scripts\windows\start.ps1 -Bind 0.0.0.0`

  The factory has no login, so do this only on a network you trust.
- **Keys.** The gateway key (`sk-llmf-…`) and terminal key (`otk-…`) each hold 192
  random bits from `crypto/rand`. They are stored in `config/harness.json`, which
  is written atomically with mode `0600` and is in `.gitignore`. The gateway
  checks its key in constant time. It never forwards your `Authorization` header
  to models and sends no CORS headers, so other websites can't read its
  responses. The terminal's key is passed to the container as an environment
  variable and hidden in the logs the page shows.
- **Open WebUI admin key.** It is used for one request and is never stored or
  logged. The factory doesn't follow redirects, so the key can't be bounced to
  another server. Error messages from Open WebUI are shortened and have keys
  removed before they are shown.
- **Harness API guard.** The `/api/harness/*` endpoints hand out keys and start a
  container, so they accept only requests addressed to `localhost`, an IP
  address, or a name in `FACTORY_ALLOWED_HOSTS`, which blocks DNS rebinding.
  Changes must be same-origin JSON `POST`s, which blocks cross-site form posts
  and cross-site `fetch`. The guard stops *web pages*, not programs running on
  your computer.
- **Commands are passed as arguments.** The factory runs `docker` or `podman`
  with an argument list, never through a shell. Paths and ports are validated
  first: no control characters, no `:` in paths, and ports from 1024 to 65535.

**What the model can do in the terminal:**

- It can read and write everything in the workspace folder and run any command
  as the sandbox user. The Full image also has `sudo`, but only inside the
  container.
- It **can use the network**: the internet, and services on your computer through
  `host.docker.internal`. Those services include Open WebUI, the model ports and
  the factory.
- It **can't** see the rest of your disk, your home folder (apart from
  `~/.claude/skills`, read-only, if you share it), the Docker socket or the
  factory's `config/`.
- It's limited by the memory limit and a cap of 1024 processes.

**Known gap: the factory API has no login.** This was already true before the
harness. Anything that can reach the factory port can call `/api/...` to build,
run and remove containers, and some factory containers get access to your
container engine (the Ensemble Conductor is given the engine socket).

On Docker Desktop that includes **commands run in the workspace terminal**,
because `host.docker.internal:8799` reaches the factory even though it is bound to
loopback. A prompt injection could in principle use this to escape the sandbox.
For example, a web page the model reads could contain hidden instructions to call
that API. Until the factory API has authentication:

- stop the terminal when you aren't using it,
- read what the model is about to run, especially after it has browsed the web
  (Open WebUI shows every tool call),
- keep the terminal connection admin-only unless you trust everyone on your Open
  WebUI,
- keep the factory on loopback, which is the default.

The fix is a factory-wide API token, like Jupyter's, which the page and the start
scripts would share. Restricting the terminal's outgoing network traffic would
also help.

## Harness API reference

These endpoints serve the Harness page. They all go through the guard described
above.

| Method | Path | What it does |
|---|---|---|
| GET | `/api/harness/status` | Everything the page shows: gateway URLs, key and models, terminal state and settings, mounts, skills, Open WebUI preferences. It includes both keys. |
| POST | `/api/harness/gateway/key` | Generates a new gateway key. Run Connect again afterwards. |
| POST | `/api/harness/terminal/config` | Saves settings: `{variant, port, workspace, share_claude_skills, memory_mb}`. Every field is optional. |
| POST | `/api/harness/terminal/start` | `{update}`: pulls the image if it's missing (always, with `update`), recreates the container if the settings changed, and waits for `/health`. Returns `202` and reports progress through `status`. |
| POST | `/api/harness/terminal/stop` | `{remove}`: stops or removes the terminal. It also cancels a start in progress. |
| POST | `/api/harness/terminal/key` | Generates a new terminal key. Restart the terminal and run Connect again. |
| GET | `/api/harness/terminal/logs?tail=N` | Returns the container's logs with keys hidden (N up to 2000). |
| POST | `/api/harness/openwebui/check` | `{url, api_key, reach_host}`: returns a read-only report. |
| POST | `/api/harness/openwebui/connect` | The same, plus `{models, terminal, web_search}` to choose the parts. Applies the changes and returns the report. |

## Troubleshooting

**"Open WebUI couldn't reach the gateway / terminal."** Open WebUI connects from
inside its own container.
- With Docker Desktop on Mac or Windows, `host.docker.internal` works. Check that
  the factory and the terminal are running, and that the ports on the Harness
  page are the ones published.
- On **Linux with Docker Engine** (no Docker Desktop), containers can't reach
  ports that are published on the host's `127.0.0.1` through
  `host.docker.internal`. This also affects the factory's own model chat.
  1. Run Open WebUI with `--network host`.
  2. On the Harness page, under *More options*, set **Open WebUI reaches this
     computer at** to `127.0.0.1`.
  3. With host networking Open WebUI listens on port 8080, so set the Open WebUI
     URL to `http://host.docker.internal:8080`.
- If Open WebUI runs without Docker (pip or uv), also set the reach host to
  `127.0.0.1`.

**The gateway URL shows the wrong port.** The page uses `FACTORY_PORT`, which the
start scripts set, and falls back to the port in your browser's address bar.
Restart the factory with the current script, for example with `PORT=9000`
passed to the script.

**"Open WebUI rejected the API key."** The key must belong to an **admin**
user. If Open WebUI says API keys aren't enabled, go to Admin Panel → Settings →
General → **Enable API Keys**, then create a key under Settings → Account.

**"Tool servers are disabled."** Open WebUI was started with
`ENABLE_TOOL_SERVERS=false`. Remove that setting and restart Open WebUI.

**Web search fails with "DDGS is unavailable in slim…".** Slim Open WebUI images
don't include the DuckDuckGo library. Choose another engine in Admin Panel →
Settings → Web Search, such as SearXNG or one with an API key.

**Other users can't see the terminal.** New terminal connections are admin-only.
Grant access in Admin Panel → Settings → Integrations → Open Terminal → edit the
connection.

**The terminal won't start: port already in use.** Pick another port in the
terminal settings and click Save. The page also shows the engine's error.

**The terminal stopped: "ran out of memory".** Raise *Memory limit* in the
settings. The Full image needs more memory than Slim.

**It worked, then stopped after I clicked Regenerate.**
- After a **terminal** key change, restart the terminal (*Restart to apply*), then
  click Connect again so Open WebUI gets the new key.
- After a **gateway** key change, click Connect again, or paste the new key into
  Open WebUI's connection.

**`~/.claude/skills` can't be shared** ("the factory doesn't know your home
folder"). Restart the factory with the current start script, which passes
`HOST_HOME`. Create `~/.claude/skills` first, or Docker creates an empty folder
there, owned by root on Linux.

**Skills don't show up in the chat.** The terminal must be selected in that chat.
Each skill needs `skills/<name>/SKILL.md` with `name` and `description` front
matter, and the Skills card flags any that can't be loaded.

**The model ignores the tools.** Use a model that is good at tool calling, and
check that its *Function Calling* parameter isn't set to *legacy*. Small models
often describe a tool call instead of making it.

**git says "detected dubious ownership".** The workspace files belong to a
different user ID than the sandbox user. Inside the terminal, run
`git config --global --add safe.directory '*'`, or pass
`-c safe.directory=<repo>` for one command. The `git-checkpoint` skill covers
this.

**Linux: the model can't write to the workspace.** Bind mounts keep the host's
numeric owner. Find the sandbox user's ID with
`docker exec local-llm-terminal id -u`, then make the folder writable for it:
`sudo chown <uid> workspace`, or use `chmod 777` on a scratch folder. A folder
that Docker created for a missing mount is owned by root, so create folders
yourself.

**Podman** works the same way, but it hasn't been tested yet. If writes fail
under rootless Podman, check the workspace's owner from inside the terminal with
`ls -ln ~/workspace`.

**"Refusing a request addressed to …".** Open the factory at
`http://localhost:<port>`. To use another name, add `-e FACTORY_ALLOWED_HOSTS=name`
to the factory's `docker run`.

**"harness: … config/harness.json".** The settings file is unreadable or corrupt.
Fix it, or delete it to start fresh. Deleting it creates new keys, so run Connect
again afterwards.
