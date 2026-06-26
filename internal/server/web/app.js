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

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// ---- Inline SVG icon set (professional line icons, dependency-free) --------
// 24×24 viewBox, drawn in currentColor. `icon(name)` wraps a path; size/colour
// come from CSS (.ico). Keep names stable — referenced by MODALITY and CAT_ICON.
const ICONS = {
  chevron:   '<polyline points="6 9 12 15 18 9"/>',
  search:    '<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>',
  sliders:   '<path d="M4 21v-6M4 11V3M12 21v-9M12 7V3M20 21v-5M20 11V3M1 15h6M9 7h6M17 15h6"/>',
  star:      '<polygon points="12 2 15 9 22 9 17 14 19 21 12 17 5 21 7 14 2 9 9 9"/>',
  cpu:       '<rect x="5" y="5" width="14" height="14" rx="2"/><rect x="9" y="9" width="6" height="6"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M19 9h3M19 14h3M2 9h3M2 14h3"/>',
  gpu:       '<rect x="2" y="6" width="20" height="12" rx="2"/><circle cx="8" cy="12" r="2.4"/><circle cx="15.5" cy="12" r="2.4"/><path d="M5 18v3"/>',
  // modalities
  chat:      '<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>',
  code:      '<polyline points="16 18 22 12 16 6"/><polyline points="8 6 2 12 8 18"/>',
  idea:      '<path d="M9 18h6M10 22h4"/><path d="M12 2a7 7 0 0 0-4 12.7c.6.5 1 1.3 1 2.3h6c0-1 .4-1.8 1-2.3A7 7 0 0 0 12 2z"/>',
  eye:       '<path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z"/><circle cx="12" cy="12" r="3"/>',
  hash:      '<path d="M4 9h16M4 15h16M10 3 8 21M16 3l-2 18"/>',
  image:     '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.8"/><path d="m21 15-5-5L5 21"/>',
  mic:       '<rect x="9" y="2" width="6" height="11" rx="3"/><path d="M5 10a7 7 0 0 0 14 0M12 18v4"/>',
  speaker:   '<path d="M11 5 6 9H2v6h4l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7M19 5a9 9 0 0 1 0 14"/>',
  // capability categories
  sigma:     '<path d="M18 7V4H6l6 8-6 8h12v-3"/>',
  book:      '<path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20M4 4.5A2.5 2.5 0 0 1 6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5z"/>',
  pen:       '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>',
  list:      '<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>',
  globe:     '<circle cx="12" cy="12" r="10"/><path d="M2 12h20M12 2a15 15 0 0 1 0 20 15 15 0 0 1 0-20z"/>',
  scan:      '<path d="M3 7V5a2 2 0 0 1 2-2h2M17 3h2a2 2 0 0 1 2 2v2M21 17v2a2 2 0 0 1-2 2h-2M7 21H5a2 2 0 0 1-2-2v-2"/><path d="M7 12h10"/>',
  zap:       '<path d="M13 2 3 14h9l-1 8 10-12h-9z"/>',
  gauge:     '<path d="M12 14l4-4"/><path d="M3.5 19a10 10 0 1 1 17 0"/>',
  target:    '<circle cx="12" cy="12" r="9"/><circle cx="12" cy="12" r="5"/><circle cx="12" cy="12" r="1.4"/>',
  shield:    '<path d="M12 2 4 6v6c0 5 3.5 8 8 10 4.5-2 8-5 8-10V6z"/>',
  shapes:    '<path d="M12 2 2 8l10 6 10-6z"/><path d="M2 16l10 6 10-6"/>',
  wave:      '<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>',
};
function icon(name, cls) {
  const p = ICONS[name];
  if (!p) return "";
  return `<svg class="ico${cls ? " " + cls : ""}" viewBox="0 0 24 24" width="16" height="16" fill="none" ` +
    `stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${p}</svg>`;
}

// ---- Modality + capability-category metadata ------------------------------
// Keep modality keys in sync with the catalog "modality" field.
const MODALITY = {
  text:        { icon: "chat",    label: "Chat" },
  code:        { icon: "code",    label: "Code" },
  reasoning:   { icon: "idea",    label: "Reasoning" },
  vision:      { icon: "eye",     label: "Vision" },
  embedding:   { icon: "hash",    label: "Embeddings" },
  image:       { icon: "image",   label: "Image gen" },
  "audio-stt": { icon: "mic",     label: "Speech→Text" },
  tts:         { icon: "speaker", label: "Text→Speech" },
};
const MOD_ORDER = ["text", "code", "reasoning", "vision", "embedding", "image", "audio-stt", "tts"];
function modOf(m) { return (m && m.modality) || "text"; }
function modMeta(k) { return MODALITY[k] || MODALITY.text; }
function modalityBadge(k) { const x = modMeta(k); return `<span class="badge mod mod-${k}">${icon(x.icon)} ${x.label}</span>`; }

// The capability dimensions shown as progress bars, per modality (mirrors the
// rating workflow). LLM-like modalities share one set so they're comparable.
const DIMS = {
  llm:         ["Reasoning", "Coding", "Math", "Knowledge", "Writing", "Instruction", "Multilingual"],
  vision:      ["Reasoning", "Vision", "OCR", "Knowledge", "Writing", "Multilingual"],
  embedding:   ["Retrieval", "Multilingual", "Efficiency"],
  image:       ["Image quality", "Prompt adherence", "Speed", "Versatility"],
  "audio-stt": ["Accuracy", "Multilingual", "Speed", "Robustness"],
  tts:         ["Voice quality", "Naturalness", "Speed", "Languages"],
};
const DIM_SET = { text: "llm", code: "llm", reasoning: "llm", vision: "vision", embedding: "embedding", image: "image", "audio-stt": "audio-stt", tts: "tts" };
function dimsFor(mod) { return DIMS[DIM_SET[mod] || "llm"]; }
const CAT_ICON = {
  Reasoning: "idea", Coding: "code", Math: "sigma", Knowledge: "book", Writing: "pen",
  Instruction: "list", Multilingual: "globe", Vision: "eye", OCR: "scan",
  Retrieval: "search", Efficiency: "zap",
  "Image quality": "image", "Prompt adherence": "target", Speed: "gauge", Versatility: "shapes",
  Accuracy: "target", Robustness: "shield", "Voice quality": "speaker", Naturalness: "wave", Languages: "globe",
};

// Native context window per family (prefix match, longest first). Display-only;
// the image actually serves at CTX_SIZE (default 4096 CPU / 8192 GPU).
const CONTEXT = [
  ["qwen3-coder", "256K"], ["qwen3-embedding", "32K"], ["qwen3", "32K–128K"],
  ["qwen2.5-coder", "32K–128K"], ["qwen2.5-vl", "32K–128K"], ["qwen2.5", "32K–128K"],
  ["gemma-3", "128K"], ["llama-3.2", "128K"], ["llama-3.1", "128K"],
  ["mistral-small", "128K"], ["mistral-7b", "32K"], ["phi-3.5", "128K"],
  ["deepseek-r1", "128K"], ["qwq", "32K–128K"], ["nomic-embed", "8K"], ["bge-large", "512"],
];
function contextFor(id) { const f = CONTEXT.find(([p]) => id.startsWith(p)); return f ? f[1] : ""; }

// Human labels for the compute target and engine, shown in the images table.
const COMPUTE_BADGE = { cpu: "CPU", cuda: "GPU (CUDA)", vulkan: "GPU (Vulkan)" };
function computeBadge(c) { return COMPUTE_BADGE[c] || "CPU"; }
function engineBadge(e) { return e === "podman" ? "Podman" : "Docker"; }
function ctxDefaultFor(compute) { return (compute === "cuda" || compute === "vulkan") ? 8192 : 4096; }

// ---- Per-model capability data (from model-meta.js) -----------------------
let META = {};          // id -> { strengths, summary, weakness, ratings }
let TAG_LIST = [];       // controlled strength vocabulary actually used
function loadMeta() {
  const d = window.MODEL_META;
  if (!d || !d.models) return;
  for (const x of d.models) META[x.id] = x;
  TAG_LIST = d.tagList || [];
}

// ---- System info + hardware performance estimator -------------------------
// SYSINFO = the engine machine the model actually runs in (RAM/CPU/GPU). Used
// to rate, per model, how well THIS machine would run it on CPU vs GPU.
let SYSINFO = null;
async function loadSysInfo() { try { SYSINFO = await api("/api/sysinfo"); } catch { SYSINFO = null; } }
function sysSummary() {
  if (!SYSINFO || !SYSINFO.mem_gb) return "current";
  return `${SYSINFO.mem_gb.toFixed(0)} GB${SYSINFO.gpu ? " · " + SYSINFO.gpu + " GPU" : " · no GPU"}`;
}

// Six performance levels (+ N/A), worst → best. The estimator returns one of
// these keys for CPU and for GPU; CSS maps each to a colour.
const PERF = {
  excellent:  { label: "Excellent" },
  good:       { label: "Good" },
  fair:       { label: "Fair" },
  warning:    { label: "Warning" },
  poor:       { label: "Poor" },
  impossible: { label: "Won't run" },
  na:         { label: "N/A" },
};
const RANKS = ["impossible", "poor", "warning", "fair", "good", "excellent"];
const lvl = (rank) => RANKS[Math.max(0, Math.min(5, rank))];
const worse = (a, b) => (RANKS.indexOf(a) <= RANKS.indexOf(b) ? a : b);

// Cap a base level by whether the model fits `capacity` GB, with a headroom
// penalty so a barely-fitting model never reads "excellent".
function capByFit(level, need, capacity) {
  if (!capacity || !need) return level;
  if (need > capacity + 0.01) return "impossible";
  const head = capacity - need;
  if (head < 1.5) return worse(level, "warning");
  if (head < 3)   return worse(level, "fair");
  return level;
}

// Estimate CPU and GPU performance for this model on this machine. Heuristic:
// modality + size set a base "speed" tier; RAM/VRAM fit then caps it (and marks
// it impossible if it can't load). Unified-memory Macs use system RAM as the
// GPU pool, so we cap GPU by total RAM too.
function hwPerf(m) {
  const mod = modOf(m), gb = m.size_gb || 0;
  const cap = (SYSINFO && SYSINFO.mem_gb) || 0;
  const hasGPU = !!(SYSINFO && SYSINFO.gpu);

  let cpu;
  if (mod === "tts" || mod === "embedding") cpu = "excellent";          // tiny, CPU-native
  else if (mod === "audio-stt") cpu = gb > 1 ? "good" : "excellent";    // whisper is CPU-friendly
  else if (mod === "image") cpu = gb > 3 ? "poor" : "warning";          // diffusion on CPU is very slow
  else {                                                                // llm / code / reasoning / vision
    cpu = gb <= 1.5 ? "excellent" : gb <= 3 ? "good" : gb <= 6 ? "fair" : gb <= 10 ? "warning" : "poor";
    if (mod === "reasoning") cpu = lvl(Math.max(1, RANKS.indexOf(cpu) - 1)); // verbose CoT → slower
    if (mod === "vision")    cpu = lvl(Math.max(1, RANKS.indexOf(cpu) - 1)); // image encode adds cost
  }
  cpu = capByFit(cpu, m.min_ram_gb || gb, cap);

  let gpu;
  if (mod === "tts") gpu = "na";                                        // Piper has no GPU path
  else if (!hasGPU) gpu = "na";                                         // no GPU configured here
  else {
    gpu = mod === "image" ? (gb > 6 ? "good" : "excellent") : "excellent"; // ggml on GPU is fast
    gpu = capByFit(gpu, m.min_vram_gb || gb, cap);
  }
  return { cpu, gpu };
}
function perfChip(kind, level) {
  const lab = (PERF[level] || PERF.na).label;
  const name = kind === "gpu" ? "GPU" : "CPU";
  return `<span class="perf perf-${level}" title="${name} performance on your system: ${lab}">` +
    `${icon(kind)} <span class="perf-name">${name}</span> ${lab}</span>`;
}
function perfMini(m) {
  const h = hwPerf(m);
  return `<span class="perf-mini perf-${h.cpu}" title="CPU: ${(PERF[h.cpu] || PERF.na).label}">${icon("cpu")}</span>` +
    `<span class="perf-mini perf-${h.gpu}" title="GPU: ${(PERF[h.gpu] || PERF.na).label}">${icon("gpu")}</span>`;
}

// ---- Catalog load + custom model picker (combobox) ------------------------

async function loadCatalog() {
  CATALOG = await api("/api/catalog");
  loadMeta();
  await loadSysInfo();
  $("modelSearch").placeholder = `Search ${CATALOG.length} models…`;
  buildFilters();
  renderList();
  selectDefault();
}
function selectedModel() { return SELECTED; }

let SELECTED = null;
let FILTER = { q: "", mods: new Set(), strengths: new Set(), maxSize: Infinity };

// Build the filter controls inside the picker panel (modality checkboxes,
// key-strength chips, size slider). Re-run on each toggle to refresh "on" state.
function buildFilters() {
  const mods = [...new Set(CATALOG.map(modOf))].sort((a, b) => MOD_ORDER.indexOf(a) - MOD_ORDER.indexOf(b));
  $("filterModalities").innerHTML = mods.map((k) => {
    const n = CATALOG.filter((m) => modOf(m) === k).length;
    const on = FILTER.mods.has(k);
    return `<button type="button" class="fchip${on ? " on" : ""}" data-mod="${k}">${icon(modMeta(k).icon)} ${modMeta(k).label} <span class="fn">${n}</span></button>`;
  }).join("");
  $("filterModalities").querySelectorAll(".fchip").forEach((b) => b.onclick = () => {
    const k = b.dataset.mod; FILTER.mods.has(k) ? FILTER.mods.delete(k) : FILTER.mods.add(k);
    buildFilters(); renderList();
  });

  const grp = $("filterStrengths").closest(".filter-group");
  if (!TAG_LIST.length) { if (grp) grp.hidden = true; }
  else {
    if (grp) grp.hidden = false;
    $("filterStrengths").innerHTML = TAG_LIST.map((t) => {
      const on = FILTER.strengths.has(t);
      return `<button type="button" class="fchip sm${on ? " on" : ""}" data-tag="${escapeHtml(t)}">${escapeHtml(t)}</button>`;
    }).join("");
    $("filterStrengths").querySelectorAll(".fchip").forEach((b) => b.onclick = () => {
      const t = b.dataset.tag; FILTER.strengths.has(t) ? FILTER.strengths.delete(t) : FILTER.strengths.add(t);
      buildFilters(); renderList();
    });
  }
  $("sizeVal").textContent = FILTER.maxSize === Infinity ? "any" : `≤ ${FILTER.maxSize} GB`;
}

// Apply the active filters. Modality = OR within selected; strengths = OR;
// size = max; search = substring over name/id/description/strengths.
function filteredModels() {
  const q = FILTER.q.toLowerCase().trim();
  return CATALOG.filter((m) => {
    if (FILTER.mods.size && !FILTER.mods.has(modOf(m))) return false;
    if (FILTER.maxSize !== Infinity && (m.size_gb || 0) > FILTER.maxSize) return false;
    const tags = (META[m.id] && META[m.id].strengths) || [];
    if (FILTER.strengths.size && !tags.some((t) => FILTER.strengths.has(t))) return false;
    if (q) {
      const hay = `${m.name} ${m.id} ${m.description || ""} ${tags.join(" ")}`.toLowerCase();
      if (!hay.includes(q)) return false;
    }
    return true;
  });
}
function renderList() {
  const list = filteredModels();
  $("modelCount").textContent = `${list.length} of ${CATALOG.length}`;
  if (!list.length) { $("modelList").innerHTML = `<li class="combo-empty">No models match these filters.</li>`; return; }
  $("modelList").innerHTML = list.map((m) => {
    const sel = SELECTED && SELECTED.id === m.id;
    const tags = (((META[m.id] && META[m.id].strengths) || []).slice(0, 3))
      .map((t) => `<span class="tag xs">${escapeHtml(t)}</span>`).join("");
    return `<li class="combo-opt${sel ? " sel" : ""}" role="option" aria-selected="${sel}" data-id="${m.id}">
      <span class="opt-ic mod-${modOf(m)}">${icon(modMeta(modOf(m)).icon)}</span>
      <span class="opt-main">
        <span class="opt-name">${escapeHtml(m.name)}${m.recommended ? ` ${icon("star", "star-rec")}` : ""}</span>
        <span class="opt-tags">${tags}</span>
      </span>
      <span class="opt-size">${m.size_gb} GB</span>
      <span class="opt-perf">${perfMini(m)}</span>
    </li>`;
  }).join("");
  $("modelList").querySelectorAll(".combo-opt").forEach((li) =>
    li.onclick = () => { setModel(li.dataset.id); closePanel(); });
}
function openPanel() { $("modelPanel").hidden = false; $("modelPicker").classList.add("open"); $("modelTrigger").setAttribute("aria-expanded", "true"); $("modelSearch").focus(); }
function closePanel() { $("modelPanel").hidden = true; $("modelPicker").classList.remove("open"); $("modelTrigger").setAttribute("aria-expanded", "false"); }
function togglePanel() { $("modelPanel").hidden ? openPanel() : closePanel(); }

function triggerHtml(m) {
  return `<span class="trig-ic mod-${modOf(m)}">${icon(modMeta(modOf(m)).icon)}</span>` +
    `<span class="trig-main"><span class="trig-name">${escapeHtml(m.name)}</span>` +
    `<span class="trig-sub">${escapeHtml(m.params)} · ${m.size_gb} GB · ${modMeta(modOf(m)).label}</span></span>` +
    `<span class="trig-perf">${perfMini(m)}</span>${icon("chevron", "caret")}`;
}
function setModel(id) {
  const m = CATALOG.find((x) => x.id === id);
  if (!m) return;
  SELECTED = m;
  $("model").value = id;                         // hidden input keeps build()/template flows working
  $("modelTrigger").innerHTML = triggerHtml(m);
  renderDetail(m);
  $("modelList").querySelectorAll(".combo-opt").forEach((li) => {
    const on = li.dataset.id === id;
    li.classList.toggle("sel", on); li.setAttribute("aria-selected", on);
  });
}
function selectDefault() {
  if (SELECTED && CATALOG.some((m) => m.id === SELECTED.id)) { setModel(SELECTED.id); return; }
  const r = CATALOG.find((m) => m.recommended) || CATALOG[0];
  if (r) setModel(r.id);
}

// One capability progress bar (0–100), coloured by tier, with its category icon.
function capBar(label, val) {
  const v = Math.max(0, Math.min(100, val || 0));
  const tier = v >= 80 ? "b-hi" : v >= 55 ? "b-mid" : v >= 35 ? "b-lo" : "b-min";
  const ic = CAT_ICON[label] ? icon(CAT_ICON[label]) : "";
  return `<div class="cap-row"><span class="cap-label">${ic}<span>${escapeHtml(label)}</span></span>` +
    `<span class="cap-track"><span class="cap-fill ${tier}" style="width:${v}%"></span></span>` +
    `<span class="cap-val">${v}</span></div>`;
}

// The rich detail card shown under the picker for the selected model.
function renderDetail(m) {
  if (!m) { $("modelHint").innerHTML = ""; return; }
  const mod = modOf(m), meta = META[m.id] || {};
  const vram = m.min_vram_gb > 0 ? ` · ~${m.min_vram_gb} GB VRAM` : "";
  const ctx = contextFor(m.id);
  const bars = meta.ratings
    ? dimsFor(mod).filter((d) => d in meta.ratings).map((d) => capBar(d, meta.ratings[d])).join("")
    : `<div class="cap-empty">Capability ratings unavailable for this model.</div>`;
  const tags = (meta.strengths || []).map((t) => `<span class="tag">${escapeHtml(t)}</span>`).join("");
  const rec = m.recommended ? `<span class="badge rec">${icon("star")} Recommended</span>` : "";

  $("modelHint").innerHTML =
    `<div class="md-head">${modalityBadge(mod)} ${rec}` +
      `<span class="md-perf">${perfChip("cpu", hwPerf(m).cpu)} ${perfChip("gpu", hwPerf(m).gpu)}</span></div>` +
    `<div class="md-spec">${escapeHtml(m.params)} · ${m.size_gb} GB · ~${m.min_ram_gb} GB RAM${vram}${ctx ? " · context " + ctx : ""}</div>` +
    (m.description ? `<div class="md-desc">${escapeHtml(m.description)}</div>` : "") +
    (meta.summary ? `<div class="md-sum"><b>Best at</b> ${escapeHtml(meta.summary)}</div>` : "") +
    (meta.weakness ? `<div class="md-weak"><b>Watch-outs</b> ${escapeHtml(meta.weakness)}</div>` : "") +
    (tags ? `<div class="md-tags">${tags}</div>` : "") +
    `<div class="caps">${bars}</div>` +
    `<div class="ctxnote">Performance levels are estimates for your ${sysSummary()} system. ` +
      `Capability scores (0–100) are rough guidance, calibrated within local open models.</div>`;

  const imgInput = $("imageName");
  if (!imgInput.dataset.touched) imgInput.value = "local-llm/" + m.id;
}
// Back-compat alias: older flows (template load, build reconnect) call this.
function updateHint() { renderDetail(SELECTED); }

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
    progressEl().classList.add("error"); setBar(100); setStatus(line);
    // Auto-reveal the log so the cause (classifier message + streamed detail) is
    // visible without the user having to click "Show log".
    const log = $("buildLog");
    if (log.hidden) {
      log.hidden = false;
      const t = $("logToggle"); if (t) t.textContent = "Hide log";
    }
    log.scrollTop = log.scrollHeight;
    return;
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
  if (c.model_id && CATALOG.some((m) => m.id === c.model_id)) setModel(c.model_id);
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

    const imMod = (im.Labels && im.Labels["local-llm.modality"]) || "text";
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td><a href="#" class="link tpl" title="Use as a template for a new build">${displayName}</a><br>${modalityBadge(imMod)}</td>
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

  if (cfg.model_id && CATALOG.some((m) => m.id === cfg.model_id)) setModel(cfg.model_id);
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
    const body = { ref, port, engine: engine || "docker" };
    // Optional init-prompt override (no rebuild): sent only when non-empty so a
    // blank field leaves each image's baked prompt untouched.
    const promptOverride = ($("runPromptOverride") && $("runPromptOverride").value || "").trim();
    if (promptOverride) body.system_prompt = promptOverride;
    await api("/api/run", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
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
      <td class="actions">
        <button class="small" data-act="logs">Logs</button>
        <button class="small danger" data-act="stop">Stop &amp; remove</button>
      </td>`;
    tr.querySelector('[data-act="logs"]').onclick = () =>
      showContainerLogs(c.ID || c.Names, c.Engine || "docker", c.Names || c.ID);
    tr.querySelector('[data-act="stop"]').onclick = async () => {
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

// Fetch + show the tail of a container's logs in a popup (useful when a model
// crashes seconds after Run, so it's gone from the list before you can read it).
async function showContainerLogs(id, engine, name) {
  let logs;
  try {
    const q = `id=${encodeURIComponent(id)}&engine=${encodeURIComponent(engine)}&tail=400`;
    logs = (await api("/api/container/logs?" + q)).logs || "(no output yet)";
  } catch (e) { alert("Couldn't fetch logs: " + e.message); return; }
  const w = window.open("", "_blank", "width=900,height=600");
  if (!w) { alert("Allow popups to view logs."); return; }
  w.document.title = "logs: " + (name || id);
  w.document.body.style.cssText = "margin:0;background:#0b0b0b;color:#ddd";
  w.document.body.innerHTML =
    `<pre style="white-space:pre-wrap;font:12px ui-monospace,monospace;padding:1rem;margin:0">${escapeHtml(logs)}</pre>`;
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

// Custom model picker (combobox) wiring.
$("modelTrigger").addEventListener("click", (e) => { e.preventDefault(); togglePanel(); });
$("modelSearch").addEventListener("input", () => { FILTER.q = $("modelSearch").value; renderList(); });
$("filterSize").addEventListener("input", () => {
  const v = parseFloat($("filterSize").value);
  FILTER.maxSize = v >= parseFloat($("filterSize").max) ? Infinity : v;
  $("sizeVal").textContent = FILTER.maxSize === Infinity ? "any" : `≤ ${FILTER.maxSize} GB`;
  renderList();
});
$("filterReset").addEventListener("click", () => {
  FILTER = { q: "", mods: new Set(), strengths: new Set(), maxSize: Infinity };
  $("modelSearch").value = ""; $("filterSize").value = $("filterSize").max;
  buildFilters(); renderList();
});
// Close the panel on outside-click or Escape.
document.addEventListener("click", (e) => { if (!$("modelPicker").contains(e.target)) closePanel(); });
document.addEventListener("keydown", (e) => { if (e.key === "Escape") closePanel(); });

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
