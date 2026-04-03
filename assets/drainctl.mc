MessageIdTypedef=DWORD

SeverityNames=(
    Success=0x0:STATUS_SEVERITY_SUCCESS
    Informational=0x1:STATUS_SEVERITY_INFORMATIONAL
    Warning=0x2:STATUS_SEVERITY_WARNING
    Error=0x3:STATUS_SEVERITY_ERROR
)

FacilityNames=(
    System=0x0:FACILITY_SYSTEM
    Runtime=0x1:FACILITY_RUNTIME
)

LanguageNames=(English=0x409:MSG00409)

; ── Informational (1xxx) ──────────────────────────────────────────────────────

MessageId=1000
Severity=Informational
Facility=Runtime
SymbolicName=MSG_SERVICE_STARTED
Language=English
%1
.

MessageId=1001
Severity=Informational
Facility=Runtime
SymbolicName=MSG_SERVICE_STOPPED
Language=English
%1
.

MessageId=1002
Severity=Informational
Facility=Runtime
SymbolicName=MSG_CHECK_HEALTHY
Language=English
%1
.

MessageId=1003
Severity=Informational
Facility=Runtime
SymbolicName=MSG_CONFIG_RELOADED
Language=English
%1
.

MessageId=1004
Severity=Informational
Facility=Runtime
SymbolicName=MSG_TRANSITION_DETECTED
Language=English
%1
.

; ── Warning (2xxx) ────────────────────────────────────────────────────────────

MessageId=2000
Severity=Warning
Facility=Runtime
SymbolicName=MSG_CHECK_GRACE
Language=English
%1
.

; ── Error (3xxx) ──────────────────────────────────────────────────────────────

MessageId=3000
Severity=Error
Facility=Runtime
SymbolicName=MSG_CHECK_ALERT
Language=English
%1
.

MessageId=3001
Severity=Error
Facility=Runtime
SymbolicName=MSG_REGISTRY_READ_FAILED
Language=English
%1
.

MessageId=3002
Severity=Error
Facility=Runtime
SymbolicName=MSG_SERVICE_ERROR
Language=English
%1
.
