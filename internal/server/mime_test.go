package server

import (
	"mime"
	"testing"
)

func TestFontMIMETypes(t *testing.T) {
	for ext, want := range map[string]string{".woff2": "font/woff2", ".woff": "font/woff"} {
		if got := mime.TypeByExtension(ext); got != want {
			t.Errorf("%s: got %q, want %q", ext, got, want)
		}
	}
}
