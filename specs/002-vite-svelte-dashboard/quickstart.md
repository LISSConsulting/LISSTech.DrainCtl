# Quickstart: Vite + Svelte Dashboard Migration

**Branch**: `002-vite-svelte-dashboard` | **Date**: 2026-04-09

## Prerequisites

- Node.js 20+ (for Vite + Svelte build)
- Go 1.26+ (existing requirement)
- MinGW, WiX 5, .NET SDK 8+ (existing requirements)

## Development Setup

### 1. Install frontend dependencies

```bash
cd frontend
npm install
```

### 2. Development workflow (two terminals)

```bash
# Terminal 1: Go backend (dev mode, no auth)
just dev

# Terminal 2: Vite dev server with HMR
cd frontend
npm run dev
```

The Vite dev server runs on `http://localhost:5173` and proxies `/api/*` requests to the Go backend.

### 3. Production build

```bash
# Build frontend + copy to embed directory + build Go binary
just all
```

Or step by step:

```bash
cd frontend && npm run build    # → frontend/dist/
just frontend-copy              # → internal/dashboard/dist/
just cli                        # → bin/drainctl.exe (with embedded dashboard)
```

## Key Files

| File | Purpose |
|---|---|
| `frontend/src/App.svelte` | Root component |
| `frontend/src/app.css` | Tailwind v4 theme + global styles |
| `frontend/src/lib/state.svelte.js` | Global reactive state ($state runes) |
| `frontend/src/lib/api.js` | Backend API client |
| `frontend/src/lib/theme.js` | Theme toggle (localStorage) |
| `frontend/vite.config.js` | Vite + Svelte plugin config |
| `internal/dashboard/server.go` | Go HTTP server (embed + serve) |

## Architecture Notes

- **Single binary**: Vite build output is embedded into the Go binary via `//go:embed all:dist`
- **No SSR**: The dashboard is a client-side SPA served by the Go backend
- **API unchanged**: All routes, auth, and response shapes are identical to pre-migration
- **CSP tightened**: `'unsafe-inline'` replaced with `'self'` + nonce for theme snippet
- **Theme**: localStorage-based `data-theme` attribute on `<html>`, same as before
