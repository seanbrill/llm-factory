package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/ensemble"
)

// MountMembers turns an ensemble's llama.cpp members into members with
// mounted weights (ensemble.WeightsMounted): each runs from the model-less
// runtime for its compute (docs/RUNTIME.md), and names its weights' file,
// size, SHA-256 and download address, and its projector's, so an app can
// download them once and hand the Conductor the folder (WEIGHTS_DIR). The
// weights are downloaded here if they aren't yet, to be hashed. Members of
// other runtimes (speech, image) keep their images. model looks a catalog
// model up by id.
func (b *Builder) MountMembers(ctx context.Context, e *ensemble.Ensemble, model func(id string) (catalog.Model, bool), log LogFunc) error {
	if e.Weights != ensemble.WeightsMounted {
		return nil
	}
	for i := range e.Members {
		m := &e.Members[i]
		if m.Weights != nil {
			continue // named already
		}
		cm, ok := model(m.Model)
		if !ok || modelFamily(cm) != "llama" {
			log(fmt.Sprintf("The %s member (%s) keeps its image: only llama.cpp models run from the model-less runtime.", m.Tool, m.Image))
			continue
		}
		compute := m.Compute
		if compute == "" {
			compute = e.Compute
		}
		ref, err := b.RuntimeRef(RuntimeOptions{Compute: compute, Platform: e.Platform})
		if err != nil {
			return fmt.Errorf("the %s member: %w", m.Tool, err)
		}
		w, err := b.weightsFile(ctx, cm.File, cm.URL, func() (string, error) { return b.EnsureModel(ctx, cm, log) }, log)
		if err != nil {
			return fmt.Errorf("the %s member's weights: %w", m.Tool, err)
		}
		m.Weights, m.MMProj, m.Image, m.Compute = w, nil, ref, compute
		if cm.MMProjFile != "" {
			if m.MMProj, err = b.weightsFile(ctx, cm.MMProjFile, cm.MMProjURL, func() (string, error) { return b.EnsureMMProj(ctx, cm, log) }, log); err != nil {
				return fmt.Errorf("the %s member's projector: %w", m.Tool, err)
			}
		}
		log(fmt.Sprintf("The %s member runs %s with %s mounted.", m.Tool, ref, w.File))
	}
	return nil
}

// weightsFile describes one weights file for the manifest, fetching it with
// ensure when it isn't here yet.
func (b *Builder) weightsFile(ctx context.Context, file, url string, ensure func() (string, error), log LogFunc) (*ensemble.WeightsFile, error) {
	p, err := ensure()
	if err != nil {
		return nil, err
	}
	sum, size, err := b.sha256Of(ctx, p, log)
	if err != nil {
		return nil, err
	}
	w := &ensemble.WeightsFile{File: path.Base(filepath.ToSlash(file)), SHA256: sum, Size: size, URL: url}
	b.pin(ctx, w, log)
	return w, nil
}

// pin names the Hugging Face commit w was downloaded from, when the file
// there now has w's SHA-256; otherwise it leaves w unpinned and says why.
func (b *Builder) pin(ctx context.Context, w *ensemble.WeightsFile, log LogFunc) {
	if b.PinWeights == nil || w.URL == "" {
		return
	}
	p, ok, err := b.PinWeights(ctx, w.URL)
	switch {
	case !ok:
		return
	case err != nil:
		log(fmt.Sprintf("Couldn't pin %s on Hugging Face (%v): the manifest names its URL without a revision.", w.File, err))
	case p.SHA256 != w.SHA256:
		log(fmt.Sprintf("%s on Hugging Face has changed since it was downloaded (now %.12s…, here %.12s…): the manifest names it without a revision. Download it again to pin it.", w.File, p.SHA256, w.SHA256))
	default:
		w.Repo, w.Revision, w.URL = p.Repo, p.Revision, p.URL
	}
}

var shaCacheMu sync.Mutex

// sha256Of hashes a file, remembering the result by path, size and time in
// config/weights-sha256.json so a rebuild doesn't read gigabytes again.
func (b *Builder) sha256Of(ctx context.Context, p string, log LogFunc) (string, int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", 0, err
	}
	type entry struct {
		Size   int64  `json:"size"`
		Mod    int64  `json:"mod"`
		SHA256 string `json:"sha256"`
	}
	cachePath := filepath.Join(b.ConfigDir, "weights-sha256.json")
	shaCacheMu.Lock()
	defer shaCacheMu.Unlock()
	cache := map[string]entry{}
	if data, err := os.ReadFile(cachePath); err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	if c, ok := cache[p]; ok && c.Size == fi.Size() && c.Mod == fi.ModTime().UnixNano() && c.SHA256 != "" {
		return c.SHA256, fi.Size(), nil
	}
	log(fmt.Sprintf("Hashing %s (%.2f GB) ...", filepath.Base(p), float64(fi.Size())/1e9))
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, ctxReader{ctx, f}); err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	cache[p] = entry{Size: fi.Size(), Mod: fi.ModTime().UnixNano(), SHA256: sum}
	if data, err := json.MarshalIndent(cache, "", "  "); err == nil && b.ConfigDir != "" {
		_ = os.WriteFile(cachePath, data, 0o644)
	}
	return sum, fi.Size(), nil
}
