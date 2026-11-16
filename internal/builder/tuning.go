package builder

// Runtime tuning (docs/TUNING.md): baked into a llama.cpp chat image as ENV
// lines the factory appends to its runtime stage (llmgate turns them into
// llama-server flags), plus the label local-llm.tuning, and passed again at
// run time as -e overrides. The benchmark's engine calls live here too.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/seanbrill/local-llm/internal/tuning"
)

// LabelTuning is the image (and container) label holding the tuning JSON;
// LabelDraft names the draft model an image bakes.
const (
	LabelTuning = "local-llm.tuning"
	LabelDraft  = "local-llm.draft"
)

var envValueRe = regexp.MustCompile(`^[A-Za-z0-9_./:+-]*$`)

// tuningDockerfile appends the tuning to a runtime Dockerfile's last stage:
// one ENV instruction with the set variables (later ENV wins over the
// Dockerfile's own PARALLEL=1 and CTX_SIZE), and, with a draft model, the
// COPY that bakes it at tuning.ImageDraftPath. Only the image's last layers
// change, so a retune reuses the cached llama.cpp compile.
func tuningDockerfile(df []byte, t tuning.Tuning, draft bool) ([]byte, error) {
	env := t.EnvMap(tuning.ImageDraftPath)
	if !draft {
		delete(env, tuning.EnvDraftModelPath)
	}
	if len(env) == 0 && !draft {
		return df, nil
	}
	keys := make([]string, 0, len(env))
	for k, v := range env {
		if !envValueRe.MatchString(v) {
			return nil, fmt.Errorf("tuning value %s=%q can't be baked", k, v)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.Write(df)
	if len(df) > 0 && df[len(df)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("\n# Runtime tuning, appended by the factory (docs/TUNING.md).\n")
	if draft {
		b.WriteString("COPY draft.gguf " + tuning.ImageDraftPath + "\n")
	}
	if len(keys) > 0 {
		b.WriteString("ENV")
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%q", k, env[k])
		}
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

// BakedTuning is the tuning an image was built with (the zero Tuning when
// it has none), from its labels.
func BakedTuning(labels map[string]string) tuning.Tuning {
	return tuning.FromLabel(labels[LabelTuning])
}

// EngineOutput runs `<engine> args...` and returns its combined output. For
// read-only queries and the benchmark's container lifecycle.
func EngineOutput(ctx context.Context, engine string, args ...string) ([]byte, error) {
	out, err := engineCmd(ctx, engine, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %v: %s", resolveEngine(engine), args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// PublishedPort is the host port a container publishes its port 8080 on
// (`<engine> port <name> 8080`), for containers run with an ephemeral port.
func PublishedPort(ctx context.Context, engine, name string) (int, error) {
	out, err := EngineOutput(ctx, engine, "port", name, "8080/tcp")
	if err != nil {
		return 0, err
	}
	return parsePortOutput(string(out))
}

// parsePortOutput reads `port` output: "127.0.0.1:49153" (maybe several
// lines, IPv6 too).
func parsePortOutput(s string) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.LastIndex(line, ":"); i >= 0 {
			if p, err := strconv.Atoi(line[i+1:]); err == nil && p > 0 {
				return p, nil
			}
		}
	}
	return 0, fmt.Errorf("no published port in %q", strings.TrimSpace(s))
}

// ContainerMemGB is a container's memory use in GB, from
// `<engine> stats --no-stream`.
func ContainerMemGB(ctx context.Context, engine, name string) (float64, error) {
	out, err := EngineOutput(ctx, engine, "stats", "--no-stream", "--format", "{{.MemUsage}}", name)
	if err != nil {
		return 0, err
	}
	return parseMemUsage(string(out))
}

var sizeRe = regexp.MustCompile(`^\s*([0-9.]+)\s*([kKMGT]?i?B)`)

// parseMemUsage reads the used half of "1.234GiB / 12GiB".
func parseMemUsage(s string) (float64, error) {
	used, _, _ := strings.Cut(strings.TrimSpace(s), "/")
	m := sizeRe.FindStringSubmatch(used)
	if m == nil {
		return 0, fmt.Errorf("can't read memory use from %q", strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	mult := map[string]float64{
		"B": 1, "kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "kiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
	}[m[2]]
	if mult == 0 {
		mult = 1
	}
	return v * mult / 1e9, nil
}
