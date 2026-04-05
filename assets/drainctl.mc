MessageIdTypedef=DWORD

SeverityNames=(
    Success=0x0:STATUS_SEVERITY_SUCCESS
)

FacilityNames=(
    System=0x0:FACILITY_SYSTEM
)

LanguageNames=(English=0x409:MSG00409)

; ── Informational (1xxx) ──────────────────────────────────────────────────────

MessageId=1000
Severity=Success
Facility=System
SymbolicName=MSG_SERVICE_STARTED
Language=English
%1
.

MessageId=1001
Severity=Success
Facility=System
SymbolicName=MSG_SERVICE_STOPPED
Language=English
%1
.

MessageId=1002
Severity=Success
Facility=System
SymbolicName=MSG_CHECK_HEALTHY
Language=English
%1
.

MessageId=1003
Severity=Success
Facility=System
SymbolicName=MSG_CONFIG_RELOADED
Language=English
%1
.

MessageId=1004
Severity=Success
Facility=System
SymbolicName=MSG_TRANSITION_DETECTED
Language=English
%1
.

MessageId=1099
Severity=Success
Facility=System
SymbolicName=MSG_GENERIC_INFO
Language=English
%1
.

; ── Warning (2xxx) ────────────────────────────────────────────────────────────

MessageId=2000
Severity=Success
Facility=System
SymbolicName=MSG_CHECK_GRACE
Language=English
%1
.

MessageId=2099
Severity=Success
Facility=System
SymbolicName=MSG_GENERIC_WARNING
Language=English
%1
.

; ── Error (3xxx) ──────────────────────────────────────────────────────────────

MessageId=3000
Severity=Success
Facility=System
SymbolicName=MSG_CHECK_ALERT
Language=English
%1
.

MessageId=3001
Severity=Success
Facility=System
SymbolicName=MSG_REGISTRY_READ_FAILED
Language=English
%1
.

MessageId=3002
Severity=Success
Facility=System
SymbolicName=MSG_SERVICE_ERROR
Language=English
%1
.

MessageId=3099
Severity=Success
Facility=System
SymbolicName=MSG_GENERIC_ERROR
Language=English
%1
.
