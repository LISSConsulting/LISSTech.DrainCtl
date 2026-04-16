# Contract: `event_spike` Notification Payload

**Scope**: JSON body sent to webhook / ntfy / email targets that are subscribed to the `event_spike` trigger.

**Backward compatibility**: Additive to the existing `SendNotification` envelope. Downstream integrators that parse the existing shape continue to work; they can opt in to the new `spike` sub-object when they want.

---

## Envelope

Identical to the existing notification envelope (see `notify.go` → `SendNotification`). Fields marked NEW are specific to this trigger.

| Field | Type | Example | Notes |
|-------|------|---------|-------|
| `event` | string | `"event_spike"` | **NEW value** on the existing `event` field. |
| `host` | string | `"RDSH-04"` | Hostname of the server that detected the spike. |
| `timestamp` | RFC 3339 | `"2026-04-16T14:23:42-05:00"` | When the alert was fired (post-confirmation). |
| `subject` | string | `"⚠️ RDSH-04: Event spike on Microsoft-Windows-Winlogon/Operational (47 events, expected ~3.2)"` | Email subject line; reuses the existing subject-building path with an evtspike-specific template. |
| `status` | string | `"warning"` / `"alert"` | Echoed from the target's configured severity (Q4 D clarification). |
| `message` | string | `"Event count 47 in 10s bucket; expected ~3.2 at this time-of-day; tail probability 2.1e-7."` | Human-readable single sentence. |
| `version` | string | `"26.106.12"` | DrainCtl version for debugging. |
| `spike` | object | (see below) | **NEW** — spike-specific data. |

## The `spike` sub-object

| Field | Type | Example | Notes |
|-------|------|---------|-------|
| `channel` | string | `"Microsoft-Windows-Winlogon/Operational"` | Event log channel name. |
| `window_start` | RFC 3339 | `"2026-04-16T14:22:42-05:00"` | Start of the scoring window. |
| `window_end` | RFC 3339 | `"2026-04-16T14:22:52-05:00"` | End. Always `window_start + 10s`. |
| `observed` | int | `47` | Event count in this window. |
| `expected` | float | `3.2` | Posterior mean for this channel/slot. |
| `tail_probability` | float | `2.1e-7` | P(Y ≥ observed) under NegBin predictive. Always `0 < p < 1`. |
| `confirmation_count` | int | `3` | 2 or 3 — how many of the last three windows were anomalous. |
| `first_seen_at` | RFC 3339 | `"2026-04-16T14:22:32-05:00"` | When the spike first breached threshold (may be 10s or 20s before `window_start`). |

---

## Full example (webhook)

```json
{
  "event": "event_spike",
  "host": "RDSH-04",
  "timestamp": "2026-04-16T14:23:42-05:00",
  "subject": "⚠️ RDSH-04: Event spike on Microsoft-Windows-Winlogon/Operational",
  "status": "alert",
  "message": "Event count 47 in 10s bucket; expected ~3.2 at this time-of-day; tail probability 2.1e-7.",
  "version": "26.106.12",
  "spike": {
    "channel": "Microsoft-Windows-Winlogon/Operational",
    "window_start": "2026-04-16T14:22:42-05:00",
    "window_end": "2026-04-16T14:22:52-05:00",
    "observed": 47,
    "expected": 3.2,
    "tail_probability": 2.1e-7,
    "confirmation_count": 3,
    "first_seen_at": "2026-04-16T14:22:32-05:00"
  }
}
```

## HMAC signing

Webhook targets with `secret` configured receive the same `X-DrainCtl-Signature: sha256=<hex>` header as existing triggers — HMAC-SHA256 over the raw request body, hex-encoded. No change from existing behavior.

## Email rendering

The MJML template for `event_spike` reuses the card + emoji + preview-text styling landed in commits `bdf197e`, `38d6dae`, `96e28a4`, `0049cfa`, `bb8dcdd`. New template sections:
- Subject emoji: `⚠️` for `warning` status, `🚨` for `alert`, consistent with existing severity mapping.
- Preview text: `"<channel>: <observed> events, expected ~<expected>. Tail probability <p>."`
- Card body: two rows — `Observed vs Expected` and `Confirmation window`.

## ntfy rendering

ntfy payload uses the `Title` = `subject`, `Message` = `message`, `Priority` mapped from `status` (3 for warning, 4 for alert — consistent with existing mapping), `Tags` = `["evtspike", host, channel-basename]`.

---

## Contract tests

- `TestEventSpikePayload_Shape`: marshal a `SpikePayload` via `SendNotification`, assert top-level fields present and `spike` sub-object matches schema.
- `TestEventSpikePayload_HMAC`: sign body with `secret`, verify `X-DrainCtl-Signature` matches HMAC-SHA256.
- `TestEventSpikePayload_EmailTemplate`: render MJML → HTML, assert observed/expected shown and emoji present.
- `TestEventSpikePayload_NtfyPriority`: warning → priority 3, alert → priority 4.
