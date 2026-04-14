# Implementation Plan: Installer Dialog Redesign

**Branch**: `003-installer-config` | **Date**: 2026-04-10 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `specs/003-installer-dialog-redesign/spec.md`

## Summary

Redesign the MSI installer dialog flow by splitting the overcrowded InstallModeDlg into three focused dialogs: a simplified mode selector, a new MonitoringDlg (grace period, poll interval, session warning threshold, performance monitoring), and a new NotificationsDlg (webhook and ntfy URLs). Two new MSI properties (POLL_INTERVAL, SESSION_THRESHOLD) are added to ConfigureNotifyAction to pass the new settings to the CLI configure command.

## Technical Context

**Language/Version**: WiX Toolset 5.0.2 (XML dialect), Go 1.26+ (CLI — no changes needed)
**Primary Dependencies**: WixToolset.UI.wixext, WixToolset.Util.wixext (v5.0.2)
**Storage**: N/A (MSI properties → CLI flags → config.json)
**Testing**: Manual MSI install/uninstall testing, `just all` build verification
**Target Platform**: Windows Server (x64) — MSI installer
**Project Type**: MSI installer package (WiX v5)
**Performance Goals**: N/A (installer UI)
**Constraints**: 255-char ICE03 limit per CustomAction.Target; ConfigureAction at 212 chars (no room), ConfigureNotifyAction at 139 chars → 219 chars after adding new flags (36 chars headroom)
**Scale/Scope**: 6 files modified/created, ~200 lines of WiX XML

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

Constitution is an unfilled template — no concrete gates defined. No violations to check.

**Post-design re-check**: No gates to enforce. Proceed.

## Project Structure

### Documentation (this feature)

```text
specs/003-installer-dialog-redesign/
├── plan.md              # This file
├── research.md          # Phase 0: 255-char limit, navigation, defaults
├── data-model.md        # Phase 1: MSI properties, dialog flow, localization
├── quickstart.md        # Phase 1: step-by-step implementation guide
├── contracts/
│   └── msi-properties.md  # Phase 1: MSI property + custom action contracts
├── checklists/
│   └── requirements.md  # Spec quality checklist
└── tasks.md             # Phase 2 output (via /speckit.tasks)
```

### Source Code (repository root)

```text
installer/
├── LISSTech.DrainCtl.wxs          # MSI properties + custom actions (modify)
├── LISSTech.DrainCtl.wixproj      # Build file (no changes needed)
├── LISSTech.DrainCtl.wxl          # Localization strings (modify)
└── dialogs/
    ├── DrainCtlUI.wxs             # Dialog navigation flow (modify)
    ├── InstallModeDlg.wxs         # Simplified mode selector (modify)
    ├── ModeConfigDlg.wxs          # Dashboard/Registration config (no changes)
    ├── MonitoringDlg.wxs          # NEW: monitoring settings dialog
    └── NotificationsDlg.wxs       # NEW: notification URLs dialog
```

**Structure Decision**: All changes are within the existing `installer/` directory. Two new `.wxs` dialog files are created; four existing files are modified. No Go code changes required — the CLI flags already exist.

## Implementation Phases

### Phase 1: MSI Properties + Custom Action Update

**File**: `installer/LISSTech.DrainCtl.wxs`

1. Add `POLL_INTERVAL` and `SESSION_THRESHOLD` property declarations with defaults (300, 80) alongside existing properties (line ~32).
2. Append `--poll-interval=[POLL_INTERVAL] --session-warning-threshold=[SESSION_THRESHOLD]` to ConfigureNotifyAction's SetProperty value (line ~64-65). Final length: 219 chars.

**Verification**: `just all` builds without ICE03 warnings.

### Phase 2: Create MonitoringDlg

**File**: `installer/dialogs/MonitoringDlg.wxs` (new)

Dialog structure (Width="370", Height="300"):
- Standard banner/bottom-line (matching existing dialogs)
- Grace period: label + Edit (60px) + hint "minutes (1–1440)" — Property: GRACE_PERIOD
- Poll interval: label + Edit (60px) + hint "seconds (minimum 10)" — Property: POLL_INTERVAL
- Session warning threshold: label + Edit (60px) + hint "percent (0 = disabled)" — Property: SESSION_THRESHOLD
- Enable performance monitoring: CheckBox — Property: ENABLE_PERF, CheckBoxValue="1"
- Collect RemoteFX metrics: CheckBox (indented X=36) — Property: ENABLE_RFX, CheckBoxValue="1", EnableCondition/DisableCondition on ENABLE_PERF
- Back / Next / Cancel buttons

### Phase 3: Create NotificationsDlg

**File**: `installer/dialogs/NotificationsDlg.wxs` (new)

Dialog structure (Width="370", Height="270"):
- Standard banner/bottom-line
- Webhook URL: label + Edit (330px) — Property: WEBHOOK_URL
- ntfy URL: label + Edit (330px) — Property: NTFY_URL
- Back / Next / Cancel buttons

### Phase 4: Simplify InstallModeDlg

**File**: `installer/dialogs/InstallModeDlg.wxs`

1. Remove controls: GraceLabel, GraceEdit, GraceHint, PerfCheck, RfxCheck, WebhookLabel, WebhookEdit, NtfyLabel, NtfyEdit (9 controls removed).
2. Keep: BannerBitmap, BannerLine, BottomLine, Title, Description, ModeLabel, ModeCombo, Back, Next, Cancel.
3. Reduce dialog Height from 300 to a value that fits the remaining controls (~200).
4. Adjust BottomLine and button Y positions to match new height.

### Phase 5: Rewire Dialog Navigation

**File**: `installer/dialogs/DrainCtlUI.wxs`

Update `<Publish>` elements:

**InstallModeDlg Next** (replace current 3 rules):
- `INSTALL_MODE = "dashboard"` → DashboardConfigDlg (unchanged)
- `INSTALL_MODE = "registration"` → RegistrationConfigDlg (unchanged)
- `INSTALL_MODE = "standalone"` → MonitoringDlg (was VerifyReadyDlg)

**DashboardConfigDlg Next**: → MonitoringDlg (was VerifyReadyDlg)
**RegistrationConfigDlg Next**: → MonitoringDlg (was VerifyReadyDlg)

**MonitoringDlg** (new):
- Back: conditional on INSTALL_MODE (standalone→InstallModeDlg, dashboard→DashboardConfigDlg, registration→RegistrationConfigDlg)
- Next: → NotificationsDlg

**NotificationsDlg** (new):
- Back: → MonitoringDlg
- Next: → VerifyReadyDlg

**VerifyReadyDlg Back** (replace current 3 first-install rules):
- All first-install modes → NotificationsDlg (single rule: `NOT Installed`)
- Maintenance rules unchanged

### Phase 6: Update Localization

**File**: `installer/LISSTech.DrainCtl.wxl`

1. Update InstallModeDlgTitle value: remove "and Notifications"
2. Remove 6 unused InstallModeDlg strings (grace, webhook, ntfy, perf, rfx)
3. Add 11 MonitoringDlg strings (title, description, grace, poll, threshold, perf, rfx + hints)
4. Add 4 NotificationsDlg strings (title, description, webhook, ntfy)

See [data-model.md](data-model.md) for complete string table.

## Complexity Tracking

No constitution violations. No complexity justifications needed.

## Key Risks

1. **Character budget**: ConfigureNotifyAction at 219/255 chars. If future properties need to be added, a third custom action may be necessary.
2. **Property expansion**: MSI property values expand at install time. Long URLs in WEBHOOK_URL/NTFY_URL could push the expanded command past shell limits — but this is a pre-existing concern, not introduced by this change.
3. **Silent install compatibility**: Existing `msiexec /quiet` commands that don't set POLL_INTERVAL or SESSION_THRESHOLD will get the defaults (300, 80), matching current behavior.
