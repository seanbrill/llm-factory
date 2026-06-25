// Vanilla-JS frontend for the local-llm factory API.
"use strict";

const $ = (id) => document.getElementById(id);
let CATALOG = [];

async function api(path, opts) {
  const res = await fetch(path, opts);
  const text = await res.text();
  let data;
  try { data = text ? JSON.parse(text) : {}; } catch { data = { error: text }; }
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

// ---- Catalog / build form -------------------------------------------------

async function loadCatalog() {
  CATALOG = await api("/api/catalog");
  const sel = $("model");
  sel.innerHTML = "";
  for (const m of CATALOG) {
    const opt = document.createElement("option");
    opt.value = m.id;
    opt.textContent = `${m.name} — ${m.size_gb} GB (${m.tier})${m.recommended ? "  ★" : ""}`;
    sel.appendChild(opt);
  }
  const rec = CATALOG.find((m) => m.recommended);
  if (rec) sel.value = rec.id;
  updateHint();
}

function selectedModel() {
  return CATALOG.find((m) => m.id === $("model").value);
}

// UI-side model guidance (no backend involvement). `context` is the model's
// native window; the image actually serves at CTX_SIZE (default 4096 CPU /
// 8192 GPU). Ratings are rough 1–5 guidance relative to these local models.
// Edit freely — UI only, hot-reloads in dev mode.
const MODEL_INFO = {
  "llama-3.2-1b":   { context: "128K",              ratings: { Coding: 2, Math: 2, Reasoning: 2, Science: 2, Writing: 3, Multilingual: 3 } },
  "qwen2.5-1.5b":   { context: "32K",               ratings: { Coding: 3, Math: 3, Reasoning: 3, Science: 2, Writing: 3, Multilingual: 4 } },
  "qwen2.5-3b":     { context: "32K",               ratings: { Coding: 4, Math: 4, Reasoning: 3, Science: 3, Writing: 3, Multilingual: 4 } },
  "llama-3.2-3b":   { context: "128K",              ratings: { Coding: 3, Math: 3, Reasoning: 3, Science: 3, Writing: 4, Multilingual: 3 } },
  "phi-3.5-mini":   { context: "128K",              ratings: { Coding: 4, Math: 4, Reasoning: 4, Science: 3, Writing: 3, Multilingual: 3 } },
  "mistral-7b-v0.3":{ context: "32K",               ratings: { Coding: 3, Math: 3, Reasoning: 4, Science: 4, Writing: 4, Multilingual: 3 } },
  "qwen2.5-7b":     { context: "32K (128K w/ YaRN)", ratings: { Coding: 4, Math: 5, Reasoning: 4, Science: 4, Writing: 4, Multilingual: 5 } },
  "llama-3.1-8b":   { context: "128K",              ratings: { Coding: 4, Math: 4, Reasoning: 4, Science: 4, Writing: 4, Multilingual: 4 } },
  "qwen2.5-14b":    { context: "32K (128K w/ YaRN)", ratings: { Coding: 5, Math: 5, Reasoning: 5, Science: 5, Writing: 4, Multilingual: 5 } },
};

function stars(n) {
  n = Math.max(0, Math.min(5, n | 0));
  return "★".repeat(n) + "☆".repeat(5 - n);
}
function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}
function ratingsHtml(r) {
  return Object.entries(r)
    .map(([cat, n]) => `<span class="rate"><span class="cat">${cat}</span><span class="stars">${stars(n)}</span></span>`)
    .join("");
}

function updateHint() {
  const m = selectedModel();
  if (!m) return;
  const info = MODEL_INFO[m.id] || {};
  const vram = m.min_vram_gb > 0 ? `, ~${m.min_vram_gb} GB VRAM on GPU` : "";
  const ctx = info.context ? ` · context ${info.context}` : "";

  let html = `<span class="hint-spec">${escapeHtml(m.params)} · ~${m.min_ram_gb} GB RAM${vram}${ctx}</span>` +
             `<br>${escapeHtml(m.description)}`;
  if (info.ratings) html += `<div class="ratings">${ratingsHtml(info.ratings)}</div>`;
  html += `<div class="ctxnote">Ratings are rough guidance. Native context shown; the image serves at CTX_SIZE ` +
          `(default 4096 CPU / 8192 GPU — raise via env to use more of the window).</div>`;

  $("modelHint").innerHTML = html;
  const imgInput = $("imageName");
  if (!imgInput.dataset.touched) imgInput.value = "local-llm/" + m.id;
}

// ---- Resources & deploy helpers -------------------------------------------

function ctxDefaultFor(compute) { return (compute === "cuda" || compute === "vulkan") ? 8192 : 4096; }

// Human labels for the compute target and engine, shown in the images/containers
// tables. Keep in sync with the build-form <option> values.
const COMPUTE_BADGE = { cpu: "CPU", cuda: "GPU (CUDA)", vulkan: "GPU (Vulkan)" };
function computeBadge(c) { return COMPUTE_BADGE[c] || "CPU"; }
function engineBadge(e) { return e === "podman" ? "Podman" : "Docker"; }

// Prefill the context default for the selected compute, unless the user edited it.
function applyComputeDefaults() {
  const ci = $("ctxSize");
  if (!ci.dataset.touched) ci.value = ctxDefaultFor($("compute").value);
  updateEngineHint();
}

// Guidance for the engine/compute combo. Vulkan/Metal GPU only works under
// Podman + a libkrun machine (macOS) or a Linux GPU — surface that inline.
function updateEngineHint() {
  const engine = $("engine").value, compute = $("compute").value;
  let msg = "";
  if (compute === "vulkan" && engine !== "podman") {
    msg = "⚠ Vulkan/Metal GPU needs Podman. On macOS, switch Engine to Podman and run a libkrun machine (see README).";
  } else if (compute === "vulkan") {
    msg = "GPU via Vulkan→Metal: requires a Podman libkrun/krunkit machine on macOS (or a Linux Vulkan GPU). See README.";
  } else if (compute === "cuda") {
    msg = "NVIDIA only: needs the NVIDIA Container Toolkit on the host (WSL2 on Windows).";
  }
  $("engineHint").textContent = msg;
}

// Normalize the URL alias the same way the server does (for the live hint).
function routeAlias() {
  return $("route").value.toLowerCase().trim().replace(/^\//, "").replace(/[^a-z0-9-]/g, "");
}

function updateRouteHint() {
  const a = routeAlias();
  $("routeHint").textContent = a
    ? `→ reachable at http://${a}.localhost once you Run it (needs the proxy on port 80)`
    : "";
}

// ---- Build progress -------------------------------------------------------

const progressEl = () => $("buildProgress");
function setBar(pct) { $("buildBar").style.width = Math.max(0, Math.min(100, pct)) + "%"; }
function setStatus(t) { $("buildStatus").textContent = t; }
function setIndeterminate(on) { progressEl().classList.toggle("indeterminate", on); }

// UI-side default whole-build estimates (seconds), used until the server reports
// a real measured duration for that compute. Tweak here — UI only, no backend
// restart needed (and these hot-reload from disk in dev mode).
const DEFAULT_ETA = { cpu: 300, cuda: 1200, vulkan: 1200 };

let downloadingPhase = false;
let currentPhase = "Preparing…";
let buildCompute = "cpu";    // compute of the active build (selects the default ETA)
let recordedEta = 0;         // server's measured duration for this compute (0 = none yet)
let buildStart = 0;
let buildTimer = null;       // 1s elapsed/ETA ticker
let buildPollTimer = null;   // polls /api/build/state for new log lines
let buildOffset = 0;         // how many buffered lines we've consumed

function fmtDur(s) {
  s = Math.max(0, Math.round(s));
  return Math.floor(s / 60) + ":" + String(s % 60).padStart(2, "0");
}

// Effective whole-build ETA: the server's measured duration if we have one,
// otherwise the UI default for this compute.
function effectiveEta() {
  return recordedEta > 0 ? recordedEta : (DEFAULT_ETA[buildCompute] || 0);
}

function resetProgress() {
  const p = progressEl();
  p.hidden = false;
  p.classList.remove("error", "ok", "indeterminate");
  downloadingPhase = false;
  currentPhase = "Preparing…";
  recordedEta = 0;
  setBar(2);
  setStatus("Preparing build context…");
  $("buildLog").textContent = "";
}

function stopBuildTimer() {
  if (buildTimer) { clearInterval(buildTimer); buildTimer = null; }
  if (buildPollTimer) { clearInterval(buildPollTimer); buildPollTimer = null; }
}

// Ticks every second: elapsed + estimated time remaining for the WHOLE build,
// and drives the bar by elapsed/ETA. The estimate is marked "(est)" until the
// server has a real measured duration that overrides the default.
function tickBuild() {
  const elapsed = (Date.now() - buildStart) / 1000;
  const eta = effectiveEta();
  let txt = currentPhase + " · " + fmtDur(elapsed) + " elapsed";
  if (eta > 0) {
    const remain = eta - elapsed;
    const tag = recordedEta > 0 ? "" : " (est)";
    txt += remain > 0 ? " · ~" + fmtDur(remain) + " left" + tag : " · wrapping up…";
    setIndeterminate(false);
    setBar(Math.min(95, (elapsed / eta) * 100));
  } else {
    setIndeterminate(true);   // unknown compute / no default
  }
  setStatus(txt);
}

// Track the build phase from streamed log lines. The progress bar itself is
// driven by the whole-build ETA in tickBuild (not per individual step).
function handleBuildLine(line) {
  if (!line) return;
  $("buildLog").textContent += line + "\n";
  $("buildLog").scrollTop = $("buildLog").scrollHeight;

  if (line.startsWith("ERROR:")) {
    stopBuildTimer(); setIndeterminate(false);
    progressEl().classList.add("error"); setBar(100); setStatus(line); return;
  }
  if (line === "DONE") {
    stopBuildTimer(); setIndeterminate(false);
    progressEl().classList.add("ok"); setBar(100);
    setStatus("Done ✓ in " + fmtDur((Date.now() - buildStart) / 1000) + " — built & exported to ./images");
    return;
  }
  if (line.startsWith("Downloading")) { downloadingPhase = true; currentPhase = "Downloading model"; return; }
  const pm = line.match(/(\d+(?:\.\d+)?)%/);
  if (downloadingPhase && pm) { currentPhase = "Downloading model " + Math.round(parseFloat(pm[1])) + "%"; return; }
  if (line.startsWith("Saved model") || line.startsWith("Model already present")) { downloadingPhase = false; currentPhase = "Model ready"; return; }
  if (line.startsWith("Staging")) { currentPhase = "Staging model"; return; }
  if (line.startsWith("Baking")) { currentPhase = "Baking init prompt"; return; }
  if (line.startsWith("Building")) { downloadingPhase = false; currentPhase = "Building image (compiling llama.cpp)"; return; }
  if (line.startsWith("Built image")) { currentPhase = "Image built"; return; }
  if (line.startsWith("Exporting")) { currentPhase = "Exporting .tar"; return; }
  if (line.startsWith("Exported")) { currentPhase = "Exported"; return; }
  if (/^#\d+/.test(line)) { currentPhase = "Building… " + line.slice(0, 60); } // buildkit step
}

// Poll the server's buffered build state, applying any new log lines. Works for
// both the build we just started and one we reconnected to after a refresh.
async function pollBuildOnce() {
  let st;
  try { st = await api("/api/build/state?offset=" + buildOffset); }
  catch { return; }
  if (st.config && st.config.compute) buildCompute = st.config.compute;
  if (st.eta_seconds) recordedEta = st.eta_seconds;   // measured duration overrides the UI default
  for (const line of st.lines || []) handleBuildLine(line);
  buildOffset = st.next_offset;
  if (st.status && st.status !== "running") {
    stopBuildTimer();
    $("buildBtn").disabled = false;
    loadImages();
    loadModels();
  }
}

function startBuildLoop() {
  stopBuildTimer();
  buildTimer = setInterval(tickBuild, 1000);
  buildPollTimer = setInterval(pollBuildOnce, 800);
  tickBuild();
  pollBuildOnce();
}

async function build() {
  const m = selectedModel();
  const body = {
    model_id: m.id,
    image_name: $("imageName").value.trim(),
    tag: $("tag").value.trim() || "latest",
    engine: $("engine").value,
    compute: $("compute").value,
    system_prompt: $("systemPrompt").value,
    inject_mode: $("injectMode").value,
    ctx_size: parseInt($("ctxSize").value, 10) || 0,
    memory_gb: parseFloat($("memoryGb").value) || 0,
    route: routeAlias(),
    autostart: $("autostart").checked,
  };
  if (!body.image_name) { alert("Enter an image name."); return; }

  let res;
  try {
    res = await fetch("/api/build", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  } catch (e) { alert("Couldn't start build: " + e.message); return; }

  if (res.status === 409) {            // a build is already running — reconnect to it
    alert("A build is already in progress — reconnecting to it.");
    attachToBuild();
    return;
  }
  if (!res.ok) { alert("Build error: " + (await res.text())); return; }

  resetProgress();
  $("buildBtn").disabled = true;
  buildCompute = body.compute;   // selects the default ETA until the server has a measured one
  recordedEta = 0;
  buildStart = Date.now();
  buildOffset = 0;
  startBuildLoop();
}

// On page load (or after a 409), reconnect to an in-progress build so its
// progress and log persist across a refresh.
async function attachToBuild() {
  let st;
  try { st = await api("/api/build/state?offset=0"); }
  catch { return; }
  if (!st.active) return;            // nothing running — don't resurrect finished builds

  // Restore the build form to what's actually building, so a refresh shows the
  // config you set (model, compute, image name, init prompt) — not the defaults.
  const c = st.config || {};
  if (c.model_id && CATALOG.some((m) => m.id === c.model_id)) $("model").value = c.model_id;
  if (c.engine) $("engine").value = c.engine;
  if (c.compute) $("compute").value = c.compute;
  $("injectMode").value = c.inject_mode || "missing";
  $("systemPrompt").value = c.system_prompt || "";
  $("imageName").dataset.touched = "1";
  $("imageName").value = c.image_name || "";
  $("tag").value = c.tag || "latest";
  applyDeployConfig(c);
  updateHint();
  updateEngineHint();

  resetProgress();
  $("buildBtn").disabled = true;
  buildCompute = c.compute || "cpu";
  recordedEta = st.eta_seconds || 0;                 // measured duration overrides the UI default
  buildStart = Date.now() - (st.elapsed_ms || 0);    // server-relative, avoids clock skew
  for (const line of st.lines || []) handleBuildLine(line);
  buildOffset = st.next_offset;
  startBuildLoop();
}

function scrollToBuild() {
  document.querySelector("main .card").scrollIntoView({ behavior: "smooth", block: "start" });
}

// ---- Images ---------------------------------------------------------------

// Map a runtime container back to the image ref it was started from.
function containerRef(c) {
  for (const part of (c.Labels || "").split(",")) {
    const i = part.indexOf("=");
    if (i > 0 && part.slice(0, i) === "local-llm.ref") return part.slice(i + 1);
  }
  return c.Image || "";
}
function containerPort(c) {
  const m = (c.Names || "").match(/localllm-(\d+)/);
  if (m) return m[1];
  const pm = (c.Ports || "").match(/:(\d+)->/);
  return pm ? pm[1] : "";
}
function isRunning(c) {
  return (c.State || "").toLowerCase() === "running" || /^Up/.test(c.Status || "");
}
// Map a container's docker health to a coarse state for the status badge.
function healthOf(c) {
  const s = c.Status || "";
  if (/health:\s*starting/i.test(s)) return "starting";
  if (/\(healthy\)/i.test(s)) return "ready";
  if (/\(unhealthy\)/i.test(s)) return "unhealthy";
  return isRunning(c) ? "running" : "stopped";
}
function statusBadge(up, cs) {
  if (up) {
    const port = containerPort(up);
    switch (healthOf(up)) {
      case "starting":  return `<span class="starting">◐ starting…</span> :${port}`;
      case "ready":     return `<span class="running">● ready</span> :${port}`;
      case "unhealthy": return `<span class="error-text">● unhealthy</span> :${port}`;
      default:          return `<span class="running">● running</span> :${port}`;
    }
  }
  return cs.length ? `<span class="stopped">stopped</span>` : `<span class="stopped">—</span>`;
}

async function loadImages() {
  const tbody = $("imagesTable").querySelector("tbody");
  tbody.innerHTML = "";
  let imgs = [], conts = [], bstate = null;
  try { imgs = await api("/api/images"); } catch (e) { renderEmpty(tbody, 8, e.message); return; }
  try { conts = await api("/api/containers"); } catch { conts = []; }
  try { bstate = await api("/api/build/state?offset=0"); } catch {}

  // Show the in-progress build as a row (the image isn't in `docker images`
  // yet). Clicking it jumps to the live progress/config in the build form above.
  if (bstate && bstate.active) {
    const c = bstate.config || {};
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><a href="#" class="link bip">${c.image_name || "(building)"}</a></td>
      <td>${c.tag || "latest"}</td><td>${engineBadge(c.engine)}</td><td>${computeBadge(c.compute)}</td><td>—</td>
      <td><span class="starting">◐ building…</span></td>
      <td>—</td>
      <td class="actions"><button class="small bip">View progress</button></td>`;
    tr.querySelectorAll(".bip").forEach((el) => { el.onclick = (e) => { e.preventDefault(); scrollToBuild(); }; });
    tbody.appendChild(tr);
  }

  if (!imgs.length) {
    if (!(bstate && bstate.active)) renderEmpty(tbody, 8, "No images built yet.");
    return;
  }

  // Group containers by the image ref they run, to show a live status per image.
  const byRef = {};
  for (const c of conts) { (byRef[containerRef(c)] ||= []).push(c); }

  let port = 8080;
  for (const im of imgs) {
    // Handle dangling/untagged images (leftovers from rebuilding over a tag):
    // show them clearly and use the image ID as the ref so they're still
    // runnable/deletable instead of appearing as blank rows.
    const repo = im.Repository && im.Repository !== "<none>" ? im.Repository : null;
    const tagged = im.Tag && im.Tag !== "<none>" ? im.Tag : null;
    const shortId = (im.ID || "").replace(/^sha256:/, "").slice(0, 12);
    const ref = repo ? `${repo}:${im.Tag}` : shortId;
    const displayName = repo || "<untagged>";
    const displayTag = tagged || shortId;
    const engine = im.Engine || "docker";

    const cs = byRef[ref] || [];
    const up = cs.find(isRunning);
    const status = statusBadge(up, cs);

    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><a href="#" class="link tpl" title="Use as a template for a new build">${displayName}</a></td>
      <td>${displayTag}</td><td>${engineBadge(engine)}</td><td>${computeBadge(im.Compute)}</td><td>${im.Size || ""}</td>
      <td>${status}</td>
      <td><input type="number" value="${up ? containerPort(up) : port++}" /></td>
      <td class="actions">
        <button class="small primary" data-act="run">Run</button>
        <button class="small" data-act="dl">Download</button>
        <button class="small danger" data-act="del">Delete</button>
      </td>`;
    tr.querySelector(".tpl").onclick = (e) => { e.preventDefault(); useAsTemplate(ref, engine); };
    const runBtn = tr.querySelector('[data-act="run"]');
    runBtn.onclick = () => runImage(ref, parseInt(tr.querySelector("input").value, 10), runBtn, engine);
    tr.querySelector('[data-act="dl"]').onclick = () => downloadImage(ref, engine);
    tr.querySelector('[data-act="del"]').onclick = () => deleteImage(ref, engine);
    tbody.appendChild(tr);
  }
}

function downloadImage(ref, engine) {
  // Browser handles the save dialog; streams `<engine> save` from the server.
  window.location.href = "/api/image/download?ref=" + encodeURIComponent(ref) +
    "&engine=" + encodeURIComponent(engine || "docker");
}

// Load a built image's baked config into the build form to clone it as a template.
async function useAsTemplate(ref, engine) {
  let cfg;
  try {
    cfg = await api("/api/image/config?ref=" + encodeURIComponent(ref) +
      "&engine=" + encodeURIComponent(engine || "docker"));
  } catch (e) { alert("Couldn't load template: " + e.message); return; }

  if (cfg.model_id && CATALOG.some((m) => m.id === cfg.model_id)) $("model").value = cfg.model_id;
  if (cfg.engine) $("engine").value = cfg.engine;
  if (cfg.compute) $("compute").value = cfg.compute;
  $("injectMode").value = cfg.inject_mode || "missing";
  $("systemPrompt").value = cfg.system_prompt || "";
  // Mark the name as user-owned so the model-change handler won't overwrite it.
  $("imageName").dataset.touched = "1";
  $("imageName").value = cfg.repository || "";
  $("tag").value = cfg.tag || "latest";
  applyDeployConfig(cfg);
  updateHint();
  updateEngineHint();

  document.querySelector("main .card").scrollIntoView({ behavior: "smooth", block: "start" });
  $("imageName").focus();
  $("imageName").select();
}

// Restore the resources/deploy fields from a build config or image labels
// (values may arrive as numbers from /api/build/state or strings from labels).
function applyDeployConfig(c) {
  const ctx = parseInt(c.ctx_size, 10);
  $("ctxSize").value = ctx > 0 ? ctx : ctxDefaultFor($("compute").value);
  $("ctxSize").dataset.touched = "1";
  $("memoryGb").value = parseFloat(c.memory_gb) || 0;
  $("route").value = c.route || "";
  $("autostart").checked = !!c.autostart;
  updateRouteHint();
}

// Poll while any container is still in "health: starting" so the status badge
// flips from starting… to ready on its own.
let healthTimer = null;
function scheduleHealthRefresh() {
  if (healthTimer) return;
  let ticks = 0;
  healthTimer = setInterval(async () => {
    ticks++;
    let conts = [];
    try { conts = await api("/api/containers"); } catch {}
    loadImages();
    loadContainers();
    const starting = conts.some((c) => /health:\s*starting/i.test(c.Status || ""));
    if (!starting || ticks > 40) { clearInterval(healthTimer); healthTimer = null; }
  }, 3000);
}

async function runImage(ref, port, btn, engine) {
  // Safety: warn before starting a second instance of an already-running image.
  let conts = [];
  try { conts = await api("/api/containers"); } catch {}
  const already = conts.filter((c) => containerRef(c) === ref && isRunning(c));
  if (already.length) {
    const ports = already.map(containerPort).join(", ");
    if (!confirm(`"${ref}" is already running (port ${ports}).\n\nStart ANOTHER instance on port ${port}?`)) {
      return;
    }
  }

  if (btn) { btn.disabled = true; btn.textContent = "Starting…"; }
  try {
    // Server decides CPU/GPU and the engine authoritatively from the image label;
    // we pass the engine the row was listed under as the lookup hint.
    await api("/api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ref, port, engine: engine || "docker" }),
    });
    $("chatPort").value = port;
    loadImages();             // refresh the running indicator (row re-renders)
    loadContainers();
    scheduleHealthRefresh();  // keep refreshing until the model reports healthy
  } catch (e) {
    alert("Run failed: " + e.message);
    if (btn) { btn.disabled = false; btn.textContent = "Run"; }
  }
}

async function deleteImage(ref, engine) {
  if (!confirm(`Delete image ${ref} and its exported .tar?`)) return;
  try {
    await api("/api/image/delete", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ref, engine: engine || "docker" }),
    });
    loadImages();
  } catch (e) { alert("Delete failed: " + e.message); }
}

// ---- Containers -----------------------------------------------------------

async function loadContainers() {
  const tbody = $("containersTable").querySelector("tbody");
  tbody.innerHTML = "";
  let cs = [];
  try { cs = await api("/api/containers"); } catch (e) { renderEmpty(tbody, 6, e.message); return; }
  if (!cs.length) { renderEmpty(tbody, 6, "No containers."); return; }

  for (const c of cs) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${c.Names || ""}</td><td>${c.Image || ""}</td><td>${engineBadge(c.Engine)}</td>
      <td>${c.Status || c.State || ""}</td><td>${c.Ports || ""}</td>
      <td class="actions"><button class="small danger">Stop &amp; remove</button></td>`;
    tr.querySelector("button").onclick = async () => {
      if (!confirm(`Stop and remove container ${c.Names || c.ID}?`)) return;
      try {
        await api("/api/stop", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ id: c.ID || c.Names, engine: c.Engine || "docker" }),
        });
        loadContainers();
        loadImages();   // refresh the running indicator
      } catch (e) { alert("Remove failed: " + e.message); }
    };
    tbody.appendChild(tr);
  }
}

// ---- Downloaded models ----------------------------------------------------

async function loadModels() {
  const tbody = $("modelsTable").querySelector("tbody");
  tbody.innerHTML = "";
  let models = [];
  try { models = await api("/api/models"); } catch (e) { renderEmpty(tbody, 4, e.message); return; }
  const present = models.filter((m) => m.downloaded);
  if (!present.length) { renderEmpty(tbody, 4, "No model files downloaded yet (they download on first build)."); return; }

  for (const m of present) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${m.name}</td><td><code>${m.file}</code></td><td>${m.on_disk_gb.toFixed(2)} GB</td>
      <td class="actions"><button class="small danger">Delete</button></td>`;
    tr.querySelector("button").onclick = async () => {
      if (!confirm(`Delete weights for ${m.name} (${m.on_disk_gb.toFixed(2)} GB)? A future build will re-download it.`)) return;
      try {
        await api("/api/model/delete", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ model_id: m.id }),
        });
        loadModels();
      } catch (e) { alert("Delete failed: " + e.message); }
    };
    tbody.appendChild(tr);
  }
}

// ---- Chat / test ----------------------------------------------------------

async function chat() {
  const out = $("chatOut");
  out.hidden = false;
  out.textContent = "Thinking… (CPU inference can take a few seconds)";
  const btn = $("chatBtn");
  btn.disabled = true;
  try {
    const data = await api("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        port: parseInt($("chatPort").value, 10) || 8080,
        system: $("chatSystem").value,
        prompt: $("chatPrompt").value,
      }),
    });
    out.textContent = data.response;
  } catch (e) {
    out.textContent = "ERROR: " + e.message;
  } finally {
    btn.disabled = false;
  }
}

function renderEmpty(tbody, cols, msg) {
  tbody.innerHTML = `<tr><td class="empty" colspan="${cols}">${msg}</td></tr>`;
}

// ---- Wire up --------------------------------------------------------------

$("model").addEventListener("change", updateHint);
$("compute").addEventListener("change", applyComputeDefaults);
$("engine").addEventListener("change", updateEngineHint);
$("ctxSize").addEventListener("input", () => { $("ctxSize").dataset.touched = "1"; });
$("route").addEventListener("input", updateRouteHint);
$("imageName").addEventListener("input", () => { $("imageName").dataset.touched = "1"; });
$("buildBtn").addEventListener("click", build);
$("logToggle").addEventListener("click", () => {
  const log = $("buildLog");
  log.hidden = !log.hidden;
  $("logToggle").textContent = log.hidden ? "Show log" : "Hide log";
});
$("refreshImages").addEventListener("click", loadImages);
$("refreshContainers").addEventListener("click", loadContainers);
$("refreshModels").addEventListener("click", loadModels);
$("chatBtn").addEventListener("click", chat);

(async () => {
  await loadCatalog();          // ensure the model dropdown is ready first
  applyComputeDefaults();       // prefill context default for the selected compute
  loadImages();
  loadContainers();
  loadModels();
  attachToBuild();              // reconnect to an in-progress build after a refresh
})();
