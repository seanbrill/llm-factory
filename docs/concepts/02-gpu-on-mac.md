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

This is why our GPU Dockerfiles (`docker/Dockerfile.vulkan`, `Dockerfile.sd.vulkan`,
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

## How much memory does it use?

A Mac has **unified memory** (the CPU and GPU share one pool). The model's weights
load into that shared memory. A practical guide:
- The Podman machine is given a memory cap (e.g. 16 GB). The model must fit inside
  that, plus room for its "context" (the conversation history).
- The factory's build form shows a **"fits your system"** badge to help you pick a
  model that's comfortable on your RAM.

> **Note:** krunkit has a known limit — don't give the Podman machine more than
> ~27 GB of memory, or it can fail to start.
</content>
