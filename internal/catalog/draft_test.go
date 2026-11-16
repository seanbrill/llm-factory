package catalog

import "testing"

// A draft model must be another llama.cpp text model in the catalog, from
// the same family (same tokenizer), and smaller than what it drafts for.
func TestDraftPairs(t *testing.T) {
	c := defaultCatalog(t)
	n := 0
	for _, m := range c.Models {
		if m.Draft == "" {
			continue
		}
		n++
		d, ok := c.Get(m.Draft)
		switch {
		case !ok:
			t.Errorf("%s: draft %q isn't in the catalog", m.ID, m.Draft)
		case d.ID == m.ID:
			t.Errorf("%s drafts for itself", m.ID)
		case d.Rt() != "cpp" || d.Mod() == "embedding" || d.MMProjFile != "":
			t.Errorf("%s: draft %s isn't a llama.cpp text model", m.ID, d.ID)
		case d.SizeGB >= m.SizeGB/4:
			t.Errorf("%s: draft %s (%.1f GB) is too big to speed up a %.1f GB model", m.ID, d.ID, d.SizeGB, m.SizeGB)
		case d.Draft != "":
			t.Errorf("%s: draft %s has a draft of its own", m.ID, d.ID)
		}
		if m.MMProjFile != "" {
			t.Errorf("%s: vision models run without look-ahead, a draft would never load", m.ID)
		}
	}
	if n == 0 {
		t.Error("no draft pairs in the catalog")
	}
}
