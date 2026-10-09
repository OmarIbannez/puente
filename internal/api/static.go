package api

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed placeholder/index.html
var placeholder embed.FS

// placeholderFS is served when no SPA build is available: a single
// index.html explaining how to build the web app.
func placeholderFS() fs.FS {
	sub, err := fs.Sub(placeholder, "placeholder")
	if err != nil { // unreachable: the directory is embedded above
		panic(err)
	}
	return sub
}

// PlaceholderFS returns the built-in placeholder page, for callers that
// fall back to it explicitly.
func PlaceholderFS() fs.FS { return placeholderFS() }

// Cache-Control values.
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNone      = "no-cache"
	cacheShort     = "public, max-age=3600"
)

// hashedAsset matches bundler output under assets/ with a content hash in
// the name, such as assets/index-B3f9a1-c.js (Vite's default).
var hashedAsset = regexp.MustCompile(`^assets/.+[.-][A-Za-z0-9_-]{8,}\.[a-z0-9]+$`)

// staticHandler serves the SPA: existing files as-is, with long cache
// headers for hashed assets, and index.html (no-cache) for every other
// extensionless GET path so client-side routes work on reload.
type staticHandler struct {
	fsys fs.FS
}

func newStaticHandler(fsys fs.FS) *staticHandler { return &staticHandler{fsys: fsys} }

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if h.serveFile(w, r, name) {
		return
	}
	if path.Ext(name) != "" { // a missing asset, not a client route
		http.NotFound(w, r)
		return
	}
	if !h.serveFile(w, r, "index.html") {
		http.NotFound(w, r)
	}
}

// serveFile writes the regular file name and reports whether it existed.
func (h *staticHandler) serveFile(w http.ResponseWriter, r *http.Request, name string) bool {
	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return false
	}
	content, ok := f.(io.ReadSeeker)
	if !ok {
		b, err := io.ReadAll(f)
		if err != nil {
			return false
		}
		content = bytes.NewReader(b)
	}
	switch {
	case name == "index.html":
		w.Header().Set("Cache-Control", cacheNone)
	case hashedAsset.MatchString(name):
		w.Header().Set("Cache-Control", cacheImmutable)
	default:
		w.Header().Set("Cache-Control", cacheShort)
	}
	modTime := info.ModTime()
	if name == "index.html" {
		modTime = time.Time{} // always revalidate by content, never by date
	}
	http.ServeContent(w, r, path.Base(name), modTime, content)
	return true
}
