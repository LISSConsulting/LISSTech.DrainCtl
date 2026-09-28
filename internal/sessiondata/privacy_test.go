package sessiondata

import "testing"

func TestPrivacyProjectionExactMasks(t *testing.T) {
	user, domain, station, client, address := "alex", "CONTOSO", "rdp-tcp#4", "WS-17", "192.0.2.17"
	source := SessionRecord{User: &user, Domain: &domain, Station: &station, ClientName: &client, ClientAddress: &address, Processes: []SessionProcess{{PID: 7, ImageName: "secret.exe", WorkingSetBytes: 9}}}
	masked := ProjectSession(source, PrivacyPolicy{Identity: VisibilityMasked, Client: VisibilityMasked, Process: VisibilityMasked})
	for _, value := range []*string{masked.User, masked.Domain, masked.Station, masked.ClientName, masked.ClientAddress} {
		if value == nil || *value != "***" {
			t.Fatalf("masked value = %#v", value)
		}
	}
	if got := masked.Processes[0]; got.ImageName != "***" || got.PID != 7 || got.WorkingSetBytes != 9 {
		t.Fatalf("masked process = %#v", got)
	}
	if source.Processes[0].ImageName != "secret.exe" {
		t.Fatal("projection mutated source")
	}
}

func TestPrivacyProjectionHiddenRemovesValues(t *testing.T) {
	user, domain, client := "alex", "CONTOSO", "WS-17"
	source := SessionRecord{User: &user, Domain: &domain, ClientName: &client, Processes: []SessionProcess{{PID: 7, ImageName: "secret.exe"}}}
	hidden := ProjectSession(source, PrivacyPolicy{Identity: VisibilityHidden, Client: VisibilityHidden, Process: VisibilityHidden})
	if hidden.User != nil || hidden.Domain != nil || hidden.ClientName != nil || len(hidden.Processes) != 0 {
		t.Fatalf("hidden projection retained data: %#v", hidden)
	}
	if CalculateAggregates([]SessionRecord{hidden}).UserCount != 0 {
		t.Fatal("hidden identity contributed to aggregate")
	}
}

func TestPrivacyProjectionPreservesAbsentPrivateValues(t *testing.T) {
	projected := ProjectSession(SessionRecord{}, PrivacyPolicy{Identity: VisibilityMasked, Client: VisibilityMasked, Process: VisibilityMasked})
	if projected.User != nil || projected.ClientName != nil || len(projected.Processes) != 0 {
		t.Fatalf("projection invented private values: %#v", projected)
	}
}
