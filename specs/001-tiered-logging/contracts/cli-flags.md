# Contract: CLI Flags

**Scope**: `drainctl` root command persistent flags related to logging.

## Changed Flags

### `--log-level` (NEW)

```
Flag:     --log-level <level>
Type:     string
Default:  "info"
Scope:    Persistent (applies to all subcommands)
Values:   debug, info, warn, error (case-insensitive)
```

**Behavior**:
- Controls which log messages appear on stderr during CLI execution
- Messages at or above the specified level are emitted; below are suppressed
- Invalid value → exit code 1 with message: `invalid log level "<value>"; valid levels: debug, info, warn, error`

**Examples**:
```
drainctl check --log-level debug     # All messages visible
drainctl check --log-level warn      # Only WARN and ERROR
drainctl check --log-level error     # Only ERROR
drainctl check                       # Default: INFO and above
```

### `--quiet` (REMOVED)

```
Flag:     --quiet
Status:   REMOVED (breaking change, no shim)
```

**Migration**: Use `--log-level error` for equivalent behavior.

## Unchanged Flags

- `--db <path>` — Audit trail path (unchanged)
- `--format <fmt>` — Output format: plain, table, csv, json (unchanged)

## Output Routing

| Stream | Content | Affected by `--log-level` |
|--------|---------|--------------------------|
| stderr | Log messages (`[DBG]`, `[INF]`, `[WRN]`, `[ERR]`) | Yes |
| stdout | Structured data (`--format json/csv/table`) | No |
| stdout | Result line (`--- <status>`) | No |

## Result Line Contract

The `---` separator line replaces the former `[OK]` log level:

```
# Before (old behavior):
2026-04-09T14:30:00Z [OK ] msg="healthy" host=RDSH01

# After (new behavior):
--- healthy host=RDSH01
```

**Rules**:
- Always emitted on stdout regardless of `--log-level`
- Suppressed when `--format` is `json`, `csv`, or `table` (structured output replaces it)
- No timestamp, no level tag
- Format: `--- <message> [key=value ...]\n`
