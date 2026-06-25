package builder

import "testing"

func TestLabelValue(t *testing.T) {
	labels := "local-llm.tool=runtime,local-llm.route=ai,local-llm.ref=local-llm/qwen:latest"
	cases := map[string]string{
		"local-llm.tool":  "runtime",
		"local-llm.route": "ai",
		"local-llm.ref":   "local-llm/qwen:latest",
		"missing":         "",
	}
	for key, want := range cases {
		if got := labelValue(labels, key); got != want {
			t.Errorf("labelValue(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestPortFromName(t *testing.T) {
	cases := map[string]string{
		"localllm-8080": "8080",
		"localllm-5001": "5001",
		"noport":        "",
	}
	for name, want := range cases {
		if got := portFromName(name); got != want {
			t.Errorf("portFromName(%q) = %q, want %q", name, got, want)
		}
	}
}
