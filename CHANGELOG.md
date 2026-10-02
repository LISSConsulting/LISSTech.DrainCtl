# Changelog

Release-facing notes for DrainCtl. Engineering lessons live in `CHRONICLE.md`.

## 26.10.40

- Additive export endpoints `GET /api/v1/metrics/_fleet/export` and `GET /api/v1/metrics/{host}/export` return CSV or `.xlsx` for the supported charts (Overview Load, Health Indicators, Sessions, RemoteFX when present, Per-host Load, and the per-host detail charts). CSV is the eight-column data sheet only; Excel adds a Context sheet for self-description. Filenames follow `drainctl-<fleet|host>[-<host_name>]-<graph-slug>.<ext>` and a documented 50,000-row cap returns an explicit `413 payload_too_large` rather than a silent truncation.
- New direct dependency `github.com/xuri/excelize/v2` (BSD-3-Clause, pure Go) for XLSX generation. Workbook text cells use `SetCellStr` so Excel never interprets operator-derived strings as formulas; only the `value` column is numeric. The `generator` Context field is the same git-derived CalVer the build pipeline embeds, surfaced through an ldflags-injected `packageVersion`.
