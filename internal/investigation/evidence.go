//go:build windows

package investigation

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	maxEvidenceBytes = 8000
	maxFactCount     = 134
	maxPoints        = 61
)

type Freshness string
type DrainState string
type DetectorState string
type Metric string
type Channel string

const (
	FreshnessFresh          Freshness     = "fresh"
	FreshnessStale          Freshness     = "stale"
	FreshnessUnknown        Freshness     = "unknown"
	DrainAllowAll           DrainState    = "allow_all"
	DrainDraining           DrainState    = "draining"
	DrainUnknown            DrainState    = "unknown"
	DetectorConfirmed       DetectorState = "confirmed"
	DetectorUnexplained     DetectorState = "unexplained"
	DetectorDrainAssociated DetectorState = "drain_associated"
	DetectorUnknownContext  DetectorState = "unknown_context"
	DetectorUnavailable     DetectorState = "unavailable"
)

var metrics = map[Metric]struct{}{
	"cpu_pct": {}, "cpu_p95_pct": {}, "mem_avail_mb": {}, "mem_total_mb": {}, "pages_sec": {}, "disk_queue": {}, "tcp_retrans_sec": {}, "input_delay_p50_ms": {}, "input_delay_p95_ms": {}, "input_delay_max_ms": {}, "session_cpu_p95_pct": {}, "session_cpu_p50_pct": {}, "session_mem_p95_bytes": {}, "session_mem_p50_bytes": {}, "rfx_fps_out": {}, "rfx_fps_out_p50": {}, "rfx_skip_server_sec": {}, "rfx_skip_net_sec": {}, "rfx_encode_ms": {}, "rfx_encode_ms_p50": {}, "rfx_quality_pct": {}, "rfx_quality_pct_p50": {}, "rfx_rtt_ms": {}, "rfx_rtt_ms_p50": {}, "rfx_loss_pct": {}, "rfx_loss_pct_p50": {}, "rfx_skip_server_sec_p50": {}, "rfx_skip_net_sec_p50": {}, "sessions_total": {}, "sessions_active": {}, "sessions_disconnected": {}, "sessions_max": {},
}
var channels = map[Channel]struct{}{"agent": {}, "dashboard": {}, "eventlog": {}, "wmi": {}, "perf": {}, "service": {}, "unknown": {}, "custom_channel": {}}

// EvidenceInput has only field-by-field, identity-free source facts.
type EvidenceInput struct {
	SnapshotAtMS, SourceTimeMS int64
	Source                     SourceRef
	Freshness                  Freshness
	Drain                      DrainState
	Detector                   DetectorState
	EventSpike                 *EventSpikeEvidence
	SessionDrop                *SessionDropEvidence
	Local                      []EvidencePoint
	Fleet                      []FleetEvidencePoint
	Omissions                  []OmissionCode
}
type EventSpikeEvidence struct {
	Channel                                                                         Channel
	WindowFromMS, WindowToMS, ObservedCount, ExpectedCountMilli, TailProbabilityPPB int64
}
type SessionDropEvidence struct {
	ObservedCount, ReferenceCount, ExpectedCountMilli, AbsoluteLoss, RelativeLossBPS, TailProbabilityPPB int64
	Classification                                                                                       DetectorState
}
type EvidencePoint struct {
	AtMS       int64
	Metric     Metric
	ValueMilli int64
}
type FleetEvidencePoint struct {
	EvidencePoint
	Peer int
}

type evidenceV1 struct {
	V            int                `json:"v"`
	SnapshotAtMS int64              `json:"snapshot_at_ms"`
	Window       evidenceWindow     `json:"window"`
	Source       evidenceSource     `json:"source"`
	Context      evidenceContext    `json:"context"`
	Local        *evidenceAggregate `json:"local,omitempty"`
	Fleet        *evidenceAggregate `json:"fleet,omitempty"`
	Omitted      []evidenceOmission `json:"omitted"`
}
type evidenceWindow struct {
	FactID string `json:"fact_id"`
	FromMS int64  `json:"from_ms"`
	ToMS   int64  `json:"to_ms"`
}
type evidenceSource struct {
	FactID  string     `json:"fact_id"`
	Kind    SourceKind `json:"kind"`
	ID      int64      `json:"id"`
	Label   string     `json:"label"`
	Anomaly string     `json:"anomaly"`
	Details any        `json:"details"`
}
type evidenceContext struct {
	FactID    string        `json:"fact_id"`
	Freshness Freshness     `json:"freshness"`
	Drain     DrainState    `json:"drain"`
	Detector  DetectorState `json:"detector"`
}
type evidenceAggregate struct {
	Points []evidencePoint `json:"points"`
}
type evidencePoint struct {
	FactID     string `json:"fact_id"`
	AtMS       int64  `json:"at_ms"`
	Label      string `json:"label"`
	Metric     Metric `json:"metric"`
	ValueMilli int64  `json:"value_milli"`
}
type evidenceOmission struct {
	FactID string       `json:"fact_id"`
	Code   OmissionCode `json:"code"`
}
type eventSpikeDetails struct {
	Channel            Channel `json:"channel"`
	WindowFromMS       int64   `json:"window_from_ms"`
	WindowToMS         int64   `json:"window_to_ms"`
	ObservedCount      int64   `json:"observed_count"`
	ExpectedCountMilli int64   `json:"expected_count_milli"`
	TailProbabilityPPB int64   `json:"tail_probability_ppb"`
}
type sessionDropDetails struct {
	ObservedCount      int64         `json:"observed_count"`
	ReferenceCount     int64         `json:"reference_count"`
	ExpectedCountMilli int64         `json:"expected_count_milli"`
	AbsoluteLoss       int64         `json:"absolute_loss"`
	RelativeLossBPS    int64         `json:"relative_loss_bps"`
	TailProbabilityPPB int64         `json:"tail_probability_ppb"`
	Classification     DetectorState `json:"classification"`
}

func BuildEvidence(input EvidenceInput, requestSizer func([]byte) int) (EvidenceSnapshot, error) {
	if err := validateEvidenceInput(input); err != nil {
		return EvidenceSnapshot{}, err
	}
	local, fleet, omissions := append([]EvidencePoint(nil), input.Local...), append([]FleetEvidencePoint(nil), input.Fleet...), orderedOmissions(input.Omissions)
	for {
		canonical, facts, err := marshalEvidence(input, local, fleet, omissions)
		if err != nil {
			return EvidenceSnapshot{}, err
		}
		if len(canonical) <= maxEvidenceBytes && (requestSizer == nil || requestSizer(canonical) <= 16384) {
			return EvidenceSnapshot{Version: EvidenceVersion, Kind: SnapshotKindAvailable, SourceTimeMS: input.SourceTimeMS, SnapshotAtMS: input.SnapshotAtMS, FromMS: input.SourceTimeMS - 1800000, ToMS: clippedTo(input.SourceTimeMS, input.SnapshotAtMS), CanonicalJSON: canonical, FactIDs: facts, OmissionCodes: omissions}, nil
		}
		if len(fleet) > 0 {
			fleet = fleet[:len(fleet)-1]
			omissions = appendOmission(omissions, OmissionFleetPoints)
			continue
		}
		if len(local) > 0 {
			local = local[:len(local)-1]
			omissions = appendOmission(omissions, OmissionLocalPoints)
			continue
		}
		return UnavailableEvidence(input.SourceTimeMS, input.SnapshotAtMS), nil
	}
}
func UnavailableEvidence(sourceTimeMS, snapshotAtMS int64) EvidenceSnapshot {
	return EvidenceSnapshot{Version: EvidenceVersion, Kind: SnapshotKindUnavailable, SourceTimeMS: sourceTimeMS, SnapshotAtMS: snapshotAtMS, FromMS: sourceTimeMS - 1800000, ToMS: clippedTo(sourceTimeMS, snapshotAtMS), CanonicalJSON: []byte("{}")}
}
func EvidenceHash(snapshot EvidenceSnapshot) [sha256.Size]byte {
	return sha256.Sum256(snapshot.CanonicalJSON)
}
func clippedTo(source, snapshot int64) int64 {
	if source+1800000 < snapshot {
		return source + 1800000
	}
	return snapshot
}
func appendOmission(items []OmissionCode, code OmissionCode) []OmissionCode {
	for _, v := range items {
		if v == code {
			return items
		}
	}
	return orderedOmissions(append(items, code))
}
func orderedOmissions(items []OmissionCode) []OmissionCode {
	order := []OmissionCode{OmissionPreUpgradeContext, OmissionRetentionExpired, OmissionFreshnessUnavailable, OmissionDrainUnavailable, OmissionDetectorUnavailable, OmissionLocalPoints, OmissionFleetPoints, OmissionLocalAggregate, OmissionFleetAggregate}
	out := make([]OmissionCode, 0, len(items))
	for _, want := range order {
		for _, got := range items {
			if got == want {
				out = append(out, want)
				break
			}
		}
	}
	return out
}
func validateEvidenceInput(in EvidenceInput) error {
	if in.SnapshotAtMS <= 0 || in.SourceTimeMS <= 0 || in.SnapshotAtMS < in.SourceTimeMS || in.Source.ID <= 0 || (in.Source.Kind != SourceKindEventSpike && in.Source.Kind != SourceKindSessionDrop) || len(in.Local) > maxPoints || len(in.Fleet) > maxPoints || len(in.Omissions) > 9 {
		return errors.New("invalid evidence input")
	}
	if in.Freshness != "fresh" && in.Freshness != "stale" && in.Freshness != "unknown" || in.Drain != "allow_all" && in.Drain != "draining" && in.Drain != "unknown" {
		return errors.New("invalid evidence context")
	}
	if (in.Source.Kind == SourceKindEventSpike) == (in.EventSpike == nil) || (in.Source.Kind == SourceKindSessionDrop) == (in.SessionDrop == nil) {
		return errors.New("invalid source details")
	}
	if in.Source.Kind == SourceKindSessionDrop && (in.Detector != DetectorUnexplained || in.SessionDrop.Classification != DetectorUnexplained) {
		return errors.New("ineligible session drop")
	}
	for _, p := range in.Local {
		if err := validatePoint(p, in.SourceTimeMS, in.SnapshotAtMS); err != nil {
			return err
		}
	}
	for _, p := range in.Fleet {
		if p.Peer < 1 || p.Peer > 60 {
			return errors.New("invalid peer")
		}
		if err := validatePoint(p.EvidencePoint, in.SourceTimeMS, in.SnapshotAtMS); err != nil {
			return err
		}
	}
	return nil
}
func validatePoint(p EvidencePoint, source, snapshot int64) error {
	if p.AtMS < source-1800000 || p.AtMS > clippedTo(source, snapshot) || p.ValueMilli < 0 || p.ValueMilli > 9007199254740991 {
		return errors.New("invalid evidence point")
	}
	if _, ok := metrics[p.Metric]; !ok {
		return errors.New("invalid metric")
	}
	return nil
}
func marshalEvidence(in EvidenceInput, local []EvidencePoint, fleet []FleetEvidencePoint, omissions []OmissionCode) ([]byte, []string, error) {
	sort.Slice(local, func(i, j int) bool {
		if local[i].AtMS == local[j].AtMS {
			return local[i].Metric < local[j].Metric
		}
		return local[i].AtMS < local[j].AtMS
	})
	sort.Slice(fleet, func(i, j int) bool {
		if fleet[i].AtMS == fleet[j].AtMS {
			if fleet[i].Peer == fleet[j].Peer {
				return fleet[i].Metric < fleet[j].Metric
			}
			return fleet[i].Peer < fleet[j].Peer
		}
		return fleet[i].AtMS < fleet[j].AtMS
	})
	anomaly := "evtspike"
	var details any
	if in.EventSpike != nil {
		e := in.EventSpike
		if _, ok := channels[e.Channel]; !ok {
			return nil, nil, errors.New("invalid channel")
		}
		details = eventSpikeDetails{e.Channel, e.WindowFromMS, e.WindowToMS, e.ObservedCount, e.ExpectedCountMilli, e.TailProbabilityPPB}
	} else {
		s := in.SessionDrop
		details = sessionDropDetails{s.ObservedCount, s.ReferenceCount, s.ExpectedCountMilli, s.AbsoluteLoss, s.RelativeLossBPS, s.TailProbabilityPPB, s.Classification}
		anomaly = "session_drop"
	}
	e := evidenceV1{V: 1, SnapshotAtMS: in.SnapshotAtMS, Window: evidenceWindow{"F001", in.SourceTimeMS - 1800000, clippedTo(in.SourceTimeMS, in.SnapshotAtMS)}, Source: evidenceSource{"F002", in.Source.Kind, in.Source.ID, "source", anomaly, details}, Context: evidenceContext{"F003", in.Freshness, in.Drain, in.Detector}}
	id := 4
	next := func() string { x := fmt.Sprintf("F%03d", id); id++; return x }
	if len(local) > 0 {
		e.Local = &evidenceAggregate{Points: make([]evidencePoint, len(local))}
		for i, p := range local {
			e.Local.Points[i] = evidencePoint{next(), p.AtMS, "source", p.Metric, p.ValueMilli}
		}
	}
	if len(fleet) > 0 {
		e.Fleet = &evidenceAggregate{Points: make([]evidencePoint, len(fleet))}
		for i, p := range fleet {
			e.Fleet.Points[i] = evidencePoint{next(), p.AtMS, fmt.Sprintf("peer_%d", p.Peer), p.Metric, p.ValueMilli}
		}
	}
	e.Omitted = make([]evidenceOmission, len(omissions))
	for i, c := range omissions {
		e.Omitted[i] = evidenceOmission{next(), c}
	}
	if id-1 > maxFactCount {
		return nil, nil, errors.New("too many facts")
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, nil, err
	}
	facts := make([]string, id-1)
	for i := range facts {
		facts[i] = fmt.Sprintf("F%03d", i+1)
	}
	return raw, facts, nil
}
