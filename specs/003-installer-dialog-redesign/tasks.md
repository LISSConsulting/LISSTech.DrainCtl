# Tasks: Installer Dialog Redesign

**Input**: Design documents from `specs/003-installer-dialog-redesign/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/msi-properties.md, quickstart.md

**Tests**: No automated tests requested. Verification is manual MSI install testing via `just all`.

**Organization**: Tasks grouped by user story. US1 and US2 share foundational work; US3 and US4 build on top.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

---

## Phase 1: Setup (MSI Properties + Custom Action)

**Purpose**: Add new MSI properties and update the custom action command line — foundational for all user stories.

- [x] T001 Add POLL_INTERVAL property (default "300") and SESSION_THRESHOLD property (default "80") to installer/LISSTech.DrainCtl.wxs alongside existing property declarations (~line 32)
- [x] T002 Append `--poll-interval=[POLL_INTERVAL] --session-warning-threshold=[SESSION_THRESHOLD]` to ConfigureNotifyAction SetProperty value in installer/LISSTech.DrainCtl.wxs (~line 64-65), bringing it to 219/255 chars

**Checkpoint**: `just all` builds without ICE03 warnings. New properties exist but are not yet exposed in any dialog.

---

## Phase 2: Foundational (Localization Strings)

**Purpose**: Add all localization strings needed by the new dialogs before creating them.

**⚠️ CRITICAL**: Dialog files reference `!(loc.XXX)` strings that must exist in the .wxl file, so localization must be ready first.

- [x] T003 Update InstallModeDlgTitle value from "Install Mode and Notifications" to "Install Mode" in installer/LISSTech.DrainCtl.wxl
- [x] T004 Remove 6 unused InstallModeDlg localization strings (GraceLabel, GraceHint, WebhookLabel, NtfyLabel, PerfLabel, RfxLabel) from installer/LISSTech.DrainCtl.wxl
- [x] T005 Add 11 MonitoringDlg localization strings (MonitoringDlg_Title, MonitoringDlgTitle, MonitoringDlgDescription, MonitoringDlgGraceLabel, MonitoringDlgGraceHint, MonitoringDlgPollLabel, MonitoringDlgPollHint, MonitoringDlgThresholdLabel, MonitoringDlgThresholdHint, MonitoringDlgPerfLabel, MonitoringDlgRfxLabel) to installer/LISSTech.DrainCtl.wxl per data-model.md string table
- [x] T006 Add 4 NotificationsDlg localization strings (NotificationsDlg_Title, NotificationsDlgTitle, NotificationsDlgDescription, NotificationsDlgWebhookLabel, NotificationsDlgNtfyLabel) to installer/LISSTech.DrainCtl.wxl per data-model.md string table

**Checkpoint**: Localization file complete. `just all` still builds (existing dialogs reference only unchanged strings).

---

## Phase 3: User Story 1 — Simplified Install Mode Selection (Priority: P1) 🎯 MVP

**Goal**: Strip InstallModeDlg down to only the mode selector combo box.

**Independent Test**: Run installer → reach Install Mode dialog → verify only mode selector is shown, no grace/perf/webhook controls.

### Implementation for User Story 1

- [x] T007 [US1] Remove 9 controls (GraceLabel, GraceEdit, GraceHint, PerfCheck, RfxCheck, WebhookLabel, WebhookEdit, NtfyLabel, NtfyEdit) from installer/dialogs/InstallModeDlg.wxs
- [x] T008 [US1] Reduce dialog Height from 300 to ~200 and adjust BottomLine Y, Back/Next/Cancel Y positions to match in installer/dialogs/InstallModeDlg.wxs
- [x] T009 [US1] Update InstallModeDlgDescription localization value to "Choose how this machine participates in the monitoring network." in installer/LISSTech.DrainCtl.wxl

**Checkpoint**: InstallModeDlg shows only mode selector. `just all` builds. Forward navigation still goes to old targets (will be rewired in US4).

---

## Phase 4: User Story 2 — Configure Monitoring Settings (Priority: P1)

**Goal**: Create MonitoringDlg with grace period, poll interval, session threshold, and performance monitoring controls.

**Independent Test**: Run installer → advance past mode selection → verify MonitoringDlg appears with 5 controls, correct defaults, and RemoteFX disables when perf unchecked.

### Implementation for User Story 2

- [x] T010 [US2] Create installer/dialogs/MonitoringDlg.wxs with dialog structure (Width="370", Height="300"): banner, bottom-line, header using !(loc.MonitoringDlgTitle) and !(loc.MonitoringDlgDescription)
- [x] T011 [US2] Add grace period controls to MonitoringDlg: label (!(loc.MonitoringDlgGraceLabel)), Edit (Property="GRACE_PERIOD", Width="60"), hint text (!(loc.MonitoringDlgGraceHint)) in installer/dialogs/MonitoringDlg.wxs
- [x] T012 [US2] Add poll interval controls to MonitoringDlg: label (!(loc.MonitoringDlgPollLabel)), Edit (Property="POLL_INTERVAL", Width="60"), hint text (!(loc.MonitoringDlgPollHint)) in installer/dialogs/MonitoringDlg.wxs
- [x] T013 [US2] Add session threshold controls to MonitoringDlg: label (!(loc.MonitoringDlgThresholdLabel)), Edit (Property="SESSION_THRESHOLD", Width="60"), hint text (!(loc.MonitoringDlgThresholdHint)) in installer/dialogs/MonitoringDlg.wxs
- [x] T014 [US2] Add performance monitoring CheckBox (Property="ENABLE_PERF", CheckBoxValue="1", !(loc.MonitoringDlgPerfLabel)) and indented RemoteFX CheckBox (Property="ENABLE_RFX", X="36", CheckBoxValue="1", EnableCondition/DisableCondition on ENABLE_PERF, !(loc.MonitoringDlgRfxLabel)) in installer/dialogs/MonitoringDlg.wxs
- [x] T015 [US2] Add Back/Next/Cancel navigation buttons to MonitoringDlg matching existing dialog button layout in installer/dialogs/MonitoringDlg.wxs

**Checkpoint**: MonitoringDlg file complete. `just all` builds (dialog exists but isn't wired into flow yet).

---

## Phase 5: User Story 3 — Configure Notification Endpoints (Priority: P2)

**Goal**: Create NotificationsDlg with optional webhook URL and ntfy URL fields.

**Independent Test**: Run installer → advance to Notifications dialog → verify two optional URL fields shown → complete install with both blank → verify no errors.

### Implementation for User Story 3

- [x] T016 [US3] Create installer/dialogs/NotificationsDlg.wxs with dialog structure (Width="370", Height="270"): banner, bottom-line, header using !(loc.NotificationsDlgTitle) and !(loc.NotificationsDlgDescription)
- [x] T017 [US3] Add webhook URL controls: label (!(loc.NotificationsDlgWebhookLabel)), Edit (Property="WEBHOOK_URL", Width="330") in installer/dialogs/NotificationsDlg.wxs
- [x] T018 [US3] Add ntfy URL controls: label (!(loc.NotificationsDlgNtfyLabel)), Edit (Property="NTFY_URL", Width="330") in installer/dialogs/NotificationsDlg.wxs
- [x] T019 [US3] Add Back/Next/Cancel navigation buttons to NotificationsDlg matching existing dialog button layout in installer/dialogs/NotificationsDlg.wxs

**Checkpoint**: NotificationsDlg file complete. `just all` builds (dialog exists but isn't wired into flow yet).

---

## Phase 6: User Story 4 — Consistent Back Navigation (Priority: P2)

**Goal**: Rewire the full dialog navigation flow and unify VerifyReadyDlg Back button to always go to NotificationsDlg.

**Independent Test**: Run installer through all three modes → verify forward navigation: InstallModeDlg → [mode config] → MonitoringDlg → NotificationsDlg → VerifyReadyDlg. Verify Back from VerifyReadyDlg → NotificationsDlg (all modes). Verify Back from MonitoringDlg returns to correct mode-specific dialog.

### Implementation for User Story 4

- [x] T020 [US4] Update InstallModeDlg Next publish rule for standalone mode: change target from VerifyReadyDlg to MonitoringDlg in installer/dialogs/DrainCtlUI.wxs
- [x] T021 [US4] Update DashboardConfigDlg Next publish rule: change target from VerifyReadyDlg to MonitoringDlg in installer/dialogs/DrainCtlUI.wxs
- [x] T022 [US4] Update RegistrationConfigDlg Next publish rule: change target from VerifyReadyDlg to MonitoringDlg in installer/dialogs/DrainCtlUI.wxs
- [x] T023 [US4] Add MonitoringDlg publish rules in installer/dialogs/DrainCtlUI.wxs: Back conditional on INSTALL_MODE (standalone→InstallModeDlg, dashboard→DashboardConfigDlg, registration→RegistrationConfigDlg), Next→NotificationsDlg
- [x] T024 [US4] Add NotificationsDlg publish rules in installer/dialogs/DrainCtlUI.wxs: Back→MonitoringDlg, Next→VerifyReadyDlg
- [x] T025 [US4] Replace 3 first-install VerifyReadyDlg Back publish rules with single rule: `NOT Installed` → NotificationsDlg in installer/dialogs/DrainCtlUI.wxs (keep maintenance Back rules unchanged)
- [x] T026 [US4] Add DialogRef entries for MonitoringDlg and NotificationsDlg in installer/dialogs/DrainCtlUI.wxs
- [x] T027 [US4] Update dialog sequence comment at top of installer/dialogs/DrainCtlUI.wxs to reflect new flow

**Checkpoint**: Full dialog flow wired. `just all` builds. All navigation paths work correctly.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Final verification across all user stories.

- [x] T028 Build MSI with `just all` and verify zero warnings
- [ ] T029 Manual test: install with standalone mode using all defaults → verify config.json contains poll_interval=300, session_warning_threshold=80
- [ ] T030 Manual test: install with dashboard mode, modify poll interval and session threshold → verify config.json reflects custom values
- [ ] T031 Manual test: verify maintenance/repair flow is unchanged (no MonitoringDlg or NotificationsDlg shown)
- [ ] T032 Manual test: verify Back navigation from VerifyReadyDlg → NotificationsDlg → MonitoringDlg → [mode config] → InstallModeDlg for all three modes

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Localization)**: Depends on Phase 1 (same file touched in T003-T004)
- **Phase 3 (US1)**: Depends on Phase 2 (localization strings must exist)
- **Phase 4 (US2)**: Depends on Phase 2 (localization strings must exist). Can run in parallel with Phase 3.
- **Phase 5 (US3)**: Depends on Phase 2 (localization strings must exist). Can run in parallel with Phases 3-4.
- **Phase 6 (US4)**: Depends on Phases 3, 4, 5 (all dialogs must exist before wiring navigation)
- **Phase 7 (Polish)**: Depends on all previous phases

### User Story Dependencies

- **US1 (P1)**: Independent after Phase 2 — no dependency on other stories
- **US2 (P1)**: Independent after Phase 2 — no dependency on other stories
- **US3 (P2)**: Independent after Phase 2 — no dependency on other stories
- **US4 (P2)**: Depends on US1, US2, US3 — all dialog files must exist before navigation can be wired

### Within Each User Story

- Dialog structure before controls
- Controls before navigation buttons
- All tasks within US1 are sequential (same file)
- US2 tasks T010→T011-T014 (parallel controls)→T015
- US3 tasks T016→T017-T018 (parallel controls)→T019
- US4 tasks can be parallelized by target dialog (T020-T022 parallel, T023-T024 parallel)

### Parallel Opportunities

- Phases 3, 4, 5 (US1, US2, US3) can run in parallel — they modify different files
- Within US2: T011, T012, T013 can be combined into one edit session (same file)
- Within US4: T020, T021, T022 modify the same file sequentially but are independent edits

---

## Parallel Example: User Stories 1-3

```
# These can run simultaneously (different files):
US1: Strip InstallModeDlg.wxs (T007-T009)
US2: Create MonitoringDlg.wxs (T010-T015)
US3: Create NotificationsDlg.wxs (T016-T019)

# Then sequentially:
US4: Wire navigation in DrainCtlUI.wxs (T020-T027)
```

---

## Implementation Strategy

### MVP First (US1 + US2 + Navigation)

1. Complete Phase 1: Add MSI properties
2. Complete Phase 2: Add localization strings
3. Complete Phase 3: Strip InstallModeDlg (US1)
4. Complete Phase 4: Create MonitoringDlg (US2)
5. Wire minimal navigation (standalone → MonitoringDlg → VerifyReadyDlg)
6. **STOP and VALIDATE**: Test standalone flow end-to-end

### Full Delivery

1. Complete all phases through Phase 6
2. Run full verification suite (Phase 7)
3. Test all three modes with forward and back navigation
4. Verify silent install compatibility with `msiexec /quiet`

---

## Notes

- All tasks modify WiX XML files in `installer/` — no Go code changes
- The 255-char ICE03 limit was resolved in research.md: new flags go to ConfigureNotifyAction (219/255 chars)
- Dialog Height values (200 for InstallModeDlg, 300 for MonitoringDlg, 270 for NotificationsDlg) should be adjusted during implementation if controls don't fit well
- MSI properties are strings; type validation and clamping happen in the Go CLI layer
- Commit after each phase checkpoint for clean rollback points
