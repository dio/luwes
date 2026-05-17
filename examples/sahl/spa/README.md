# spa

Serve a Vite + React SPA directly from an Envoy dynamic module `.so` -- no file
system access, no separate web server. Demonstrates `//go:embed`, SPA fallback
routing, asset caching, and `w.SetResponseHeader` + `w.SendBytes`.

## Routes

| URL | Filter | Description |
|-----|--------|-------------|
| `/` | `spa` | Home page (index.html) |
| `/about`, `/dashboard` | `spa` | Client-side routes (index.html fallback) |
| `/assets/*` | `spa` | Fingerprinted JS/CSS: `immutable` cache headers |
| `/api/hello` | `api-backend` | JSON: `{"message":"hello from inside the .so"}` |
| `/api/time` | `api-backend` | JSON: current UTC time |
| `/*` (unknown) | `spa` | SPA fallback: React Router renders a 404 component |

Refreshing on `/about` works because `spa` returns `index.html` for any path
that does not match a static asset. React Router renders the correct component
client-side.

## Two filters, one .so

| Filter | Description |
|--------|-------------|
| `spa` | Serves embedded `ui/dist` assets; falls back to `index.html` |
| `api-backend` | Handles `/api/*` from Go; passes other paths to `spa` |

Both filters share the same embedded filesystem (`//go:embed ui/dist`).

## Build

```sh
make -C examples/sahl/spa build        # local .so (host arch)
make -C examples/sahl/spa build-linux  # cross-compile amd64 + arm64 via zig
```

Or from inside the spa directory:

```sh
make build
```

## Docker

```sh
# Cross-compile + build multi-arch image (loads to local Docker daemon)
make -C examples/sahl/spa docker

# Push to a registry
make -C examples/sahl/spa docker-push IMAGE_TAG=ghcr.io/you/spa:latest

# Run and smoke test
make -C examples/sahl/spa smoke
```

The Dockerfile uses `envoyproxy/envoy:distroless-v1.38.0` — no shell,
no package manager. The `.so` is copied to `/etc/envoy/libspa.so`.

## Run

```sh
make run EXAMPLE=sahl/spa
# or:
GODEBUG=cgocheck=0 \
ENVOY_DYNAMIC_MODULES_SEARCH_PATH=$(pwd)/dist \
.bin/envoy -c examples/sahl/spa/envoy.yaml --log-level warning
```

Then open http://localhost:10000.

## Frontend development

The `ui/` directory is a standard Vite + React + TypeScript project. The
built output in `ui/dist/` is embedded into the `.so` at Go compile time.

```sh
cd examples/sahl/spa/ui
npm install
npm run dev      # Vite dev server on :5173, proxy /api → Envoy on :10000
npm run build    # rebuild ui/dist/, then rebuild the .so
```

## What this demonstrates

- `//go:embed ui/dist` -- bundle a complete Vite build into the `.so` at compile time
- SPA fallback routing -- `index.html` for any unmatched path so React Router works on refresh
- `w.SetResponseHeader` + `w.SendBytes` -- serve binary assets (JS bundles, CSS) from a filter
- `w.SetResponseHeader("cache-control", "public, max-age=31536000, immutable")` for fingerprinted assets
- Two filters in one `.so` sharing the same embedded filesystem
- `sahl.Chain` with logging middleware on the `api-backend` filter
- Pass-through: `api-backend` calls no methods on `w` for non-`/api/` paths

## Cache strategy

| Path | Cache-Control | Why |
|------|---------------|-----|
| `index.html` | `no-cache` | Entry point must always reflect the latest deploy |
| `/assets/*` | `public, max-age=31536000, immutable` | Vite fingerprints filenames: safe to cache forever |

## Filter structure

```
examples/sahl/spa/
  spa.go           SPAHandler, APIHandler, apiLogMiddleware
  spa_test.go      11 unit tests: index.html, assets, /api/*, SPA fallback
  ui/              Vite + React + TypeScript source
    dist/          Pre-built output embedded into the .so (committed)
    src/           React components
  cmd/main.go      Wiring: register spa + api-backend, sahl.Factories()
  envoy.yaml       Envoy config: api-backend → spa → router → direct_response
  README.md        This file
```

## Comparison with jisr spa

| Aspect | jisr | sahl |
|--------|------|------|
| Handler signature | `func(ctx context.Context, w ResponseWriter, r *Request)` | `func(w *Writer, r *Request)` |
| Path access | `r.Header.Get(":path")` | `r.Path` (pre-copied) |
| Response headers | `w.SetResponseHeader(k, v)` then `w.SendBytes` | same API |
| Middleware | `jisr.Chain` | `sahl.Chain` |
| Allocs (spa handler) | similar | similar: no body read, no pool overhead |
