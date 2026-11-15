package builder

// What the factory keeps on disk, for the Library's cleanup panel and the
// Overview's disk card: downloaded weights, exported image tars, and build
// contexts / transfer tars left behind by a build that didn't finish.

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UsageItem is one file or folder and its size.
type UsageItem struct {
	Name string  `json:"name"`
	GB   float64 `json:"gb"`
}

// UsageGroup is one kind of thing on disk.
type UsageGroup struct {
	Count int         `json:"count"`
	GB    float64     `json:"gb"`
	Items []UsageItem `json:"items"`
}

func (g *UsageGroup) add(name string, bytes int64) {
	gb := float64(bytes) / 1e9
	g.Count++
	g.GB += gb
	g.Items = append(g.Items, UsageItem{Name: name, GB: gb})
}

func (g *UsageGroup) sortBySize() {
	sort.Slice(g.Items, func(i, j int) bool { return g.Items[i].GB > g.Items[j].GB })
}

// PathSize is the total size of a file, or of every regular file under a
// directory (symlinks are not followed). Unreadable entries count as 0.
func PathSize(path string) int64 {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	if !fi.IsDir() {
		if fi.Mode().IsRegular() {
			return fi.Size()
		}
		return 0
	}
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// isLeftover matches what CleanStaleBuildDirs removes.
func isLeftover(name string) bool {
	return strings.HasPrefix(name, "ctx-") || strings.HasPrefix(name, "ens-") ||
		(strings.HasPrefix(name, "xfer-") && strings.HasSuffix(name, ".tar"))
}

// WeightsUsage lists the downloaded weights in models/ (top-level files and
// folders, skipping the build dir and dotfiles such as .gitkeep).
func (b *Builder) WeightsUsage() UsageGroup {
	g := UsageGroup{Items: []UsageItem{}}
	entries, _ := os.ReadDir(b.ModelsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		g.add(e.Name(), PathSize(filepath.Join(b.ModelsDir, e.Name())))
	}
	g.sortBySize()
	return g
}

// TarsUsage lists the exported image tarballs in images/.
func (b *Builder) TarsUsage() UsageGroup {
	g := UsageGroup{Items: []UsageItem{}}
	matches, _ := filepath.Glob(filepath.Join(b.ImagesDir, "*.tar"))
	for _, m := range matches {
		g.add(filepath.Base(m), PathSize(m))
	}
	g.sortBySize()
	return g
}

// LeftoversUsage lists build contexts and transfer tars in the build dir. While
// a build runs, its own context is in there too: callers must only call these
// leftovers when no build is running.
func (b *Builder) LeftoversUsage() UsageGroup {
	g := UsageGroup{Items: []UsageItem{}}
	entries, _ := os.ReadDir(b.BuildDir)
	for _, e := range entries {
		if isLeftover(e.Name()) {
			g.add(e.Name(), PathSize(filepath.Join(b.BuildDir, e.Name())))
		}
	}
	g.sortBySize()
	return g
}
