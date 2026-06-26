# Containers 101: Docker & Podman

> **One-line version:** a *container* is a lightweight, self-contained bundle that
> runs an app with everything it needs, exactly the same on any machine. The factory
> uses containers so each AI model is a clean, portable unit you can run, stop, and
> delete without messing up your computer.

## What problem do containers solve?

Software usually needs a specific environment — a certain operating system,
libraries, and tools — to run. Setting that up by hand is fiddly and breaks easily
("it works on my machine"). A **container** packages the app *and* its environment
together, so it runs identically anywhere a container engine is installed.

Think of it like a **shipping container**: standardized on the outside, whatever you
need on the inside. Your computer just needs to know how to move and open them.

## Key words (plain English)

- **Image** — a *recipe + ingredients*, frozen. A read-only template that contains an
  app and its environment. (For us: a model + the program that serves it.) Images
  have names like `local-llm/qwen3-4b:latest` (`:latest` is the *tag*, a version
  label).
- **Container** — a *running instance* of an image. You can start several from one
  image. When it stops, anything it wrote (that you didn't save outside) is gone.
- **Dockerfile** — the written recipe used to *build* an image, step by step.
- **Build** — turning a Dockerfile into an image.
- **Run** — starting a container from an image.
- **Registry** — an online store of images (like Docker Hub). We mostly build our
  own locally, so you rarely need this.
- **Volume / bind-mount** — a way to share a folder between your computer and a
  container (we use it so downloaded models live in your `models/` folder, not
  trapped inside an image).
- **Port** — a numbered "door" a container exposes so you can talk to it. We map a
  container's port 8080 to a port on your machine so the browser can reach it.

## Docker vs Podman — what's the difference?

Both build and run containers and accept almost the same commands. The factory works
with **either**.

| | Docker | Podman |
|---|---|---|
| Background service | Runs a central **daemon** (a always-on helper process). | **Daemonless** — runs containers directly. |
| Root | Historically needed root. | Designed to run **rootless** (safer). |
| On macOS/Windows | Runs a hidden Linux VM (Docker Desktop). | Runs a Linux VM too (a "Podman machine"). |
| Why we care | Great default, widely installed. | **The only way to use the GPU on a Mac** (via a special "krunkit" machine — see the [GPU on a Mac](02-gpu-on-mac.md) guide). |

**Rule of thumb in this project:**
- Just want it to work / on Linux with NVIDIA? **Docker** is fine.
- On a Mac and want GPU speed? **Podman** with a krunkit machine.

## Why does the factory itself run in a container?

The control panel ships as a container too, so you don't need to install Go or any
build tools — you only need Docker or Podman. It then talks to your machine's
container engine to build and run the *model* containers. (The deep-dive on this
"Docker-out-of-Docker" setup is in [CONTAINER.md](../CONTAINER.md).)

## Commands you'll see the factory run (FYI)

You never have to type these — the UI does — but here's what's happening:
```bash
docker build  -t local-llm/qwen3-4b .     # turn a Dockerfile into an image
docker run -d -p 8080:8080 local-llm/...   # start a container in the background
docker ps                                  # list running containers
docker rm -f <name>                        # stop and delete a container
docker images                              # list images
```
Podman accepts the same commands — just swap `docker` for `podman`.
</content>
