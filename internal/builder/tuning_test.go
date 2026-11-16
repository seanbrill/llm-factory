package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/tuning"
)

func TestTuningDockerfile(t *testing.T) {
	df, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(RuntimesDir), "Dockerfile.vulkan"))
	if err != nil {
		t.Fatal(err)
	}
	tu := tuning.Tuning{Preset: "balanced", CtxSize: 32768, FlashAttn: "on", KVCache: "q8_0", Parallel: 2,
		KVUnified: tuning.BoolPtr(true), Spec: "draft", DraftModel: "qwen3-0.6b", CacheRAMMB: tuning.IntPtr(1024),
		Temperature: tuning.FloatPtr(0.6)}
	out, err := tuningDockerfile(df, tu, true)
	if err != nil {
		t.Fatal(err)
	}
	tail := string(out[len(df):])
	for _, want := range []string{
		"COPY draft.gguf /models/draft.gguf\n",
		`ENV CACHE_RAM_MB="1024" CTX_SIZE="32768" DRAFT_MODEL_PATH="/models/draft.gguf" FLASH_ATTN="on" KV_CACHE_TYPE="q8_0" KV_UNIFIED="1" PARALLEL="2" SPEC_TYPE="draft" TEMPERATURE="0.6"`,
	} {
		if !strings.Contains(tail, want) {
			t.Errorf("missing %q in:\n%s", want, tail)
		}
	}
	// The appended lines land after the last stage's ENTRYPOINT: still that stage.
	if strings.LastIndex(string(out), "FROM ") > len(df) {
		t.Error("tuning opened a new stage")
	}
	// No tuning: the Dockerfile is untouched.
	if same, _ := tuningDockerfile(df, tuning.Tuning{}, false); string(same) != string(df) {
		t.Error("an empty tuning changed the Dockerfile")
	}
	// Values that could break out of the ENV line are refused.
	if _, err := tuningDockerfile(df, tuning.Tuning{FlashAttn: "on\nRUN rm -rf /"}, false); err == nil {
		t.Error("an unsafe value was baked")
	}
}

func TestRunArgsTuningAndBench(t *testing.T) {
	o := RunOptions{Ref: "local-llm/x:latest", HostPort: -1, Compute: "cpu", Name: "localllm-tune-abc", Tool: "tune",
		Env: []string{"CTX_SIZE=32768", "SPEC_TYPE="}, Labels: []string{"local-llm.tuning={}"}}
	got := strings.Join(runArgs("docker", "localllm-tune-abc", o, -1), " ")
	for _, want := range []string{"--name localllm-tune-abc", "-p 127.0.0.1::8080", "local-llm.tool=tune",
		"-e CTX_SIZE=32768 -e SPEC_TYPE= --label local-llm.tuning={} local-llm/x:latest"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if !strings.HasSuffix(got, " local-llm/x:latest") {
		t.Errorf("image isn't last: %s", got)
	}
}

func TestParseEngineOutputs(t *testing.T) {
	if p, err := parsePortOutput("0.0.0.0:49153\n[::]:49153\n"); err != nil || p != 49153 {
		t.Errorf("port: %d %v", p, err)
	}
	if _, err := parsePortOutput(""); err == nil {
		t.Error("empty port output parsed")
	}
	for in, want := range map[string]float64{"1.5GiB / 12GiB": 1.5 * (1 << 30) / 1e9, "812.3MiB / 11.68GiB": 812.3 * (1 << 20) / 1e9, "2.1GB / 12GB": 2.1} {
		got, err := parseMemUsage(in)
		if err != nil || got < want*0.999 || got > want*1.001 {
			t.Errorf("%q: %g %v (want %g)", in, got, err, want)
		}
	}
}
