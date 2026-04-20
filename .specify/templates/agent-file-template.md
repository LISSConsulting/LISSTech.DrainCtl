# [PROJECT NAME] Development Guidelines

Auto-generated from all feature plans. Last updated: [DATE]

## Active Technologies

[EXTRACTED FROM ALL PLAN.MD FILES]

## Project Structure

```text
[ACTUAL STRUCTURE FROM PLANS]
```

## Commands

[ONLY COMMANDS FOR ACTIVE TECHNOLOGIES]

## Code Style

[LANGUAGE-SPECIFIC, ONLY FOR LANGUAGES IN USE]

## Constitution Highlights

- Windows-first delivery: new Go files use `//go:build windows`
- Stable operator surfaces: keep CLI, service, dashboard, DLL, and PowerShell behavior coherent
- Tests and zero-noise verification: `go test ./...`, `just lint`, and pre-commit checks must pass cleanly
- Config and release discipline: config stays in JSON; release version stays git-derived
- Operational observability: new behavior must be diagnosable through logs, telemetry, audit, or UI

## Recent Changes

[LAST 3 FEATURES AND WHAT THEY ADDED]

<!-- MANUAL ADDITIONS START -->
<!-- MANUAL ADDITIONS END -->
