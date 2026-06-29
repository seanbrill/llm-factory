// Command videogate is the in-container entrypoint for video-generation images.
//
// stable-diffusion.cpp's video support (Wan / LTX, mode `vid_gen`) is exposed
// through the `sd` CLI, not the HTTP sd-server. So this shim wraps it: it serves
// POST /generate, assembles the CLI flags from the request plus the baked model
// and its extra weights (a manifest of VAE / T5 / high-noise files), runs one
// generation, and returns the resulting clip as a base64 data URL.
//
// Stdlib only. The flags follow the documented Wan 2.2 invocation; uncertain
// bits (binary name, output container) are auto-detected, and EXTRA_ARGS lets the
// exact CLI be tuned at run time without a rebuild.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("[videogate] ")
	mux := http.NewServeMux()
	mux.HandleFunc("/generate", handleGenerate)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	port := env("PORT", "8080")
	log.Printf("listening on :%s (sd=%s, model=%s)", port, sdBin(), env("MODEL_PATH", "/models/model.gguf"))
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// sdBin finds the stable-diffusion.cpp CLI binary, whose name has been `sd` and
// `sd-cli` across versions. SD_BIN overrides.
func sdBin() string {
	if b := os.Getenv("SD_BIN"); b != "" {
		return b
	}
	for _, name := range []string{"sd-cli", "sd"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "sd-cli"
}

type weight struct {
	File string `json:"file"`
	Role string `json:"role"`
}

// roleFlag maps a baked extra-weight role to its sd CLI flag.
func roleFlag(role string) string {
	switch role {
	case "high_noise_diffusion", "high_noise":
		return "--high-noise-diffusion-model"
	case "vae":
		return "--vae"
	case "t5", "t5xxl", "umt5", "umt5xxl":
		return "--t5xxl"
	case "clip_l":
		return "--clip_l"
	case "clip_g":
		return "--clip_g"
	case "clip_vision":
		return "--clip_vision"
	default:
		return ""
	}
}

func loadManifest() []weight {
	data, err := os.ReadFile(env("EXTRA_MANIFEST", "/models/extra_files.json"))
	if err != nil {
		return nil
	}
	var ws []weight
	_ = json.Unmarshal(data, &ws)
	return ws
}

type genReq struct {
	Prompt         string  `json:"prompt"`
	Negative       string  `json:"negative"`
	Frames         int     `json:"frames"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Steps          int     `json:"steps"`
	CfgScale       float64 `json:"cfg_scale"`
	Seed           int     `json:"seed"`
	SamplingMethod string  `json:"sampling_method"`
	FlowShift      float64 `json:"flow_shift"`
}

func handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req genReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeErr(w, http.StatusBadRequest, "prompt is required")
		return
	}
	// Defaults tuned for ~12 GB at 480p (from the Wan 2.2 docs).
	def(&req.Width, 832)
	def(&req.Height, 480)
	def(&req.Frames, 33)
	def(&req.Steps, 10)
	deff(&req.CfgScale, 3.5)
	deff(&req.FlowShift, 3.0)
	if req.SamplingMethod == "" {
		req.SamplingMethod = "euler"
	}

	outDir, err := os.MkdirTemp("", "vidgen-")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(outDir)
	// stable-diffusion.cpp writes single-file video only as .avi, .webm, or
	// animated .webp (NO .mp4 muxer). webm is the browser-native default;
	// VIDEO_EXT overrides at run time (e.g. "webp") without a rebuild.
	ext := strings.TrimPrefix(strings.ToLower(env("VIDEO_EXT", "webm")), ".")
	outPath := filepath.Join(outDir, "out."+ext)

	args := []string{"-M", "vid_gen", "--diffusion-model", env("MODEL_PATH", "/models/model.gguf")}
	for _, wt := range loadManifest() {
		if flag := roleFlag(wt.Role); flag != "" {
			args = append(args, flag, filepath.Join(env("EXTRA_DIR", "/models/extra"), wt.File))
		}
	}
	args = append(args,
		"-p", req.Prompt,
		"-W", strconv.Itoa(req.Width), "-H", strconv.Itoa(req.Height),
		"--video-frames", strconv.Itoa(req.Frames),
		"--steps", strconv.Itoa(req.Steps),
		"--cfg-scale", ftoa(req.CfgScale),
		"--sampling-method", req.SamplingMethod,
		"--flow-shift", ftoa(req.FlowShift),
		"--diffusion-fa",
		"-o", outPath,
	)
	if req.Negative != "" {
		args = append(args, "-n", req.Negative)
	}
	if req.Seed != 0 {
		args = append(args, "--seed", strconv.Itoa(req.Seed))
	}
	if env("OFFLOAD_TO_CPU", "1") == "1" {
		args = append(args, "--offload-to-cpu")
	}
	// VAE-decode the video in tiles so the decode compute buffer fits consumer
	// GPUs. --offload-to-cpu only moves the *weights* to RAM; the VAE decode
	// activation buffer is the spike — full-res Wan decode reserves ~20GB VRAM and
	// OOMs on <=12GB cards, failing AFTER sampling with "vae decode compute failed"
	// (so you waste the whole denoise). Tiling caps that. Default on; set
	// VAE_TILING=0 to disable on big-VRAM GPUs for a faster single-shot decode.
	if env("VAE_TILING", "1") == "1" {
		args = append(args, "--vae-tiling")
	}
	if extra := os.Getenv("EXTRA_ARGS"); strings.TrimSpace(extra) != "" {
		args = append(args, strings.Fields(extra)...)
	}

	log.Printf("run: %s %s", sdBin(), strings.Join(args, " "))
	cmd := exec.CommandContext(r.Context(), sdBin(), args...)
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		writeErr(w, http.StatusInternalServerError, "sd CLI failed: "+err.Error()+": "+tail(stderr.String(), 800))
		return
	}

	produced := findOutput(outDir, outPath)
	if produced == "" {
		writeErr(w, http.StatusInternalServerError, "no video file produced — sd log: "+tail(stderr.String(), 800))
		return
	}
	data, err := os.ReadFile(produced)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	mime := mimeFor(produced)
	writeJSON(w, http.StatusOK, map[string]any{
		"video": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
		"mime":  mime,
	})
}

// findOutput returns the generated clip — the expected -o path, else the first
// video file the CLI dropped in the dir (its container choice varies).
func findOutput(dir, want string) string {
	if fi, err := os.Stat(want); err == nil && fi.Size() > 0 {
		return want
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mp4", ".webm", ".gif", ".mkv", ".webp", ".mov":
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

func mimeFor(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".webm":
		return "video/webm"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mkv":
		return "video/x-matroska"
	case ".mov":
		return "video/quicktime"
	default:
		return "video/mp4"
	}
}

func def(p *int, v int)        { if *p == 0 { *p = v } }
func deff(p *float64, v float64) { if *p == 0 { *p = v } }
func ftoa(f float64) string    { return strconv.FormatFloat(f, 'g', -1, 64) }
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
