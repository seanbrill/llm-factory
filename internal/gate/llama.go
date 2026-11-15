package gate

import (
	"os"
	"strconv"
	"strings"
)

// Llama is how llama-server is started: by llmgate inside a model image and
// by llmpack from a native package.
type Llama struct {
	Model    string // GGUF path
	Host     string // listen address; "" = 127.0.0.1 (only the gate in front reaches it)
	Port     int
	CtxSize  int
	Threads  int // 0 = llama.cpp's own choice (its performance cores)
	Parallel int // 0 = 1
	// GPULayers is --n-gpu-layers: "" leaves llama-server's default, else a
	// number, "auto" (as many as fit in the GPU's memory) or "all".
	GPULayers       string
	MMProj          string // vision projector; "" or an empty file = text model
	MMProjNoOffload bool   // keep the projector on the CPU (Vulkan VLM caveat)
	Modality        string // text|code|reasoning|vision|embedding (drives flags)
	NoWebUI         bool   // turn llama-server's own web page off (prebuilt releases carry one)
	Extra           []string
}

// Args is llama-server's command line (without the program).
func (l Llama) Args() []string {
	host := l.Host
	if host == "" {
		host = "127.0.0.1"
	}
	parallel := l.Parallel
	if parallel <= 0 {
		parallel = 1
	}
	args := []string{
		"--model", l.Model,
		"--host", host,
		"--port", strconv.Itoa(l.Port),
		"--ctx-size", strconv.Itoa(l.CtxSize),
	}
	if l.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(l.Threads), "--threads-batch", strconv.Itoa(l.Threads))
	}
	args = append(args,
		"--parallel", strconv.Itoa(parallel),
		"--cont-batching",
		"--jinja",
		"--metrics",
	)
	if l.NoWebUI {
		args = append(args, "--no-webui")
	}
	if g := strings.TrimSpace(l.GPULayers); g != "" && g != "0" {
		args = append(args, "--n-gpu-layers", g)
	}
	// Embedding models serve /v1/embeddings instead of chat; --embedding
	// switches llama-server into that mode (pooling auto-detects from the model).
	if l.Modality == "embedding" {
		args = append(args, "--embedding")
	}
	// Vision projector: only when a real (non-empty) mmproj exists. Text model
	// images bake a zero-byte placeholder, which is skipped. On Vulkan the
	// projector encode can be kept on the CPU (--no-mmproj-offload) to dodge
	// the known Venus VLM-encoder bug.
	if l.MMProj != "" {
		if fi, err := os.Stat(l.MMProj); err == nil && fi.Size() > 0 {
			args = append(args, "--mmproj", l.MMProj)
			if l.MMProjNoOffload {
				args = append(args, "--no-mmproj-offload")
			}
		}
	}
	return append(args, l.Extra...)
}
