# Contract: Named-Pipe Access Control

## Scope

Defines the authorization contract for verbs dispatched through the DrainCtl service
named pipe. Applies to `internal/pipe/pipe.go` `handlePipeConn` and all verb handlers
registered against it.

## Verb classification

### Read-only (no SID check)

| Verb | Purpose |
|------|---------|
| `status` | Current drain mode + service state |
| `history` | Drain-mode transition history (SQLite-backed) |
| `servers` | Registered server list |

Any caller the OS permitted past the pipe DACL may invoke these verbs. No change from
current behavior.

### Privileged (SID check required)

| Verb | Purpose |
|------|---------|
| `register` | Register this agent with a dashboard instance |
| `remove-server` | Remove a registered server record from the dashboard |
| `baseline-reset` | Reset evtspike's learned baseline for a channel |

Caller MUST be `SYSTEM` (`S-1-5-18`) OR a member of the local Administrators group
(`S-1-5-32-544`), verified via effective token (`windows.Token.IsMember`).

## Authorization algorithm

```
On accept(conn):
    read request message (shared readPipeMessage helper — FR-008)
    decode into PipeRequest { Cmd, Args }

    if Cmd is privileged:
        pid := GetNamedPipeClientProcessId(conn server-side handle)
        procHandle := OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
        defer CloseHandle(procHandle)

        tokenHandle := OpenProcessToken(procHandle, TOKEN_QUERY)
        defer tokenHandle.Close()

        sid := GetTokenInformation(TokenUser)
        if sid == SYSTEM_SID:
            allow
        else if windows.Token(tokenHandle).IsMember(adminSID):
            allow
        else:
            deny
    else:
        allow
```

## Deny response

- **Over the wire**: `PipeResponse{OK: false, Error: "access denied"}`.
- **Service log** (`slog.Warn`):
  `level=WARN msg="pipe=access_denied" cmd=<verb> sid=<S-1-5-…>`.
- **ETW audit**: emit event `EvtAccessDenied` (event ID 5004, already defined) through
  the existing ETW audit channel. Payload fields match other audit events in the same
  channel.
- **No implicit retry**: the connection closes after the deny response; caller must
  reconnect.

## Error cases

- **Pipe disconnects before SID lookup completes**: treat as deny. Log at
  `slog.Warn` with `reason="client disconnected during SID check"`.
- **`OpenProcess` fails (e.g. process exited)**: treat as deny. Log the underlying
  Win32 error.
- **`OpenProcessToken` or `GetTokenInformation` fails**: treat as deny. Log the
  underlying Win32 error.

## Handle-lifetime invariants

Every authorization check opens three native handles. All paths (success, deny, error)
MUST close them:

1. Process handle from `OpenProcess` → `defer windows.CloseHandle(procHandle)` immediately after successful open.
2. Token handle from `OpenProcessToken` → `defer tokenHandle.Close()`.
3. SID buffer (from `GetTokenInformation(TokenUser)`) lives in the returned byte slice;
   no extra free needed, but the `*windows.SID` pointer MUST NOT escape the helper
   (it aliases into the slice).

## Testing contract

- **Unit** (`internal/pipe/sid_test.go`): stub-injectable token reader; table of
  (SID class → verdict) cases covering SYSTEM, admin, non-admin, filtered-admin (UAC),
  unknown SID, token-open error, process-open error.
- **Integration** (`internal/pipe/pipe_caller_test.go`): open a pipe from an admin
  dev session, invoke `register` against a stub `HandleRegister`, assert success.
  Simulate the deny branch via scripted reader; assert response shape and log line.
- **Manual**: from a non-admin shell on the dev box, run `drainctl register https://dash/`
  → confirm `access denied`. Run `drainctl status` from the same shell → confirm still works.

## Future work (not in scope for 009)

- **Tighter DACL**: consider a future SDDL that explicitly grants `Interactive Users`
  (`S-1-5-4`) read-only, SYSTEM+Admins full. Staged separately so it can revert
  without losing the per-verb guard.
