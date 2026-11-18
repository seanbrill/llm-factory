package builder

import (
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// The llama.cpp Dockerfiles build a runtime-base stage with no model in it,
// and the model on top in the last stage, so a plain build is unchanged.
func TestRuntimeBaseStage(t *testing.T) {
	b := &Builder{BaseDir: filepath.Join("..", "..")}
	for _, compute := range []string{"cpu", "cuda", "vulkan"} {
		r, err := b.resolveRuntime(RuntimeOptions{Compute: compute})
		if err != nil {
			t.Fatal(err)
		}
		df := logical(string(r.dockerfile))
		i := strings.Index(df, " AS runtime-base\n")
		j := strings.Index(df, "FROM runtime-base AS runtime\n")
		if i < 0 || j < i {
			t.Fatalf("%s: no runtime-base stage before the model stage", compute)
		}
		base, model := df[i:j], df[j:]
		for _, f := range []string{"model.gguf", "mmproj.gguf", "system_prompt.txt", "harness/"} {
			if strings.Contains(base, "COPY "+f) {
				t.Errorf("%s: runtime-base copies %s", compute, f)
			}
			if !strings.Contains(model, "COPY "+f) {
				t.Errorf("%s: the model stage should copy %s", compute, f)
			}
		}
		for _, want := range []string{"ENTRYPOINT", "MODEL_PATH=", "COPY --from=gate"} {
			if !strings.Contains(base, want) {
				t.Errorf("%s: runtime-base has no %s", compute, want)
			}
		}
		if strings.Count(model, "FROM ") != 1 {
			t.Errorf("%s: the model stage should be the last", compute)
		}
		if r.llamaRef == "unknown" || r.ref != "local-llm/runtime:"+compute+"-"+r.llamaRef+"-"+goruntime.GOARCH {
			t.Errorf("%s: ref %s", compute, r.ref)
		}
	}
	if _, err := b.resolveRuntime(RuntimeOptions{Compute: "metal"}); err == nil {
		t.Error("an unknown compute should fail")
	}
	if _, err := b.resolveRuntime(RuntimeOptions{Platform: "windows/amd64"}); err == nil {
		t.Error("an unknown platform should fail")
	}
	if ref, _ := b.RuntimeRef(RuntimeOptions{Compute: "cuda", Platform: "linux/amd64", ImageName: "reg.example/x/runtime", Tag: "t1"}); ref != "reg.example/x/runtime:t1" {
		t.Errorf("a named runtime: %s", ref)
	}
}

func TestRuntimeBuildArgs(t *testing.T) {
	got := strings.Join(runtimeBuildArgs("r:t", "cuda", "v0.9.0", "amd64", "linux/amd64", enginePodman, "/w"), " ")
	for _, want := range []string{"build -t r:t --target runtime-base", "local-llm.arch=amd64", "local-llm.llama_cpp_ref=v0.9.0",
		"local-llm.compute=cuda", "org.opencontainers.image.licenses=Apache-2.0 AND MIT", "--platform linux/amd64", "seccomp=unconfined", " /w"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if got := strings.Join(runtimeBuildArgs("r:t", "cpu", "v", "arm64", "", engineDocker, "/w"), " "); strings.Contains(got, "--platform") || strings.Contains(got, "seccomp") {
		t.Errorf("no platform, docker: %s", got)
	}
}
