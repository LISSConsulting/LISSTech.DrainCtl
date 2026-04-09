# Contract: config.json Schema Changes

**Scope**: `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`

## New Fields

### `log_file_level`

```json
{
  "log_file_level": "debug"
}
```

| Property | Value |
|----------|-------|
| Type | string |
| Default | `"debug"` |
| Valid values | `"debug"`, `"info"`, `"warn"`, `"error"` (case-insensitive) |
| Context | Service mode file log minimum level |

### `log_event_level`

```json
{
  "log_event_level": "info"
}
```

| Property | Value |
|----------|-------|
| Type | string |
| Default | `"info"` |
| Valid values | `"debug"`, `"info"`, `"warn"`, `"error"` (case-insensitive) |
| Context | Service mode ETW Operational channel minimum level |

## Validation Behavior

- Missing field → use default (no warning)
- Invalid value → log warning to file sink, fall back to default
- Empty string → treated as missing (use default)

## Full Schema (with new fields highlighted)

```json
{
  "grace_period": 60,
  "retention_days": 90,
  "poll_interval": 300,
  "audit_path": "C:\\ProgramData\\LISS Technologies\\LISSTech DrainCtl\\audit.jsonl",
  "log_file_level": "debug",
  "log_event_level": "info",
  "notifications": [],
  "dashboard": {
    "enabled": false,
    "port": 49470,
    "group": "Domain Admins"
  },
  "session_warning_threshold": 80,
  "performance": {
    "enabled": false
  }
}
```

## Precedence

```
CLI --log-level flag  >  config.json value  >  hard-coded default
```

The CLI flag only affects the CLI invocation's stderr handler. It does not affect the service's file or ETW sinks. The config.json values control the service's sinks.
