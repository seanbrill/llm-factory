package server

import "mime"

// Go's built-in MIME table has no web fonts, and the factory image has no
// /etc/mime.types to fill the gap: give the UI's bundled fonts their real type
// so the file server doesn't fall back to sniffing.
func init() {
	_ = mime.AddExtensionType(".woff2", "font/woff2")
	_ = mime.AddExtensionType(".woff", "font/woff")
}
