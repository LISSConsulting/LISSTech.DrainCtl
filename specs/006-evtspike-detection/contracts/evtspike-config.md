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
    "added_channels": [],
    "security_channel_enabled": false
  }
}
```

## Field contract

See `data-model.md` §1 for the full table (defaults, clamps, live-reload eligibility).

## Security channel opt-in

The `Security` channel is **never** in the default watched list. Admins opt in by setting `security_channel_enabled: true` in the config block. At subsystem Start, if the flag is set, the service enables `SeSecurityPrivilege` on its own process token via `AdjustTokenPrivileges` and `"Security"` is added to the channel list by `ResolveChannels`.

The default service account (`LocalSystem`) has `SeSecurityPrivilege` present (Disabled state) in its kernel-assembled token, so enabling it at runtime succeeds. No MSI-time grant is required and no `LsaAddAccountRights` call is ever made.

Admins who have reconfigured DrainCtl to run as a **dedicated service account** must grant `SeSecurityPrivilege` to that account manually (via `secedit /configure` or group policy). If `AdjustTokenPrivileges` returns `ERROR_NOT_ALL_ASSIGNED`, the subsystem logs a warning and skips the Security subscription; other channels continue to operate.

An admin who manually puts `"Security"` into `added_channels` without setting `security_channel_enabled: true` will have the channel subscribed-to but the privilege will not be enabled — subscription will fail and be logged as skipped per FR-009. `security_channel_enabled: true` is the authoritative opt-in.

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
| `min_count`, `threshold`, `cooldown_minutes`, `slot_maturity_observations`, `persist_interval_seconds`, `half_life_buckets` | Hot-apply to running detectors (no baseline reset). |
| `prior_strength`, `mean_per_bucket_prior` | Hot-apply, but affects **new channels only**. Existing `GammaState.Alpha/Beta` are not rewritten — the prior's influence fades over time as observations accumulate, so late changes have diminishing effect on mature channels. Admins wanting to re-prior a mature detector should delete the baseline file and restart. |
| `baseline_path` | Stop subsystem; reload from new path; start. |
| `disabled_channels`, `added_channels`, `security_channel_enabled` | Stop subsystem; restart with new channel set. For `security_channel_enabled: false → true`, the Start path enables `SeSecurityPrivilege` on the token via `AdjustTokenPrivileges`. For `true → false`, the Stop path simply drops the subscription; no `LsaRemoveAccountRights` is called (LocalSystem's built-in privilege is not touched). |

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

- `TestEvtSpikeConfig_ZeroValueDisabled`: config without the block → `Enabled == false`, `SecurityChannelEnabled == false`, other fields at defaults.
- `TestClampEvtSpike`: table-driven — each clamped field hit its lower + upper bound; non-multiple `persist_interval_seconds` emits a warning.
- `TestEvtSpikeConfig_LiveReload_Sensitivity`: change `threshold` at runtime, verify running detectors see new value within one scoring window.
- `TestEvtSpikeConfig_LiveReload_ChannelList`: change `disabled_channels`, verify subsystem restarts.
- `TestEvtSpikeConfig_LiveReload_SecurityFlag`: change `security_channel_enabled: false → true`, verify subsystem restarts and `Security` is in the subscribed-channels list; change `true → false`, verify `Security` is dropped.
- `TestEvtSpikeConfig_SecurityNotInDefaults`: parse a default-install config, assert `security_channel_enabled == false` and `"Security"` not in `added_channels`.
- `TestEvtSpikeConfig_LiveReload_PriorScope`: change `prior_strength` at runtime; assert existing channels' `GammaState.Alpha/Beta` unchanged; assert newly-added channels observed after the change use the new prior.
