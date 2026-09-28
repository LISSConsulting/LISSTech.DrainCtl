# Data Model — Fleet Sessions

This feature stores the newest **successful** complete session snapshot for each host, together with the newest collection attempt. It has no per-session history table and no EAV table. All persisted and wire timestamps are signed Unix milliseconds in UTC (`INTEGER` in SQLite; JSON number); a value is never an ISO-8601 string. A `NULL` timestamp means that value does not exist or was not supplied, not zero or an unknown epoch.

## Identity and time vocabulary

* `canonical_host` is the existing lower-case, trimmed canonical host key. It is the only host value used for joins, uniqueness, paths, or commands. Display casing comes from the host registry and is not duplicated here.
* A live-session identity is `(canonical_host, session_id)`, where `session_id` is an unsigned 32-bit Windows session ID (`0..4294967295`).
* An action also binds `expected_logon_at_ms`. The agent executes only if its newly enumerated target has exactly that non-null logon time.
* `agent_instance_id` is a UUIDv7 generated and durably reserved when the agent service starts. `sequence` is that instance's unsigned 64-bit, monotonically incrementing reporting-attempt counter. Before it posts, the agent advances its local `session_generation_fence` for the canonical host to a strictly greater UUIDv7. The agent has one synchronous reporting loop, increments `sequence` for every snapshot-post attempt, and does not concurrently post snapshots. UUIDv7 values are parsed to 16 bytes and compared bytewise, never as locale-sorted text.
* `observed_at_ms` is agent wall time after the collection attempt and is display/diagnostic only; it never orders attempts or establishes freshness. `received_at_ms` is server time sampled inside the accepted write transaction.
* `latest_attempt_*` describes the newest accepted attempt, successful or fatal. `last_success_*` describes the newest accepted attempt with `collection_error:null`. Both include the accepted attempt's UUIDv7 instance ID and sequence. `session_latest` rows and `session_count` always describe `last_success_*`, never a fatal attempt. A host without a successful attempt has null `last_success_*`, null `session_count`, and no child rows. `session_generation_fence.max_instance_id` is independent of snapshot rows and is the greatest dashboard-accepted UUIDv7 generation for that canonical host.
* `NULL` in nullable metadata means unavailable or intentionally omitted by collector capability; empty string is not an alternative. Transport nullable properties are present and JSON `null` in that condition.

## Go entities

These types define the canonical in-memory agent transport model. `Sequence` and every `WorkingSetBytes` remain numeric in Go, but their browser-facing JSON representations are canonical unsigned decimal strings; `omitempty` MUST NOT be used for nullable fields.

`logical_cpu_count` is reported with every agent snapshot and persisted with its last successful snapshot. It is an integer in the inclusive range `1..1024`; the server validates each non-null session or process CPU percentage against `100 * logical_cpu_count`.
```go
type SessionSnapshot struct {
    Schema           string
    Host             string
    AgentInstanceID  string // UUIDv7, generated and durably reserved at agent service start
    Sequence         uint64 // JSON string: canonical unsigned decimal
    ObservedAtMS     int64  // display/diagnostic only
    CollectorVersion string
    LogicalCPUCount  uint16 // 1..1024
    Capabilities     SessionCapabilities
    CollectionError  *CollectionError
    Sessions         []SessionRecord // 0..500; complete set on success
}

type SessionCapabilities struct {
    SessionActions bool
    Processes      bool
    InputDelay     bool
    RemoteFX       bool
}

type CollectionError struct { Code string }

type SessionRecord struct {
    SessionID       uint32
    LogonAtMS       *int64
    User            *string
    Domain          *string
    State           SessionState
    Station         *string
    ClientName      *string
    ClientAddress   *string
    ConnectAtMS     *int64
    DisconnectAtMS  *int64
    IdleSinceMS     *int64
    CPUPercent      *float64
    WorkingSetBytes *uint64
    InputDelayMS    *uint32
    RemoteFX        *RemoteFXMetrics
    Processes       []SessionProcess // 0..5, deterministic top-N
}

type RemoteFXMetrics struct {
    FPS, QualityPercent, EncodeTimeMS, RTTMS, LossPercent, ServerSkippedFPS, NetworkSkippedFPS *float64
}

type SessionProcess struct {
    PID             uint32
    ImageName       string
    CPUPercent      *float64 // null for a first or inaccessible delta
    WorkingSetBytes uint64
}

type SessionState string
const (
    SessionActive SessionState = "active"
    SessionConnected SessionState = "connected"
    SessionConnectQuery SessionState = "connect_query"
    SessionShadow SessionState = "shadow"
    SessionDisconnected SessionState = "disconnected"
    SessionIdle SessionState = "idle"
    SessionListen SessionState = "listen"
    SessionReset SessionState = "reset"
    SessionDown SessionState = "down"
    SessionInit SessionState = "init"
    SessionUnknown SessionState = "unknown" // render after every known state
)

type SessionAction struct {
    ActionID string
    CanonicalHost string
    SessionID uint32
    ExpectedLogonAtMS int64
    Type SessionActionType
    State SessionActionState
    CreatedAtMS int64
    ExpiresAtMS int64
    RequestedBy string
    IdempotencyKey string
    RequestFingerprint []byte
}

type SessionActionType string
const (
    SessionActionLogoff     SessionActionType = "logoff"
    SessionActionMessage    SessionActionType = "message"
    SessionActionDisconnect SessionActionType = "disconnect"
)

type SessionActionState string
const (
    SessionActionQueued SessionActionState = "queued"
    SessionActionDelivered SessionActionState = "delivered"
    SessionActionCompleted SessionActionState = "completed"
    SessionActionFailed SessionActionState = "failed"
    SessionActionExpired SessionActionState = "expired"
    SessionActionSessionChanged SessionActionState = "session_changed"
    SessionActionUnsupported SessionActionState = "unsupported"
)

type SessionActionStatus struct {
    ActionID          string
    Type              SessionActionType
    CanonicalHost     string
    SessionID         uint32
    ExpectedLogonAtMS int64
    State             SessionActionState
    CreatedAtMS       int64
    ExpiresAtMS       int64
    CompletedAtMS     *int64
    ResultCode        *string
}

type SessionActionLedgerEntry struct {
    ActionID      string
    State         SessionActionLedgerState
    Outcome       *SessionActionOutcome
    ClaimedAtMS   int64
    CompletedAtMS *int64
    ExpiresAtMS   int64
}

type SessionActionLedgerState string
const (
    SessionActionLedgerClaimed  SessionActionLedgerState = "claimed"
    SessionActionLedgerTerminal SessionActionLedgerState = "terminal"
)

type SessionActionOutcome string
const (
    SessionActionOutcomeCompleted      SessionActionOutcome = "completed"
    SessionActionOutcomeFailed         SessionActionOutcome = "failed"
    SessionActionOutcomeExpired        SessionActionOutcome = "expired"
    SessionActionOutcomeSessionChanged SessionActionOutcome = "session_changed"
    SessionActionOutcomeUnsupported    SessionActionOutcome = "unsupported"
    SessionActionOutcomeDuplicate      SessionActionOutcome = "duplicate"
)

// ProjectedSessionRecord is persisted and returned after privacy projection.
// Its process values retain operational metrics; only ImageName is masked.
type ProjectedSessionRecord struct {
    SessionID       uint32
    LogonAtMS       *int64
    User, Domain    *string
    State           SessionState
    Station         *string
    ClientName, ClientAddress *string
    ConnectAtMS, DisconnectAtMS, IdleSinceMS *int64
    CPUPercent      *float64
    WorkingSetBytes *uint64 // JSON string when non-null
    InputDelayMS    *uint32
    RemoteFX        *RemoteFXMetrics
    Processes       []ProjectedSessionProcess
}

type ProjectedSessionProcess struct {
    PID             uint32
    ImageName       string // "***" when process visibility is masked
    CPUPercent      *float64
    WorkingSetBytes uint64 // JSON string
}
```

`Processes` is a computed current top-N view, not a process inventory. The collector releases every process handle before returning the snapshot.

## SQLite schema v4

The migration runs in one exclusive transaction: create the six tables and indexes, set `PRAGMA user_version = 4`, then commit. Five tables are dashboard telemetry—`session_generation_fence`, `session_snapshots`, `session_latest`, `session_action_outbox`, and `session_action_audit`; `session_action_ledger` is the agent's local durable telemetry table. The agent-local DB also uses `session_generation_fence` to reserve its host UUIDv7 before reporting. This is the canonical exact DDL for the feature; no other specification duplicates or varies it. Snapshot instance IDs are canonical UUIDv7 `TEXT`; fence values are parsed 16-byte `BLOB`, and every generation comparison parses UUIDv7 values to 16 bytes rather than using text collation. Each `*_sequence` value is the eight-byte big-endian encoding of Go `uint64`, allowing the full wire range in SQLite; comparison is performed after decoding it as `uint64`.

```sql
CREATE TABLE session_generation_fence (
    canonical_host  TEXT PRIMARY KEY,
    max_instance_id BLOB NOT NULL CHECK (length(max_instance_id) = 16)
) WITHOUT ROWID;

CREATE TABLE session_snapshots (
    canonical_host                   TEXT PRIMARY KEY,
    latest_attempt_instance_id       TEXT NOT NULL,
    latest_attempt_sequence          BLOB NOT NULL CHECK (length(latest_attempt_sequence) = 8),
    latest_attempt_observed_at_ms    INTEGER NOT NULL CHECK (latest_attempt_observed_at_ms >= 0),
    latest_attempt_received_at_ms    INTEGER NOT NULL CHECK (latest_attempt_received_at_ms >= 0),
    last_success_instance_id         TEXT,
    last_success_sequence            BLOB CHECK (last_success_sequence IS NULL OR length(last_success_sequence) = 8),
    last_success_observed_at_ms      INTEGER,
    last_success_received_at_ms      INTEGER,
    session_count                    INTEGER CHECK (session_count BETWEEN 0 AND 500),
    active_count                     INTEGER CHECK (active_count BETWEEN 0 AND 500),
    idle_count                       INTEGER CHECK (idle_count BETWEEN 0 AND 500),
    disconnected_count               INTEGER CHECK (disconnected_count BETWEEN 0 AND 500),
    user_count                       INTEGER CHECK (user_count BETWEEN 0 AND 500),
    last_activity_at_ms              INTEGER CHECK (last_activity_at_ms IS NULL OR last_activity_at_ms >= 0),
    collector_version                TEXT,
    logical_cpu_count                INTEGER CHECK (logical_cpu_count BETWEEN 1 AND 1024),
    capability_actions               INTEGER CHECK (capability_actions IN (0,1)),
    capability_processes             INTEGER CHECK (capability_processes IN (0,1)),
    capability_input_delay           INTEGER CHECK (capability_input_delay IN (0,1)),
    capability_remotefx              INTEGER CHECK (capability_remotefx IN (0,1)),
    latest_attempt_error_code        TEXT CHECK (latest_attempt_error_code IN
        ('wts_enumeration_failed','wts_metadata_failed','collector_timeout')),
    CHECK ((last_success_instance_id IS NULL) = (last_success_sequence IS NULL)),
    CHECK ((last_success_instance_id IS NULL) = (last_success_observed_at_ms IS NULL)),
    CHECK ((last_success_instance_id IS NULL) = (last_success_received_at_ms IS NULL)),
    CHECK (
        (last_success_instance_id IS NULL AND session_count IS NULL AND active_count IS NULL
         AND idle_count IS NULL AND disconnected_count IS NULL AND user_count IS NULL
         AND last_activity_at_ms IS NULL AND collector_version IS NULL AND logical_cpu_count IS NULL
         AND capability_actions IS NULL AND capability_processes IS NULL
         AND capability_input_delay IS NULL AND capability_remotefx IS NULL)
        OR
        (last_success_instance_id IS NOT NULL AND session_count IS NOT NULL AND active_count IS NOT NULL
         AND idle_count IS NOT NULL AND disconnected_count IS NOT NULL AND user_count IS NOT NULL
         AND collector_version IS NOT NULL AND logical_cpu_count IS NOT NULL
         AND capability_actions IS NOT NULL AND capability_processes IS NOT NULL
         AND capability_input_delay IS NOT NULL AND capability_remotefx IS NOT NULL)
    )
) WITHOUT ROWID;

CREATE TABLE session_latest (
    canonical_host        TEXT NOT NULL,
    session_id            INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    logon_at_ms           INTEGER CHECK (logon_at_ms IS NULL OR logon_at_ms >= 0),
    user_name             TEXT,
    domain_name           TEXT,
    state                 TEXT NOT NULL CHECK (state IN
        ('active','connected','connect_query','shadow','disconnected','idle','listen','reset','down','init','unknown')),
    station               TEXT,
    client_name           TEXT,
    client_address        TEXT,
    connect_at_ms         INTEGER CHECK (connect_at_ms IS NULL OR connect_at_ms >= 0),
    disconnect_at_ms      INTEGER CHECK (disconnect_at_ms IS NULL OR disconnect_at_ms >= 0),
    idle_since_ms         INTEGER CHECK (idle_since_ms IS NULL OR idle_since_ms >= 0),
    cpu_percent           REAL,
    working_set_bytes     INTEGER CHECK (working_set_bytes IS NULL OR working_set_bytes BETWEEN 0 AND 9223372036854775807),
    input_delay_ms        INTEGER CHECK (input_delay_ms IS NULL OR input_delay_ms >= 0),
    remotefx_fps          REAL,
    remotefx_quality_pct  REAL,
    remotefx_encode_ms    REAL,
    remotefx_rtt_ms       REAL,
    remotefx_loss_pct     REAL,
    remotefx_server_skip  REAL,
    remotefx_network_skip REAL,
    processes_json        TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (canonical_host, session_id),
    FOREIGN KEY (canonical_host) REFERENCES session_snapshots(canonical_host) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE INDEX session_snapshots_last_success_received_idx
    ON session_snapshots(last_success_received_at_ms);
CREATE INDEX session_snapshots_fleet_aggregates_idx
    ON session_snapshots(session_count, active_count, idle_count, disconnected_count, user_count, last_activity_at_ms, canonical_host);
CREATE INDEX session_latest_host_state_idx
    ON session_latest(canonical_host, state, session_id);
CREATE INDEX session_latest_state_host_idx
    ON session_latest(state, canonical_host, session_id);

CREATE TABLE session_action_outbox (
    action_id              TEXT PRIMARY KEY,
    canonical_host         TEXT NOT NULL,
    session_id             INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    expected_logon_at_ms   INTEGER NOT NULL CHECK (expected_logon_at_ms >= 0),
    action_type            TEXT NOT NULL CHECK (action_type IN ('logoff','message','disconnect')),
    message_ciphertext     BLOB,
    message_protection     TEXT,
    requested_by           TEXT NOT NULL,
    idempotency_endpoint   TEXT NOT NULL,
    idempotency_key        TEXT NOT NULL,
    request_fingerprint    BLOB NOT NULL,
    created_at_ms          INTEGER NOT NULL CHECK (created_at_ms >= 0),
    expires_at_ms          INTEGER NOT NULL CHECK (expires_at_ms >= 0),
    state                  TEXT NOT NULL CHECK (state IN
        ('queued','delivered','completed','failed','expired','session_changed','unsupported')),
    delivered_at_ms        INTEGER CHECK (delivered_at_ms IS NULL OR delivered_at_ms >= 0),
    completed_at_ms        INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    result_code            TEXT,
    CHECK (expires_at_ms = created_at_ms + 300000),
    CHECK ((state = 'queued') = (delivered_at_ms IS NULL)),
    CHECK ((state IN ('queued','delivered')) = (completed_at_ms IS NULL)),
    CHECK ((state IN ('queued','delivered')) = (result_code IS NULL)),
    CHECK (action_type = 'message' OR (message_ciphertext IS NULL AND message_protection IS NULL)),
    CHECK (
        action_type != 'message'
        OR (state IN ('queued','delivered') AND message_ciphertext IS NOT NULL AND message_protection IS NOT NULL)
        OR (state NOT IN ('queued','delivered') AND (message_ciphertext IS NULL) = (message_protection IS NULL))
    )
) WITHOUT ROWID;
CREATE UNIQUE INDEX session_action_outbox_idempotency_idx
    ON session_action_outbox(requested_by, idempotency_endpoint, idempotency_key);
CREATE INDEX session_action_outbox_delivery_idx
    ON session_action_outbox(canonical_host, state, expires_at_ms, created_at_ms);
CREATE INDEX session_action_outbox_expiry_idx
    ON session_action_outbox(state, expires_at_ms);

CREATE TABLE session_action_audit (
    event_id               TEXT PRIMARY KEY,
    event_at_ms            INTEGER NOT NULL CHECK (event_at_ms >= 0),
    action_id              TEXT NOT NULL,
    action_type            TEXT NOT NULL CHECK (action_type IN ('logoff','message','disconnect')),
    canonical_host         TEXT NOT NULL,
    session_id             INTEGER NOT NULL CHECK (session_id BETWEEN 0 AND 4294967295),
    expected_logon_at_ms   INTEGER NOT NULL CHECK (expected_logon_at_ms >= 0),
    transition             TEXT NOT NULL CHECK (transition IN
        ('queued','delivered','completed','failed','expired','session_changed','unsupported','protocol_invalid')),
    result_code            TEXT,
    requested_by           TEXT,
    CHECK ((transition = 'queued') = (requested_by IS NOT NULL)),
    CHECK ((transition IN ('queued','delivered')) = (result_code IS NULL))
) WITHOUT ROWID;
CREATE INDEX session_action_audit_action_idx ON session_action_audit(action_id, event_at_ms);
CREATE INDEX session_action_audit_retention_idx ON session_action_audit(event_at_ms);

CREATE TABLE session_action_ledger (
    action_id       TEXT PRIMARY KEY,
    state           TEXT NOT NULL CHECK (state IN ('claimed','terminal')),
    outcome         TEXT CHECK (outcome IN
        ('completed','failed','expired','session_changed','unsupported','duplicate')),
    claimed_at_ms   INTEGER NOT NULL CHECK (claimed_at_ms >= 0),
    completed_at_ms INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    expires_at_ms   INTEGER NOT NULL CHECK (expires_at_ms >= 0),
    CHECK ((state = 'claimed' AND outcome IS NULL AND completed_at_ms IS NULL)
        OR (state = 'terminal' AND outcome IS NOT NULL AND completed_at_ms IS NOT NULL))
) WITHOUT ROWID;
CREATE INDEX session_action_ledger_cleanup_idx
    ON session_action_ledger(state, completed_at_ms, expires_at_ms);
```

Capability, collector version, logical CPU count, aggregates, and rows are values from the last successful snapshot. In the same replacement transaction, `session_count` is array length; `active_count` counts `active` plus `connected`; `idle_count` and `disconnected_count` count only their exact states; `user_count` is the number of distinct non-null **projected** `(user_name, domain_name)` pairs (and is zero when identity is hidden); and `last_activity_at_ms` is the maximum non-null `last_input`, `connect_at_ms`, or `logon_at_ms` from successful rows. A fatal attempt records only its accepted instance ID, sequence, observation/receipt time, and `latest_attempt_error_code`; it leaves those last-success values untouched. A successful snapshot clears `latest_attempt_error_code` and replaces all last-success fields and rows. `processes_json` is a compact, structurally validated UTF-8 JSON array of at most five `ProjectedSessionProcess` values; it is not queried in SQL. SQLite `working_set_bytes` accepts only `0..MaxInt64`, while the agent's `uint64` value is rejected before binding above `MaxInt64`. Optional numeric values are validated finite before SQLite binding.

The outbox's idempotency scope is the authenticated principal plus the canonical endpoint string (method, canonical host, and numeric session ID). `request_fingerprint` is a fixed cryptographic hash of the normalized request: action type, expected logon time, and normalized message (or an empty message for disconnect and logoff). The unique index makes concurrent same-key requests deterministic: same scope and fingerprint replay the original action response; a differing fingerprint is a conflict. The protected message is present while a message action is active and is erased on any terminal transition; disconnect and logoff cannot persist protected message fields. The paired-null constraint permits that erasure. `delivered_at_ms` records the first delivery; a delivered row remains selectable for repeated same-ID delivery until terminal outcome or expiry.

`session_action_audit` is append-only. `requested_by` is non-null only for `queued`, where it is the authenticated admin principal (maximum 256 UTF-8 bytes); it is null for other transitions. `result_code` is null for `queued`/`delivered` and otherwise a closed action-contract result code. Neither field contains identity, action text, or raw diagnostics. Before calling WTS, the agent transactionally inserts a `claimed` ledger row. It never executes a recovered or redelivered claimed action; it reports the safe failed/duplicate result and later records terminal outcome. Ledger cleanup is bounded only after dashboard acknowledgement, or after expiry plus the configured retention period.

## Replacement, ordering, delivery, and retention

1. Authenticate and validate the complete request before opening the write transaction.
2. Read the host `session_generation_fence.max_instance_id`, decode its UUIDv7 bytes, and compare it with the request UUIDv7 bytes. If no fence exists, insert the request UUIDv7 as the initial fence. If the request instance equals `latest_attempt_instance_id`, accept only a strictly greater decoded `latest_attempt_sequence`; equal/lower sequence commits no change as stale/idempotent. If it differs and is strictly greater than the fence, advance the fence and accept it as a new generation regardless of sequence. If it differs and is less than or equal to the fence, commit no change as stale. This permanently rejects a delayed old-generation snapshot after a newer generation commits. `observed_at_ms` does not participate in either comparison.
3. For an accepted fatal request (`collection_error` non-null), update only `latest_attempt_instance_id`, `latest_attempt_sequence`, `latest_attempt_observed_at_ms`, `latest_attempt_received_at_ms`, and `latest_attempt_error_code`. Do not delete or alter `session_latest`, aggregate fields, `last_success_*`, collector version, logical CPU count, or capabilities. Commit and publish a privacy-safe error-status event.
4. For an accepted successful request, delete all `session_latest` rows, update all latest-attempt values, calculate and persist the aggregates, set last-success instance ID, sequence, observation/receipt, collector version, logical CPU count, capabilities, and null error code, then insert every projected validated session in one transaction. A successful empty `sessions` array leaves zero child rows and zero counts; it is the only condition that clears rows.
5. Never merge individual rows. A session absent from an accepted successful complete snapshot is deleted.
6. Report delivery selects up to 20 unexpired rows in **both** `queued` and `delivered` state for the authenticated host, ordered by `created_at_ms`. It changes selected `queued` rows to `delivered` and records first `delivered_at_ms`; it may reselect delivered rows unchanged. The agent's local action-ID ledger prevents a second WTS call.
7. Hourly (and immediately after reducing `retention_hours`), expire active (`queued` or `delivered`) actions. Independently delete terminal outbox rows by their terminal timestamp, audit rows by `event_at_ms`, and acknowledged/expired ledger rows by their own action-relative cutoffs. Delete snapshots by `last_success_received_at_ms` (or `latest_attempt_received_at_ms` for no-success error-only rows); this cascade removes only associated `session_latest` rows. It MUST NOT delete `session_generation_fence`: the fence survives snapshot retention and privacy purges. Terminal action rows and audit/ledger records may outlive a purged snapshot until their own retention cutoff.

Freshness is independent of the latest fatal error: it is `unknown` when there is no `last_success_received_at_ms`; `offline` when the host registry says off or age is greater than `10 * effective_heartbeat_interval_ms`; `stale` when age is greater than `3 * effective_heartbeat_interval_ms`; otherwise `fresh`. `collection_status` separately reports a latest fatal error. Display precedence is `offline`, then `error`, then `stale`, then `fresh`/`unknown`. Agent wall time never establishes freshness.

## Configuration and capability semantics

```yaml
sessions:
  enabled: true
  collect_processes: true
  top_processes: 3       # integer 0..5
  retention_hours: 24    # integer 1..168
  allow_actions: false
  identity_visibility: full # full | masked | hidden
  client_visibility: full   # full | masked | hidden
  process_visibility: full  # full | masked | hidden
```

Defaults are shown. `enabled=false` stops collection and makes session endpoints feature-disabled; existing rows remain to retention. `collect_processes=false` or `top_processes=0` produces `processes: []`, not a collection error. Missing optional metrics are null and never remove WTS metadata or sibling metrics. A per-process `cpu_percent` is null for the first or an inaccessible CPU delta. Process top-N order is deterministic: CPU percent descending (null last), working set bytes descending, image name case-insensitive ascending, then PID ascending. `actions_available` requires server `allow_actions`, authenticated admin status, a fresh error-free successful snapshot, and its `session_actions` capability.

Privacy projection is applied at ingest before SQLite persistence and again when constructing a response. `full` preserves bounded collected values, `masked` persists/returns deterministic redaction, and `hidden` persists/returns null (or `[]` for processes). Later setting changes cannot reconstruct discarded values. Identity covers `user`, `domain`, and `station`; client covers `client_name` and `client_address`; process visibility hides the complete process list, while a masked process retains numeric PID, nullable CPU, and `uint64` working set and changes only `image_name` to `***`. Masked empty/null identity fields remain null; one Unicode scalar becomes `*`; otherwise first scalar plus `***`. A client address is always `***`. Authorization and execution binding use unprojected values only before projection.

Any change to `identity_visibility`, `client_visibility`, or `process_visibility` immediately deletes every `session_snapshots` row in the settings transaction; foreign-key cascade deletes all `session_latest` rows. It MUST NOT delete or lower `session_generation_fence`, so an old delayed generation cannot reclaim the host after a privacy purge. In that same transaction, every active (`queued` or `delivered`) action is transitioned to `expired` with result code `privacy_policy_changed`, its message protection/ciphertext is erased, and an `expired` audit event is appended. The next accepted successful projected snapshot repopulates telemetry under the new policy. This applies to both tightening and loosening: a looser policy cannot reconstruct old data.
