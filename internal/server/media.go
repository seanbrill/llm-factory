package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mediaExt maps a media MIME to a file extension for saved generated clips.
func mediaExt(mime string) string {
	switch {
	case strings.Contains(mime, "webm"):
		return "webm"
	case strings.Contains(mime, "mp4"):
		return "mp4"
	case strings.Contains(mime, "gif"):
		return "gif"
	case strings.Contains(mime, "webp"):
		return "webp"
	case strings.Contains(mime, "png"):
		return "png"
	case strings.Contains(mime, "jpeg"), strings.Contains(mime, "jpg"):
		return "jpg"
	case strings.Contains(mime, "wav"):
		return "wav"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return "mp3"
	default:
		return "bin"
	}
}

// saveDataURL decodes a "data:<mime>;base64,<data>" URL, writes the bytes to the
// media dir under a content-addressed name, and returns a stable
// "/api/media/<file>" URL. The UI persists that tiny URL (not the multi-MB data
// URL, which blows localStorage), so generated clips/images survive a reload and
// the loss of the container that made them. On any error the original string is
// returned so generation still works (just not persisted).
func (s *Server) saveDataURL(dataURL string) (string, error) {
	if !strings.HasPrefix(dataURL, "data:") {
		return dataURL, nil // already a URL/path
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return "", fmt.Errorf("malformed data URL")
	}
	meta := dataURL[len("data:"):comma] // e.g. "video/webm;base64"
	if !strings.Contains(meta, "base64") {
		return "", fmt.Errorf("non-base64 data URL")
	}
	mime := strings.SplitN(meta, ";", 2)[0]
	raw, err := base64.StdEncoding.DecodeString(dataURL[comma+1:])
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.mediaDir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	name := hex.EncodeToString(sum[:16]) + "." + mediaExt(mime)
	path := filepath.Join(s.mediaDir, name)
	if _, statErr := os.Stat(path); statErr != nil { // content-addressed: write once
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return "", err
		}
	}
	return "/api/media/" + name, nil
}
