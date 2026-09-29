//go:build windows

package investigation

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestBuildEvidenceAllocatesContiguousFactsAndClipsWindow(t *testing.T) {
	in := EvidenceInput{SnapshotAtMS: 2_000_000, SourceTimeMS: 1_000_000, Source: SourceRef{Kind: SourceKindEventSpike, ID: 7}, Freshness: FreshnessFresh, Drain: DrainAllowAll, Detector: DetectorConfirmed, EventSpike: &EventSpikeEvidence{Channel: "agent", WindowFromMS: 0, WindowToMS: 1_000_000}, Local: []EvidencePoint{{AtMS: 1_000_000, Metric: "sessions_active", ValueMilli: 0}}, Fleet: []FleetEvidencePoint{{EvidencePoint: EvidencePoint{AtMS: 1_000_000, Metric: "cpu_pct", ValueMilli: 1}, Peer: 1}}, Omissions: []OmissionCode{OmissionRetentionExpired}}
	got, err := BuildEvidence(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != SnapshotKindAvailable || got.FromMS != -800_000 || got.ToMS != 2_000_000 || len(got.FactIDs) != 6 {
		t.Fatalf("snapshot=%+v", got)
	}
	for i, id := range got.FactIDs {
		want := []string{"F001", "F002", "F003", "F004", "F005", "F006"}[i]
		if id != want {
			t.Fatalf("fact %d=%s", i, id)
		}
	}
	if !bytes.Contains(got.CanonicalJSON, []byte(`"label":"source"`)) || bytes.Contains(got.CanonicalJSON, []byte("host")) {
		t.Fatalf("invalid canonical evidence: %s", got.CanonicalJSON)
	}
}
func TestBuildEvidenceMakesUnavailableWhenRequestCannotFit(t *testing.T) {
	in := EvidenceInput{SnapshotAtMS: 1_000_000, SourceTimeMS: 1_000_000, Source: SourceRef{Kind: SourceKindEventSpike, ID: 1}, Freshness: FreshnessFresh, Drain: DrainAllowAll, Detector: DetectorConfirmed, EventSpike: &EventSpikeEvidence{Channel: "agent"}}
	got, err := BuildEvidence(in, func([]byte) int { return 16385 })
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != SnapshotKindUnavailable || string(got.CanonicalJSON) != "{}" || len(got.FactIDs) != 0 || EvidenceHash(got) != sha256.Sum256([]byte("{}")) {
		t.Fatalf("unavailable=%+v", got)
	}
}
