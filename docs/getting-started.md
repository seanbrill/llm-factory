# Getting started

This walks you from a blank machine to chatting with your own local AI model. No
prior experience needed. Pick the section for your operating system.

> **What you'll install:** exactly one thing — a *container engine*. That's a
> program that runs small, self-contained app bundles called containers. We use it
> so the AI models run in a clean, identical environment on any machine. (If you've
> never heard of containers, see [Containers 101](concepts/01-containers.md) — but
> you don't need to read it to follow these steps.)

---

## macOS (Apple Silicon — M1/M2/M3/M4)

On a Mac, the only way to use the **GPU** for AI inside a container is **Podman**
with a special "krunkit" machine. We recommend **Podman Desktop**.

### 1. Install Podman Desktop
1. Go to **https://podman-desktop.io** and click **Download** (Apple Silicon).
2. Open the downloaded `.dmg` and drag **Podman Desktop** to Applications.
3. Launch it. On first run it offers to set up a **Podman machine** — accept.
   - When asked for the provider, choose **"GPU enabled (LibKrun)"**. This is what
     lets models use your Mac's GPU.
   - Give it a reasonable amount of memory (e.g. 16 GB) and CPUs. See
     [GPU on a Mac 101](concepts/02-gpu-on-mac.md) for why.
4. Wait until Podman Desktop shows the machine as **Running**.

You also need **Docker** *or* Podman to run the factory's own control panel. Podman
Desktop already gives you Podman, so you're set.

### 2. Start the factory
1. Download/clone this project to a folder.
2. Open the **Terminal** app, and `cd` into the project folder.
3. Run:
   ```bash
   ./scripts/macos/start.sh
   ```
   The first run builds the factory's control panel (a minute or two) and starts it.
4. Open **http://localhost:8799** in your browser.

### 3. Build and run your first model
1. In the web page, under **"1 · Build an image"**, pick a model. Start small —
   **Qwen3 4B** (marked ★) is a great first pick. The badge shows it's a **💬 Chat**
   model and whether it **fits your system**.
2. Set **Engine → Podman** and **Compute → GPU — Vulkan / Apple Metal**.
3. Click **Build & export .tar**. It downloads the model and builds the image. The
   first GPU build compiles the inference engine, which takes ~10 minutes (later
   builds reuse it and are fast).
4. When it appears under **"2 · Built images"**, click **Run**.
5. After it loads, open the URL it gives you (like **http://marvin.localhost**) and
   start chatting.

That's it — a private AI running on your Mac's GPU.

---

## Linux

### 1. Install a container engine
- **Docker:** follow https://docs.docker.com/engine/install/ for your distro, or
  install Docker Desktop. For an NVIDIA GPU, also install the NVIDIA Container Toolkit.
- **Podman** (alternative): `sudo apt install podman` (Debian/Ubuntu) or your
  distro's package. For a GPU, a Vulkan-capable driver is needed.

### 2. Start the factory
```bash
./scripts/linux/start.sh
```
Open **http://localhost:8799**.

### 3. Build a model
- **CPU (works anywhere):** pick a model, set **Compute → CPU**, Build, Run, chat.
- **NVIDIA GPU:** set **Compute → NVIDIA GPU (CUDA)**.
- **Other GPU (AMD/Intel) via Vulkan:** set **Compute → GPU — Vulkan**.

---

## Windows

### 1. Install Podman Desktop or Docker Desktop
- **Podman Desktop:** https://podman-desktop.io (it sets up WSL2 + a Podman machine).
- **Docker Desktop:** https://www.docker.com/products/docker-desktop/ (enable the
  WSL2 backend).

### 2. Start the factory (PowerShell)
```powershell
.\scripts\windows\start.ps1
```
Open **http://localhost:8799**. GPU on Windows is best via NVIDIA + WSL2 (Compute →
CUDA). Otherwise use **Compute → CPU**.

---

## Stopping things
- Stop the factory control panel: `./scripts/<your-os>/stop.sh` (or `stop.ps1`).
- Stop a running model: click **"Stop & remove"** next to it in the web UI.

## Troubleshooting
- **"Server unavailable" at the chat URL** — the model isn't running. Open the
  factory (localhost:8799) and click **Run** on the image. After a computer
  *restart*, the factory tries to bring autostart models back automatically.
- **An error like "Container engine unreachable"** — your Docker/Podman isn't
  running. On macOS, make sure the Podman machine is **Running** in Podman Desktop,
  then re-run `./scripts/macos/start.sh`.
- **Build fails** — the factory now shows the real cause (out of disk, port in use,
  etc.) and auto-opens the build log. See [Architecture](architecture.md) for what a
  build does.
</content>
