# Contract: `evtspike` Config Block

**Scope**: The new `evtspike` block inside `%ProgramData%\LISS Technologies\LISSTech DrainCtl\config.json`.

**Backward compatibility**: Additive. Older configs without the block take the zero-value (`Enabled: false`), so upgrading to a build that includes evtspike does not activate detection until the admin opts in.

---

## Schema

Nested object under the root `Config`:

```json
{
  "grace_period": 15,
  "retention_days": 30,
  "poll_interval": 30,
  "audit_path": "",
  "memory_limit_mb": 0,
  "log_file_level": "debug",
  "log_event_level": "info",

  "notifications": [...],
  "dashboard": {...},
  "session_warning_threshold": 0,
  "performance": {...},

  "evtspike": {
    "enabled": false,
    "min_count": 10,
    "threshold": 1e-4,
    "cooldown_minutes": 10,
    "slot_maturity_observations": 7,
    "persist_interval_seconds": 900,
    "half_life_buckets": 360,
    "prior_strength": 60,
    "mean_per_bucket_prior": 0.1,
    "baseline_path": "",
    "disabled_channels": [],
    "added_channels": []
  }
}
```

## Field contract

See `data-model.md` §1 for the full table (defaults, clamps, live-reload eligibility).

## Interaction with MSI Security opt-in

The `Security` channel is **never** listed in `added_channels` by a default install. When the admin opts in via the MSI feature (contract: `msi-security-opt-in.md`), the installer drops a marker file `%ProgramData%\...\evtspike\security_enabled`. The resolver (`data-model.md` §4) reads this marker independently of `config.json` and adds `"Security"` to the effective list.

An admin who manually edits `added_channels` to include `"Security"` but has NOT opted in via the installer will see a subscription failure at startup (insufficient privilege), which is logged as a skipped channel per FR-009. The service does not self-grant privileges.

## Validation / clamping

`ClampEvtSpike(&cfg.EvtSpike)` runs during `LoadConfig` (same call site as the existing `ClampRetention`). Out-of-range values are clamped to endpoints and a warning is logged:

```
evtspike: min_count clamped 15000 → 10000 (max)
evtspike: threshold clamped 0.5 → 0.1 (max)
```

## Live reload semantics

When the config file mtime changes (existing `RegNotifyChangeKeyValue` + file watcher pattern):

| Field changed | Action |
|---------------|--------|
| `enabled: false → true` | Start subsystem; load baseline. |
| `enabled: true → false` | Stop subsystem; flush baseline. |
| `min_count`, `threshold`, `cooldown_minutes`, `slot_maturity_observations`, `persist_interval_seconds`, `half_life_buckets`, `prior_strength`, `mean_per_bucket_prior` | Hot-apply to running detectors (no baseline reset). |
| `baseline_path` | Stop subsystem; reload from new path; start. |
| `disabled_channels`, `added_channels` | Stop subsystem; restart with new channel set. |

The "stop, reload, start" path is brief (≤1 s) and logged as an info-level "evtspike: restarting for config change".

## Examples

### Typical opt-in (conservative)

```json
"evtspike": {
  "enabled": true
}
```

Everything else defaults.

### Aggressive sensitivity, quieter cooldown

```json
"evtspike": {
  "enabled": true,
  "threshold": 1e-3,
  "cooldown_minutes": 30
}
```

### Site-specific channel tuning

```json
"evtspike": {
  "enabled": true,
  "disabled_channels": [
    "Microsoft-Windows-PrintService/Operational"
  ],
  "added_channels": [
    "Citrix/Operational",
    "Our-App/Operational"
  ]
}
```

### Relocated baseline file

```json
"evtspike": {
  "enabled": true,
  "baseline_path": "D:\\DrainCtl\\evtspike-baseline.json"
}
```

---

## Contract tests

- `TestEvtSpikeConfig_ZeroValueDisabled`: config without the block → `Enabled == false`, other fields at defaults.
- `TestClampEvtSpike`: table-driven — each field hit its lower + upper bound.
- `TestEvtSpikeConfig_LiveReload_Sensitivity`: change `threshold` at runtime, verify running detectors see new value within one scoring window.
- `TestEvtSpikeConfig_LiveReload_ChannelList`: change `disabled_channels`, verify subsystem restarts.
- `TestEvtSpikeConfig_SecurityNotInAddedByDefault`: parse a default-install config, assert `"Security"` not in `added_channels`.
