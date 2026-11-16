package tuning

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// remoteHeaderBytes bounds a remote header read: the model's own keys come
// before the tokenizer, which the read stops at.
const remoteHeaderBytes = 16 << 20

// FetchArch reads the architecture metadata of a GGUF file on a web server
// (a Hugging Face download URL) with a range request, without downloading
// the weights. It stops at the tokenizer, so the template-derived fields
// (ReasoningEffort, Thinks) come only from the architecture there.
func FetchArch(ctx context.Context, client *http.Client, url string) (ArchInfo, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ArchInfo{}, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", remoteHeaderBytes-1))
	resp, err := client.Do(req)
	if err != nil {
		return ArchInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return ArchInfo{}, fmt.Errorf("gguf header: %s answered %s", req.URL.Host, resp.Status)
	}
	stop := func(key string, m *GGUFMeta) bool {
		arch := m.Arch()
		return arch != "" && strings.HasPrefix(key, "tokenizer.") && m.KV[arch+".block_count"] != nil
	}
	m, err := ReadGGUFMetaUntil(io.LimitReader(resp.Body, remoteHeaderBytes), stop)
	if err != nil {
		return ArchInfo{}, err
	}
	return m.Info()
}
