// Package spa demonstrates serving a Vite + React SPA directly from an Envoy
// dynamic module — no file system access, no separate web server.
//
// Two filters ship in the same .so:
//
//	spa         — serves embedded ui/dist assets via w.SendBytes; falls back to
//	              index.html for unmatched paths (SPA client-side routing support)
//	api-backend — handles /api/* requests directly from Go, no upstream needed
//
// # How assets are embedded
//
// The ui/dist directory is embedded at compile time using //go:embed. The Vite
// build output (index.html + fingerprinted assets/) lives there. For development,
// run the Vite dev server separately (npm run dev in ui/); for production, run
// `npm run build` in ui/ then rebuild the .so.
//
// # Request routing in Envoy
//
//	GET /              → spa filter → index.html (200, text/html)
//	GET /assets/*.js   → spa filter → fingerprinted asset (200, immutable cache)
//	GET /api/*         → api-backend filter → JSON (200, application/json)
//	GET /unknown-page  → spa filter → index.html (SPA client-side routing fallback)
//
// All responses are generated inside the filter — no upstream cluster is contacted.
// The Envoy router is still required at the end of the chain, but the route can
// point at a blackhole cluster since it is never reached.
package spa

import (
	"embed"
	"encoding/json"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dio/luwes/sahl"
)

// UIFS holds the compiled Vite output, exported so tests can discover
// fingerprinted asset filenames at runtime.
//
//go:embed ui/dist
var UIFS embed.FS

// indexHTML is the SPA shell — served for every path that is not a known asset.
//
//go:embed ui/dist/index.html
var indexHTML []byte

func init() {
	sahl.Register("spa", SPAHandler)
	sahl.Register("api-backend", sahl.Chain(APIHandler, apiLogMiddleware))
}

// SPAHandler serves embedded static assets and falls back to index.html for
// all other paths, enabling React Router's client-side routing.
// Exported for unit testing.
func SPAHandler(w *sahl.Writer, r *sahl.Request) {
	path := r.Path

	// Strip query string.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}

	// Try to serve the exact asset from the embedded FS.
	fsPath := "ui/dist" + path
	data, err := fs.ReadFile(UIFS, fsPath)
	if err == nil {
		ct := mime.TypeByExtension(filepath.Ext(path))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.SetResponseHeader("content-type", ct)
		w.SetResponseHeader("cache-control", cacheControl(path))
		w.SendBytes(http.StatusOK, data)
		return
	}

	// Not a known asset — serve index.html for SPA client-side routing.
	w.SetResponseHeader("content-type", "text/html; charset=utf-8")
	w.SetResponseHeader("cache-control", "no-cache")
	w.SendBytes(http.StatusOK, indexHTML)
}

// cacheControl returns an appropriate Cache-Control value.
// Vite fingerprints /assets/* files — safe to cache indefinitely.
func cacheControl(path string) string {
	if strings.HasPrefix(path, "/assets/") {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

// APIHandler handles /api/* requests and responds directly from the .so.
// No upstream cluster is contacted — this IS the backend the SPA calls.
// Exported for unit testing.
func APIHandler(w *sahl.Writer, r *sahl.Request) {
	path := r.Path

	switch {
	case path == "/api/hello" || strings.HasPrefix(path, "/api/hello?"):
		serveHello(w, r)

	case path == "/api/time" || strings.HasPrefix(path, "/api/time?"):
		serveTime(w, r)

	case strings.HasPrefix(path, "/api/"):
		jsonResponse(w, http.StatusNotFound, map[string]string{
			"error": "not found",
			"path":  path,
		})
	}
	// Non-/api/ path: return without responding, let the next filter (spa) handle it.
}

func serveHello(w *sahl.Writer, r *sahl.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"message": "hello from inside the .so",
		"filter":  r.FilterName,
		"path":    r.Path,
	})
}

func serveTime(w *sahl.Writer, _ *sahl.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

func jsonResponse(w *sahl.Writer, status int, v any) {
	b, _ := json.Marshal(v)
	w.SetResponseHeader("content-type", "application/json")
	w.SendBytes(status, b)
}

// apiLogMiddleware logs each /api request.
func apiLogMiddleware(next sahl.HandlerFunc) sahl.HandlerFunc {
	return func(w *sahl.Writer, r *sahl.Request) {
		r.Log(sahl.LogInfo, "[api-backend] %s %s", r.Method, r.Path)
		next(w, r)
	}
}
