package catalog

import (
	"encoding/json"
	"testing"
)

// The embedded default must stay internally consistent: downloads land in
// ./models by local filename, so two entries may share a file only if they
// fetch it from the same URL (e.g. a text model and its vision variant).
func TestDefaultCatalogConsistent(t *testing.T) {
	var c Catalog
	if err := json.Unmarshal(defaultJSON, &c); err != nil {
		t.Fatalf("parse models.default.json: %v", err)
	}
	ids := map[string]bool{}
	urls := map[string]string{} // local filename -> download URL
	claim := func(id, file, url string) {
		if file == "" {
			return
		}
		if url == "" {
			t.Errorf("%s: %s has no download URL", id, file)
		}
		if prev, ok := urls[file]; ok && prev != url {
			t.Errorf("%s: %s is fetched from two URLs:\n  %s\n  %s", id, file, prev, url)
		}
		urls[file] = url
	}
	for _, m := range c.Models {
		if ids[m.ID] {
			t.Errorf("duplicate model id %q", m.ID)
		}
		ids[m.ID] = true
		if m.Legacy && m.Recommended {
			t.Errorf("%s: a legacy model can't be recommended", m.ID)
		}
		claim(m.ID, m.File, m.URL)
		claim(m.ID, m.MMProjFile, m.MMProjURL)
		for _, w := range m.ExtraFiles {
			claim(m.ID, w.File, w.URL)
		}
	}
}
