//go:build windows

package investigation

import (
	"crypto/sha256"
	"reflect"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/telemetry"
)

func TestAttemptFromTelemetryPreservesEnumsAndTimes(t *testing.T) {
	created := time.Date(2026, time.September, 27, 8, 9, 10, 987_000_000, time.FixedZone("offset", -4*60*60))
	started := created.Add(time.Minute)
	attempt := telemetry.InvestigationAttempt{
		ID: 17, Source: telemetry.InvestigationSource{Kind: telemetry.SourceSessionDrop, ID: 23}, Number: 4,
		Initiation: telemetry.InitiationRetry, State: telemetry.StateRunning, CreatedAt: created, QueuedAt: created,
		StartedAt: &started, TerminalReason: telemetry.ReasonTimeout, EvidenceHash: sha256.Sum256([]byte("evidence")),
	}

	got := AttemptFromTelemetry(attempt)
	if got.Source != (SourceRef{Kind: SourceKindSessionDrop, ID: 23}) || got.Initiation != AttemptInitiationRetry || got.State != AttemptStateRunning || got.TerminalReason != TerminalReasonTimeout {
		t.Fatalf("enum conversion = %#v", got)
	}
	if got.CreatedAtMS != created.UTC().UnixMilli() || got.QueuedAtMS != created.UTC().UnixMilli() || got.StartedAtMS == nil || *got.StartedAtMS != started.UTC().UnixMilli() {
		t.Fatalf("time conversion = %#v", got)
	}
	if got.SendCompletedAtMS != nil {
		t.Fatalf("absent timestamp = %v, want nil", *got.SendCompletedAtMS)
	}
	if roundTrip := AttemptToTelemetry(got); roundTrip.Source != attempt.Source || roundTrip.Initiation != attempt.Initiation || roundTrip.State != attempt.State || roundTrip.TerminalReason != attempt.TerminalReason || roundTrip.CreatedAt.UnixMilli() != attempt.CreatedAt.UnixMilli() || roundTrip.StartedAt == nil || roundTrip.StartedAt.UnixMilli() != attempt.StartedAt.UnixMilli() {
		t.Fatalf("attempt reverse conversion = %#v", roundTrip)
	}
}

func TestEvidenceSnapshotTelemetryConversionPreservesTimesAndOmissions(t *testing.T) {
	testCases := []struct {
		name      string
		canonical []byte
		omissions []OmissionCode
	}{
		{name: "no omitted field", canonical: []byte(`{"facts":["F001"]}`)},
		{name: "empty omitted field", canonical: []byte(`{"facts":["F001"],"omitted":[]}`), omissions: []OmissionCode{}},
		{name: "omissions", canonical: []byte(`{"facts":["F001"],"omitted":[{"fact_id":"F002","code":"retention_expired"}]}`), omissions: []OmissionCode{OmissionRetentionExpired}},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := EvidenceSnapshot{
				AttemptID: 3, Version: 1, Kind: SnapshotKindAvailable,
				SourceTimeMS: 1000, SnapshotAtMS: 2000, FromMS: -1_799_000, ToMS: 2000,
				CanonicalJSON: tc.canonical, FactIDs: []string{"F001"}, OmissionCodes: tc.omissions,
			}
			persisted, err := EvidenceSnapshotToTelemetry(evidence)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.SnapshotKind != telemetry.SnapshotAvailable || persisted.SourceTime.UnixMilli() != evidence.SourceTimeMS || persisted.Hash != sha256.Sum256(evidence.CanonicalJSON) {
				t.Fatalf("persistence evidence = %#v", persisted)
			}
			got := EvidenceSnapshotFromTelemetry(evidence.AttemptID, persisted)
			if !reflect.DeepEqual(got, evidence) {
				t.Fatalf("evidence round trip = %#v, want %#v", got, evidence)
			}
		})
	}
}

func TestReportTelemetryConversionPreservesEnums(t *testing.T) {
	report := Report{
		ResultVersion:     1,
		Summary:           ReportText{TextKind: TextKindSummary, Text: "summary", FactIDs: []string{"F001"}},
		OverallAssessment: OverallAssessmentLocalizedOperationalIssue, EvidenceSufficiency: EvidenceSufficiencyPartial,
		HumanReviewRequired:         true,
		Hypotheses:                  []Hypothesis{{Rank: 1, Confidence: ConfidenceHigh, TextKind: TextKindHypothesis, Text: "hypothesis", SupportingFactIDs: []string{"F001"}, ContradictingFactIDs: []string{"F002"}}},
		MissingEvidence:             []MissingEvidence{{Category: MissingEvidenceFleetComparison, TextKind: TextKindMissingEvidence, Text: "missing", RelatedFactIDs: []string{"F003"}}},
		RecommendedDiagnosticChecks: []RecommendedDiagnosticCheck{{Rank: 1, CheckType: DiagnosticCheckCompareFleet, TextKind: TextKindDiagnosticCheck, Text: "check", FactIDs: []string{"F001"}, RelatedHypothesisRanks: []int{1}}},
	}

	persisted, err := ReportToTelemetry(report)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MissingEvidence[0].Ordinal != 1 || persisted.Hypotheses[0].Confidence != "high" || persisted.RecommendedChecks[0].CheckType != "compare_fleet" {
		t.Fatalf("persistence report = %#v", persisted)
	}
	if got := ReportFromTelemetry(persisted); !reflect.DeepEqual(got, report) {
		t.Fatalf("report round trip = %#v, want %#v", got, report)
	}
}

func TestProvenanceAndCountsTelemetryConversion(t *testing.T) {
	provenance := Provenance{
		AttemptID: 9, ProviderProfile: ProviderProfileName("openai_responses"), ProviderEndpoint: "https://example.test/responses", RequestedModel: "model",
		ResponseFormat: ResponseFormatName("anomaly_investigation_v1"), SendAuthorizedAtMS: 3000, SendCompletedAtMS: 4000,
		RequestHeaderBytes: 1, RequestBodyBytes: 2, ResponseHeaderBytes: 3, ResponseBodyBytes: 4, ValidationOutcome: ValidationOutcomeAccepted,
	}
	persisted, err := ProvenanceToTelemetry(provenance)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProvenanceFromTelemetry(provenance.AttemptID, persisted); !reflect.DeepEqual(got, provenance) {
		t.Fatalf("provenance round trip = %#v, want %#v", got, provenance)
	}
	counts := AttemptCounts{Queued: 1, Running: 2, Completed: 3, InsufficientEvidence: 4, Failed: 5}
	persistedCounts := AttemptCountsToTelemetry(counts)
	gotCounts, err := AttemptCountsFromTelemetry(persistedCounts)
	if err != nil {
		t.Fatal(err)
	}
	if gotCounts != counts {
		t.Fatalf("count round trip = %#v, want %#v", gotCounts, counts)
	}
}
