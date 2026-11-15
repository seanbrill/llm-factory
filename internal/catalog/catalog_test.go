package catalog

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"
	"testing"
)

func defaultCatalog(t *testing.T) Catalog {
	t.Helper()
	var c Catalog
	if err := json.Unmarshal(defaultJSON, &c); err != nil {
		t.Fatalf("parse models.default.json: %v", err)
	}
	return c
}

// The embedded default must stay internally consistent: downloads land in
// ./models by local filename, so two entries may share a file only if they
// fetch it from the same URL (e.g. a text model and its vision variant).
func TestDefaultCatalogConsistent(t *testing.T) {
	c := defaultCatalog(t)
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

// Every download is a Hugging Face "resolve" URL with a plain local file name,
// so a typo shows up here rather than as a 404 halfway through a build.
func TestDefaultCatalogURLsWellFormed(t *testing.T) {
	check := func(id, file, raw string) {
		if raw == "" {
			return
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Errorf("%s: %q doesn't parse: %v", id, raw, err)
			return
		}
		if u.Scheme != "https" || u.Host != "huggingface.co" {
			t.Errorf("%s: %s isn't an https://huggingface.co URL", id, raw)
		}
		// /<owner>/<repo>/resolve/<revision>/<path...>
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(parts) < 5 || parts[2] != "resolve" || parts[3] == "" || parts[len(parts)-1] == "" {
			t.Errorf("%s: %s isn't a /<owner>/<repo>/resolve/<revision>/<file> URL", id, raw)
		}
		if strings.ContainsAny(raw, " \t\n") {
			t.Errorf("%s: %q contains whitespace", id, raw)
		}
		if file == "" || file != path.Base(file) || strings.ContainsAny(file, `/\ `) {
			t.Errorf("%s: local file name %q must be a plain file name", id, file)
		}
	}
	for _, m := range defaultCatalog(t).Models {
		if (m.File == "") != (m.URL == "") {
			t.Errorf("%s: file and url must be set together", m.ID)
		}
		if m.File != "" {
			check(m.ID, m.File, m.URL)
		}
		if m.MMProjFile != "" {
			check(m.ID, m.MMProjFile, m.MMProjURL)
		}
		for _, w := range m.ExtraFiles {
			check(m.ID, w.File, w.URL)
		}
	}
}

// Every TTS entry says whether it clones a voice, whether that needs the
// reference's transcript, its license and whether that allows commercial use
// (the Catalog's "Clones voices" chip and "Non-commercial" note read these).
func TestTTSEntriesStateVoiceAndLicense(t *testing.T) {
	// Decode raw objects too: a missing key and an explicit false look the same
	// once unmarshalled into the struct.
	var raw struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(defaultJSON, &raw); err != nil {
		t.Fatal(err)
	}
	models := defaultCatalog(t).Models
	if len(raw.Models) != len(models) {
		t.Fatalf("raw and typed catalogs differ in length: %d vs %d", len(raw.Models), len(models))
	}
	cloners := 0
	for i, m := range models {
		if m.Mod() != "tts" {
			continue
		}
		for _, key := range []string{"voice_clone", "clone_needs_transcript", "license", "commercial_use"} {
			if _, ok := raw.Models[i][key]; !ok {
				t.Errorf("%s: tts entry doesn't state %q", m.ID, key)
			}
		}
		if m.CommercialUse == nil {
			t.Errorf("%s: commercial_use must be true or false", m.ID)
		}
		if m.CloneNeedsTranscript && !m.VoiceClone {
			t.Errorf("%s: clone_needs_transcript without voice_clone", m.ID)
		}
		if m.VoiceClone {
			cloners++
		}
	}
	if cloners < 5 {
		t.Errorf("only %d voice-cloning TTS models in the catalog", cloners)
	}
	for _, m := range models {
		if m.Mod() != "tts" && (m.VoiceClone || m.CloneNeedsTranscript) {
			t.Errorf("%s: voice_clone and clone_needs_transcript are for tts models", m.ID)
		}
		if m.License != strings.TrimSpace(m.License) {
			t.Errorf("%s: license %q has surrounding spaces", m.ID, m.License)
		}
		// A license that forbids commercial use is said in so many words too,
		// so the model's description carries the warning wherever it is shown.
		if m.CommercialUse != nil && !*m.CommercialUse && !strings.Contains(strings.ToLower(m.Description), "non-commercial") {
			t.Errorf("%s: a non-commercial model's description must say non-commercial", m.ID)
		}
	}
}

// audio.cpp entries carry the server config the builder bakes: a loader
// family, a modality it serves (speech or transcription), and extra files with
// plain in-image names.
func TestAudioCppEntries(t *testing.T) {
	n := 0
	for _, m := range defaultCatalog(t).Models {
		if (m.Rt() == "audiocpp") != (m.AudioCpp != nil) {
			t.Errorf("%s: runtime audiocpp and an audiocpp block go together", m.ID)
			continue
		}
		if m.AudioCpp == nil {
			continue
		}
		n++
		ac := m.AudioCpp
		if ac.Family == "" {
			t.Errorf("%s: audiocpp.family is empty", m.ID)
		}
		switch m.Mod() {
		case "tts":
		case "audio-stt":
			if ac.DefaultVoice != "" {
				t.Errorf("%s: a speech-to-text model has no default voice", m.ID)
			}
		default:
			t.Errorf("%s: audio.cpp serves tts and audio-stt here, not %q", m.ID, m.Mod())
		}
		if m.File == "" {
			t.Errorf("%s: an audio.cpp model needs a GGUF", m.ID)
		}
		for _, w := range m.ExtraFiles {
			name := w.As
			if name == "" {
				name = w.File
			}
			if name != path.Base(name) || name == "model.gguf" || name == "audiocpp.json" {
				t.Errorf("%s: extra file %s lands as %q in the image", m.ID, w.File, name)
			}
			if w.Role == "" {
				t.Errorf("%s: extra file %s has no role", m.ID, w.File)
			}
		}
		// VoxCPM2 refuses to stream with retry_badcase on (its default).
		if ac.Family == "voxcpm2" && ac.Streaming && ac.RequestOptions["retry_badcase"] != false {
			t.Errorf("%s: VoxCPM2 streaming needs request_options.retry_badcase=false", m.ID)
		}
	}
	if n == 0 {
		t.Fatal("no audio.cpp models in the catalog")
	}
}

func TestModelLicenseFieldsRoundTrip(t *testing.T) {
	no := false
	in := Model{ID: "x", Modality: "tts", VoiceClone: true, CloneNeedsTranscript: true, License: "CPML", CommercialUse: &no}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"voice_clone":true`, `"clone_needs_transcript":true`, `"license":"CPML"`, `"commercial_use":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("marshalled model lacks %s: %s", want, b)
		}
	}
	var out Model
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.CommercialUse == nil || *out.CommercialUse || !out.VoiceClone || !out.CloneNeedsTranscript || out.License != "CPML" {
		t.Errorf("round trip lost fields: %+v", out)
	}
	// Unstated stays unstated: no commercial_use key for a model that sets none.
	b, _ = json.Marshal(Model{ID: "y"})
	if strings.Contains(string(b), "commercial_use") || strings.Contains(string(b), "voice_clone") {
		t.Errorf("unset fields should be omitted: %s", b)
	}
}
