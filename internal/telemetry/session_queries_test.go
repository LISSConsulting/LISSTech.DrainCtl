//go:build windows

package telemetry

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/sessiondata"
)

func TestSessionQueriesFreshnessAndDetailPaging(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1000) }
	for _, host := range []string{"a.example.test", "b.example.test"} {
		snapshot := sessiondata.SessionSnapshot{Schema: sessiondata.SnapshotSchema, Host: host, AgentInstanceID: "01890f9d-5c00-7000-8000-000000000001", Sequence: 1, ObservedAtMS: 1, CollectorVersion: "test", LogicalCPUCount: 1, Sessions: []sessiondata.SessionRecord{{SessionID: 2, State: sessiondata.SessionUnknown}, {SessionID: 1, State: sessiondata.SessionActive}}}
		if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
			t.Fatal(err)
		}
	}
	queries, err := NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queries.Fleet(context.Background(), FleetSessionQuery{KnownHosts: []FleetKnownHost{{Host: "a.example.test"}, {Host: "b.example.test"}}, Now: time.UnixMilli(4001), HeartbeatInterval: time.Second, PageSize: 15})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || page.Items[0].Freshness != SessionFreshnessStale {
		t.Fatalf("fleet=%+v", page)
	}
	detail, err := queries.Detail(context.Background(), "a.example.test", SessionDetailQuery{Now: time.UnixMilli(1000), HeartbeatInterval: time.Second, PageSize: 15})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Total != 2 || detail.Sessions[0].SessionID != 1 || detail.Sessions[1].SessionID != 2 {
		t.Fatalf("detail ordering=%+v", detail.Sessions)
	}
}

func TestSessionQueriesRetainDistinctSuccessAndActivityTimes(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(202) }
	logon := int64(303)
	snapshot := sessiondata.SessionSnapshot{
		Schema:           sessiondata.SnapshotSchema,
		Host:             "times.example.test",
		AgentInstanceID:  "01890f9d-5c00-7000-8000-000000000002",
		Sequence:         1,
		ObservedAtMS:     101,
		CollectorVersion: "test",
		LogicalCPUCount:  1,
		Sessions:         []sessiondata.SessionRecord{{SessionID: 1, State: sessiondata.SessionActive, LogonAtMS: &logon}},
	}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	queries, err := NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := queries.Detail(context.Background(), snapshot.Host, SessionDetailQuery{Now: time.UnixMilli(202), HeartbeatInterval: time.Second, PageSize: 15})
	if err != nil {
		t.Fatal(err)
	}
	if detail.LastSuccessObservedAtMS == nil || detail.LastSuccessReceivedAtMS == nil || detail.LastActivityAtMS == nil {
		t.Fatalf("missing summary timestamps: %+v", detail.FleetSessionSummary)
	}
	if *detail.LastSuccessObservedAtMS != 101 || *detail.LastSuccessReceivedAtMS != 202 || *detail.LastActivityAtMS != 303 {
		t.Fatalf("summary timestamps = observed:%d received:%d activity:%d, want 101, 202, 303", *detail.LastSuccessObservedAtMS, *detail.LastSuccessReceivedAtMS, *detail.LastActivityAtMS)
	}
	if detail.LastSuccessObservedAtMS == detail.LastSuccessReceivedAtMS || detail.LastSuccessObservedAtMS == detail.LastActivityAtMS || detail.LastSuccessReceivedAtMS == detail.LastActivityAtMS {
		t.Fatalf("summary timestamp pointers alias: observed=%p received=%p activity=%p", detail.LastSuccessObservedAtMS, detail.LastSuccessReceivedAtMS, detail.LastActivityAtMS)
	}
}

func TestSessionFreshnessBoundaries(t *testing.T) {
	received := int64(1000)
	now := time.UnixMilli(4000)
	if got := freshness(&received, now, time.Second, "host", nil); got != SessionFreshnessFresh {
		t.Fatalf("3x boundary=%q", got)
	}
	if got := freshness(&received, time.UnixMilli(4001), time.Second, "host", nil); got != SessionFreshnessStale {
		t.Fatalf("over 3x=%q", got)
	}
	if got := freshness(&received, time.UnixMilli(11001), time.Second, "host", nil); got != SessionFreshnessOffline {
		t.Fatalf("over 10x=%q", got)
	}
	if got := freshness(nil, now, time.Second, "host", nil); got != SessionFreshnessUnknown {
		t.Fatalf("unknown=%q", got)
	}
}

func TestFleetIncludesKnownUnsupportedHostsAndProjectsRoster(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1000) }
	snapshot := sessiondata.SessionSnapshot{
		Schema: sessiondata.SnapshotSchema, Host: "supported.example.test",
		AgentInstanceID: "01890f9d-5c00-7000-8000-000000000003", Sequence: 1,
		ObservedAtMS: 1, CollectorVersion: "test", LogicalCPUCount: 1,
		Sessions: []sessiondata.SessionRecord{{SessionID: 1, State: sessiondata.SessionActive}},
	}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	snapshot.Host = "unrelated.example.test"
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{}); err != nil {
		t.Fatal(err)
	}
	queries, err := NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	page, err := queries.Fleet(context.Background(), FleetSessionQuery{
		KnownHosts: []FleetKnownHost{
			{Host: "SUPPORTED.EXAMPLE.TEST", Status: "Alert", Mode: "Drain", Collection: "Published Apps"},
			{Host: "unsupported.example.test", Status: "Healthy", Mode: "AllowAll", Collection: "Desktops"},
		},
		Now: time.UnixMilli(1000), HeartbeatInterval: time.Second, PageSize: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
	var supported, unsupported FleetSessionSummary
	for _, item := range page.Items {
		if item.Host == "supported.example.test" {
			supported = item
		} else {
			unsupported = item
		}
	}
	if supported.HostStatus != "Alert" || supported.HostMode != "Drain" || supported.Collection != "Published Apps" || supported.SessionCount == nil {
		t.Fatalf("supported projection = %+v", supported)
	}
	if unsupported.Host != "unsupported.example.test" || unsupported.HostStatus != "Healthy" || unsupported.HostMode != "AllowAll" || unsupported.Collection != "Desktops" || unsupported.Freshness != SessionFreshnessUnknown || unsupported.SessionCount != nil {
		t.Fatalf("unsupported host = %+v", unsupported)
	}
	if _, err := queries.Detail(context.Background(), unsupported.Host, SessionDetailQuery{PageSize: 15}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unsupported detail error = %v, want sql.ErrNoRows", err)
	}
}

func TestFleetSortFiltersAndPagingAreDeterministic(t *testing.T) {
	db := openTestDB(t)
	queries, err := NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	known := make([]FleetKnownHost, 0, 16)
	for i := range 16 {
		known = append(known, FleetKnownHost{Host: "host" + string(rune('a'+i)) + ".example.test", Status: "off", Mode: "aLpHa"})
	}
	page, err := queries.Fleet(context.Background(), FleetSessionQuery{KnownHosts: known, Sort: "status", Page: 2, PageSize: 15})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 16 || len(page.Items) != 1 || page.Items[0].Host != "hostp.example.test" {
		t.Fatalf("tie page = %+v", page)
	}
	items := []FleetSessionSummary{
		{Host: "a", HostStatus: "Healthy", HostMode: "beta"},
		{Host: "b", HostStatus: "Alert", HostMode: "Alpha"},
		{Host: "c", HostStatus: "Warning", HostMode: "alpha"},
		{Host: "d", HostStatus: "Grace", HostMode: "Zeta"},
		{Host: "e", HostStatus: "off", HostMode: "ALPHA"},
	}
	for _, tc := range []struct {
		sort, direction string
		want            []string
	}{
		{sort: "status", direction: "asc", want: []string{"b", "c", "d", "a", "e"}},
		{sort: "status", direction: "desc", want: []string{"e", "a", "d", "c", "b"}},
		{sort: "mode", direction: "asc", want: []string{"b", "c", "e", "a", "d"}},
		{sort: "mode", direction: "desc", want: []string{"d", "a", "b", "c", "e"}},
	} {
		t.Run(tc.sort+"/"+tc.direction, func(t *testing.T) {
			got := append([]FleetSessionSummary(nil), items...)
			sortFleet(got, tc.sort, tc.direction)
			for i, want := range tc.want {
				if got[i].Host != want {
					t.Fatalf("%s/%s order = %+v, want %v", tc.sort, tc.direction, got, tc.want)
				}
			}
		})
	}
	tied := 1
	at := int64(1)
	tiedItems := []FleetSessionSummary{
		{Host: "b", SessionCount: &tied, ActiveCount: &tied, IdleCount: &tied, DisconnectedCount: &tied, UserCount: &tied, LastActivityAtMS: &at},
		{Host: "a", SessionCount: &tied, ActiveCount: &tied, IdleCount: &tied, DisconnectedCount: &tied, UserCount: &tied, LastActivityAtMS: &at},
	}
	for _, field := range []string{"host", "sessions", "active", "idle", "disconnected", "users", "last_activity"} {
		for _, direction := range []string{"asc", "desc"} {
			got := append([]FleetSessionSummary(nil), tiedItems...)
			sortFleet(got, field, direction)
			wantFirst := "a"
			if field == "host" && direction == "desc" {
				wantFirst = "b"
			}
			if got[0].Host != wantFirst {
				t.Fatalf("%s/%s tie order = %+v", field, direction, got)
			}
		}
	}
	active, idle, disconnected := 1, 1, 1
	for state, item := range map[string]FleetSessionSummary{
		"active":       {ActiveCount: &active},
		"idle":         {IdleCount: &idle},
		"disconnected": {DisconnectedCount: &disconnected},
	} {
		if !matchesFleet(item, FleetSessionQuery{State: state}, false) {
			t.Fatalf("%s filter did not match", state)
		}
	}
	if matchesFleet(FleetSessionSummary{Host: "host"}, FleetSessionQuery{Query: "other"}, false) {
		t.Fatal("query matched absent host and session")
	}
}

func TestFleetKnownHostInputIsBounded(t *testing.T) {
	hosts := make([]FleetKnownHost, maxFleetKnownHosts+1)
	for i := range hosts {
		hosts[i].Host = "host" + string(rune('a'+i%26)) + ".example.test"
	}
	if _, err := normalizeKnownHosts(hosts); err == nil {
		t.Fatal("accepted an unbounded known-host context")
	}
}

func TestFleetSearchBindsHostsAndRespectsIdentityVisibility(t *testing.T) {
	db := openTestDB(t)
	store, err := NewSessionSnapshotStore(db)
	if err != nil {
		t.Fatal(err)
	}
	user, domain := "alice", "example"
	snapshot := sessiondata.SessionSnapshot{
		Schema: sessiondata.SnapshotSchema, Host: "matched.example.test",
		AgentInstanceID: "01890f9d-5c00-7000-8000-000000000004", Sequence: 1,
		ObservedAtMS: 1, CollectorVersion: "test", LogicalCPUCount: 1,
		Sessions: []sessiondata.SessionRecord{{SessionID: 7, State: sessiondata.SessionActive, User: &user, Domain: &domain}},
	}
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{Identity: sessiondata.VisibilityFull}); err != nil {
		t.Fatal(err)
	}
	snapshot.Host = "other.example.test"
	snapshot.AgentInstanceID = "01890f9d-5c00-7000-8000-000000000005"
	otherUser := "bob"
	snapshot.Sessions[0].User = &otherUser
	if _, err := store.Apply(context.Background(), snapshot, sessiondata.PrivacyPolicy{Identity: sessiondata.VisibilityFull}); err != nil {
		t.Fatal(err)
	}
	queries, err := NewSessionQueryStore(db)
	if err != nil {
		t.Fatal(err)
	}
	known := []FleetKnownHost{{Host: "matched.example.test"}, {Host: "other.example.test"}}
	for _, tc := range []struct {
		name, search string
		visibility   sessiondata.Visibility
		wantTotal    int
	}{
		{name: "full identity", search: "alice", visibility: sessiondata.VisibilityFull, wantTotal: 1},
		{name: "hidden identity", search: "alice", visibility: sessiondata.VisibilityHidden, wantTotal: 0},
		{name: "hostile query", search: `alice%' OR 1=1 --`, visibility: sessiondata.VisibilityFull, wantTotal: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := queries.Fleet(context.Background(), FleetSessionQuery{
				KnownHosts: known, Query: tc.search, IdentityVisibility: tc.visibility, PageSize: 15,
			})
			if err != nil {
				t.Fatal(err)
			}
			if page.Total != tc.wantTotal {
				t.Fatalf("total = %d, want %d; page = %+v", page.Total, tc.wantTotal, page)
			}
		})
	}
	if _, err := queries.Fleet(context.Background(), FleetSessionQuery{
		KnownHosts: []FleetKnownHost{{Host: "matched.example.test' OR 1=1 --"}},
		PageSize:   15,
	}); err == nil {
		t.Fatal("accepted a hostile-looking host")
	}
}

type closeErrorRows struct{ err error }

func (r closeErrorRows) Close() error { return r.err }

func TestCloseSessionRowsPropagatesCloseError(t *testing.T) {
	want := errors.New("close failed")
	if err := closeSessionRows(closeErrorRows{err: want}, "telemetry: session fleet rows"); !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
}
