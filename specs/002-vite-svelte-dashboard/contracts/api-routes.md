# API Routes Contract

**Branch**: `002-vite-svelte-dashboard` | **Date**: 2026-04-09

The frontend migration MUST NOT change any API routes, request/response shapes, authentication requirements, or HTTP methods. This document serves as the contract the Svelte frontend must consume.

## Routes

| Method | Route | Auth | Purpose |
|---|---|---|---|
| GET | `/` | Admin (SSPI + Group) | Serve dashboard SPA (index.html) |
| GET | `/favicon.ico` | None | Embedded favicon |
| GET | `/api/v1/health` | None | Health check: version + server counts |
| POST | `/api/v1/register` | Domain user (SSPI) | Agent registration |
| POST | `/api/v1/report` | Domain user (SSPI) | Agent status report |
| GET | `/api/v1/servers` | Admin (SSPI + Group) | List all servers |
| GET | `/api/v1/servers/{host}` | Admin (SSPI + Group) | Single server details |
| DELETE | `/api/v1/servers/{host}` | Admin (SSPI + Group) | Remove server |
| GET | `/api/v1/history/{host}` | Admin (SSPI + Group) | Server history (limit, changes_only params) |
| GET | `/api/v1/notify-config` | Domain user (SSPI) | Get notification config |
| PUT | `/api/v1/notify-config` | Admin (SSPI + Group) | Update notification config |
| POST | `/api/v1/notify-test` | Admin (SSPI + Group) | Send test notification |

## Static Asset Routes (NEW — for Vite build output)

| Method | Route | Auth | Purpose |
|---|---|---|---|
| GET | `/assets/*` | None | Hashed JS/CSS/font assets (Cache-Control: public, max-age=31536000, immutable) |

## Notes

- The `/` route changes from serving raw `dashboardHTML` bytes to serving `dist/index.html` from the embedded filesystem
- New `/assets/*` route serves Vite's hashed static assets with aggressive caching (filenames contain content hashes)
- All other routes remain unchanged
- Rate limiting (10 req/s sustained, burst 60) applies to all routes
- Security headers (CSP, X-Frame-Options, HSTS, X-Content-Type-Options) applied by `securityMiddleware`
