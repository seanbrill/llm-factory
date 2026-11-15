# GPU on a Mac 101: krunkit, Vulkan & Venus

> **One-line version:** Apple Macs have a powerful GPU, but it speaks "Metal," which
> Linux containers can't use directly. A special virtual machine called **krunkit**
> bridges the gap, exposing the Mac GPU to a container as a standard **Vulkan**
> device. Getting this working is the hard part this project solved.

This is the most advanced concept here. You don't need it to use CPU models — only
if you want GPU speed on a Mac.

## Why is GPU-in-a-container hard on a Mac?

- A **GPU** massively speeds up AI (often 5–50× faster than CPU).
- Containers are **Linux**. They expect to talk to a GPU using **CUDA** (NVIDIA) or
  **Vulkan** (a cross-vendor standard).
- An Apple Mac's GPU only speaks **Metal** (Apple's own API). There is **no Metal
  inside a Linux container**, and no direct GPU passthrough on macOS.

So normally, a model in a container on a Mac is stuck on the **CPU**.

## The bridge: krunkit + virtio-GPU + Venus

Here's the chain that makes it work (each link translates to the next):

```
Your model (llama.cpp, Vulkan)
   │  speaks Vulkan
   ▼
Mesa "Venus" driver  (a Vulkan driver that forwards commands)
   │  over a virtual GPU device (/dev/dri)
   ▼
krunkit / libkrun  (the lightweight VM running the container)
   │  translates Vulkan → Metal via Apple's frameworks
   ▼
Your Mac's GPU (Metal)
```

- **krunkit / libkrun** — a lightweight virtual machine for macOS that can
  *paravirtualize* the GPU. Podman uses it when you pick the **"GPU enabled
  (LibKrun)"** machine type.
- **virtio-GPU** — the virtual graphics device the VM exposes to the container (you
  see it as `/dev/dri` inside the container).
- **Mesa "Venus"** — a Vulkan driver whose whole job is to forward Vulkan commands
  across that virtual device to the host, where they become Metal calls.

## The catch we hit (and fixed)

Stock (normal) versions of the Mesa Venus driver **fail** on krunkit with an error
like `failed to allocate/map ring shmem` → `ERROR_OUT_OF_HOST_MEMORY`. The fix
(discovered via the RamaLama project) is to use a **patched** Venus driver, pinned
to a specific build, from a special package repository:

- Package repo (COPR): `slp/mesa-libkrun-vulkan`
- Pinned version: `mesa-vulkan-drivers-25.3.6-102.fc44`
- Base OS for the image: **Fedora 44**

This is why our GPU Dockerfiles (`scripts/docker/runtimes/Dockerfile.vulkan`, `Dockerfile.sd.vulkan`,
`Dockerfile.whisper.vulkan`) are Fedora-based and install that exact patched driver.
With it, the model reports a real GPU:
```
ggml_vulkan: Virtio-GPU Venus (Apple M4 Pro) ... uma: 1
```
and runs at GPU speed.

## What you need to do (recap)

1. Install **Podman Desktop** and create a machine with the **"GPU enabled
   (LibKrun)"** provider.
2. In the factory, build with **Engine = Podman** and **Compute = GPU — Vulkan /
   Apple Metal**.
3. The factory's Run command automatically adds the flags Venus needs
   (`--device /dev/dri --security-opt label=disable`).

Docker can be a GPU engine too, when its VM has a GPU device (Docker VMM, or a
krunkit VM). That's experimental; see the next section.

## What about Docker Desktop?

Docker Desktop also runs containers in a Linux VM. Whether that VM can give them
the GPU depends on its virtual machine manager:

- **Apple Virtualization (the default VM manager)** has no virtual GPU, so there's
  no `/dev/dri` to hand a container. Running a Vulkan image there fails with
  `error gathering device information while adding custom device "/dev/dri": no
  such file or directory`; the factory turns that into: *Docker Desktop on this Mac
  gave the container no GPU device (/dev/dri): its default VM manager, Apple
  Virtualization, has none. To try Docker's GPU (experimental), switch Docker
  Desktop to Docker VMM (Settings → General → Virtual Machine Manager → Docker
  VMM, then Apply & restart), start the factory again, and run Check GPU access on
  the System page again. Or run this image on Podman.*
- **Docker VMM** — *experimental, unverified with this project.* Docker's own VM
  manager does have a `virtio-gpu` device with a Vulkan host behind it
  (MoltenVK/KosmicKrisp), and its kernel is built with `CONFIG_DRM_VIRTIO_GPU=y`,
  so `--device /dev/dri` plus our patched-Venus image *may* work there. Nobody has confirmed it yet with the
  factory's images. If the GPU check finds a usable `/dev/dri`, the factory treats
  Docker as a Vulkan engine (see below).
- **The WebGPU device** — *undocumented, experimental.* Recent Docker Desktop
  versions list a CDI device `docker.com/gpu=webgpu` under "Discovered Devices" in
  `docker info`. It remotes the **WebGPU** API to the host (Dawn on Metal) and
  mounts `/usr/lib/webgpu/libwebgpu.so` into the container. llama.cpp's WebGPU
  backend is built against Dawn itself, so the factory can't use this device today.
- **Docker Engine in a krunkit VM** — *experimental, unverified with this project.*
  Lima and Colima can run Docker Engine in a VM with `vmType: krunkit`, which has
  the same virtio-gpu device as Podman's libkrun machine. Point the factory at that
  engine's socket with `FACTORY_DOCKER_SOCK` (below), then check GPU access.

## The GPU check decides Docker's GPU

To see what your engines actually give a container, open **System → Check GPU
access**. It starts a tiny throwaway container (alpine) on each engine with
`--device /dev/dri`, lists `/dev/dri` with its owners and modes (`ls -ln`), and
reads each engine's CDI devices.

The factory saves each engine's last result in `config/gpu-probe.json`, with the
time it ran and which machine it ran on (the engine's system, kernel, daemon ID,
host name and architecture, from `docker info` / `podman info`). Podman's GPU
still comes from the start script, which sees the libkrun machine. Docker's comes
from that saved check:

- **Docker gets Vulkan** when its last check saw a GPU render node
  (`/dev/dri/renderD128`) that the images' unprivileged user can open. The System
  page then says *Docker's GPU comes from the GPU check*, shows when it ran, and
  marks the GPU experimental. Vulkan images build for Docker and run there with
  `--device /dev/dri`. When only the node's group can open it, runs also get
  `--group-add <that group>`. A node only root can open doesn't count: the Vulkan
  images run as a normal user and would fall back to the CPU.
- **Docker doesn't get the Podman flags.** Vulkan runs on Podman also get
  `--security-opt label=disable` (SELinux on the libkrun machine) and
  `seccomp=unconfined` (a podman-remote path mismatch). Neither applies to Docker,
  so the factory doesn't add them there.
- **Switching VMs makes the check stale.** When Docker's system, kernel or daemon
  ID no longer match the saved check, the factory stops treating Docker as a GPU
  engine, and the System page says why and offers **Check GPU access again**.
  Docker Desktop can keep the same kernel when you switch VM managers, so always
  check again after switching, either way.
- **A failed GPU run clears it.** If a Vulkan run on Docker finds no `/dev/dri`,
  the factory records that, stops routing GPU work to Docker, and says how to fix
  it.
- **An NVIDIA machine stays on CUDA.** When the start script set the machine's
  GPU to CUDA, a `/dev/dri` on Docker doesn't turn it into a Vulkan engine.

### Two GPU engines

With Docker VMM and a libkrun Podman machine, both engines can have the GPU. The
Catalog, the Build plan and the Overview then send GPU work to the one with more
memory (a GPU in a VM shares that VM's memory). When they're the same size, Podman
wins, because that path is verified. A build you set to an engine that has the
GPU (**Runs on** in the row's settings) stays there. The low-memory rules work the
same on both: when the GPU engine is too small to compile the Vulkan image, the
other engine compiles it and the image moves over (`podman save` → `docker load`,
or the reverse).

### Try Docker VMM (experimental)

You need Docker Desktop with the Docker VMM option (Apple Silicon only).

1. Stop any models running on Docker.
2. In Docker Desktop, open **Settings → General → Virtual Machine Manager**, pick
   **Docker VMM**, then click **Apply & restart**.
3. When Docker is running again, start the factory again:
   `./scripts/macos/start.sh`.
4. Open **System** and click **Check GPU access**. Docker's row says whether a GPU
   device reached the container and whether the images can open it.
5. If it did, build a model with **Vulkan** on Docker (the Build plan may pick
   Docker for you when it has more memory than the Podman machine), run it, and
   look for `ggml_vulkan: Virtio-GPU Venus` in its log. If the log shows no Vulkan
   device, or the run fails, use Podman for GPU models and tell us what you saw.

To go back, pick **Apple Virtualization** in the same setting, start the factory
again, and check GPU access again.

### Docker Engine in a Lima or Colima VM (experimental)

Start Docker Engine in a VM with a krunkit VM type (Lima's `vmType: krunkit`, or
Colima's equivalent if your version has one; see their docs for the exact
command). Then start the factory with that engine's socket (this is the path
Lima's `docker` template uses; Colima's is `~/.colima/<profile>/docker.sock`):

```bash
FACTORY_DOCKER_SOCK=~/.lima/docker/sock/docker.sock ./scripts/macos/start.sh
```

The start script mounts that socket where the default Docker socket goes (the
factory container itself still runs on Docker Desktop). `start-native.sh` takes the
same variable and sets `DOCKER_HOST`. Then check GPU access as above. Nothing
about this route has been tried with the factory yet.

## How much memory does it use?

A Mac has **unified memory** (the CPU and GPU share one pool). The model's weights
load into that shared memory. A practical guide:
- The Podman machine is given a memory cap — **only 2 GB** if you created it with
  plain `podman machine init`. The model must fit inside that, plus room for its
  "context" (the conversation history): the factory counts the weights + ~2 GB,
  and asks before starting a GPU model whose weights are bigger than the machine's
  memory (a CPU model still starts, but runs slowly paging its weights from disk,
  so that only warns).
- **Building** the Vulkan image compiles llama.cpp from source, which needs about
  **6 GB** in the machine that compiles it (a CPU image about 3 GB). Each compile
  job takes ~2 GB, so the Dockerfiles run as many in parallel as the memory allows
  (`BUILD_JOBS=0` = automatic; set a number to force it) instead of one per CPU.
  If the Podman machine is too small, the factory **compiles on Docker instead and
  transfers the image** to Podman (`docker save` → `podman load`, each step in the
  build log); the image is still labelled and run as a Podman image. If no engine
  has enough memory, the build stops before downloading anything and says how to
  fix it.
- The sidebar lists each engine's machine separately and flags a GPU machine
  that's too small. To resize the Podman machine (then restart the factory so it
  reconnects):
  ```bash
  podman machine stop && podman machine set --memory 12288 && podman machine start
  ```
- The factory's build form shows a **"fits your system"** badge — for the engine
  you picked — to help you choose a model that's comfortable there.

> **Note:** krunkit has a known limit — don't give the Podman machine more than
> ~27 GB of memory, or it can fail to start.
</content>
