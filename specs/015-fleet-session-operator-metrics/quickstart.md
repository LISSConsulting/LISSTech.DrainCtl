# Quickstart: Verify Fleet Session Operator Metrics

## Prerequisites

- Windows development workstation with Go 1.26.2, Node/npm, `just`, and `prek`.
- Worktree checked out on `015-fleet-session-operator-metrics`.

## Focused verification

```powershell
# Histogram, ingestion, rollup, filtering, and retention
go test ./internal/sessiondata ./internal/telemetry ./internal/dashboard

# Frontend adapter and component behavior
npm --prefix frontend test -- --run
npm --prefix frontend run build
```

## Behavioral fixture

Create two accepted snapshots in one base interval:

- host A: 100 CPU observations at 5% total-host capacity
- host B: 2 CPU observations at 80% total-host capacity

Query:

```text
GET /api/v1/metrics/_fleet?from=<before>&to=<after>&resolution=raw
```

Verify:

- CPU AVG is approximately `6.47%`, not `42.5%`.
- CPU P95 follows the pooled 102-observation distribution within 0.5 point.
- coverage reports `2/2 hosts`.
- CPU-observed sessions is 102.
- selecting only host B returns AVG/P95 80% and `1/1 hosts`.

Then submit a newer fatal attempt for host B in the same minute. Verify host B no longer contributes values, coverage is partial/error, and the old successful values are not carried forward.

## Dashboard smoke

1. Build and launch the dashboard with a disposable data directory.
2. Open Overview → Sessions.
3. Verify Session CPU and Memory legends are `Fleet Session P95` and `Fleet Session AVG`.
4. Hover points and verify observed-session and host coverage fields.
5. Hover CPU-active Sessions and verify count, percentage, denominator, average, and max.
6. Switch 5M/1H/1D/3D/5D/30D windows and selected hosts; verify no stale response replaces the latest selection.
7. Verify pre-feature ranges show gaps, not relabeled host-summary history.

## Full gate

```powershell
go test ./...
just lint
prek run --all-files
```

Build the normal Windows release artifact through the documented repository command and inspect the embedded dashboard once more. All commands must complete without warnings or bypasses.
