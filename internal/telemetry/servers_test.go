//go:build windows

package telemetry

import (
	"context"
	"testing"
	"time"
)

func newServerStore(t *testing.T) (*ServerStore, *DB) {
	t.Helper()
	db := openTestDB(t)
	return NewServerStore(db), db
}

func TestServers_RegisterIsIdempotent(t *testing.T) {
	s, _ := newServerStore(t)

	if err := s.Register(context.Background(), "SRV01"); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	before, err := s.Get(context.Background(), "SRV01")
	if err != nil || before == nil {
		t.Fatalf("Get after first Register: err=%v info=%v", err, before)
	}

	// Second Register should be a no-op — registered_at must be preserved.
	time.Sleep(10 * time.Millisecond)
	if err := s.Register(context.Background(), "SRV01"); err != nil {
		t.Fatalf("second Register: %v", err)
	}
	after, err := s.Get(context.Background(), "SRV01")
	if err != nil || after == nil {
		t.Fatalf("Get after second Register: err=%v info=%v", err, after)
	}
	if !before.RegisteredAt.Equal(after.RegisteredAt) {
		t.Errorf("RegisteredAt changed: before=%v after=%v (re-register must preserve)",
			before.RegisteredAt, after.RegisteredAt)
	}
}

func TestServers_IsRegistered(t *testing.T) {
	s, _ := newServerStore(t)

	ok, err := s.IsRegistered(context.Background(), "GHOST")
	if err != nil {
		t.Fatalf("IsRegistered: %v", err)
	}
	if ok {
		t.Error("unregistered host reported as registered")
	}

	_ = s.Register(context.Background(), "SRV01")
	ok, err = s.IsRegistered(context.Background(), "SRV01")
	if err != nil || !ok {
		t.Errorf("SRV01 IsRegistered = %v, %v; want true, nil", ok, err)
	}
}

func TestServers_UpdateRequiresRegistration(t *testing.T) {
	s, _ := newServerStore(t)

	updated, err := s.Update(context.Background(), "GHOST", `{"state":1}`)
	if err != nil {
		t.Fatalf("Update ghost: %v", err)
	}
	if updated {
		t.Error("Update on unregistered host returned updated=true")
	}

	_ = s.Register(context.Background(), "SRV01")
	updated, err = s.Update(context.Background(), "SRV01", `{"state":1}`)
	if err != nil || !updated {
		t.Fatalf("Update SRV01 = %v, %v; want true, nil", updated, err)
	}

	info, err := s.Get(context.Background(), "SRV01")
	if err != nil || info == nil {
		t.Fatalf("Get: err=%v info=%v", err, info)
	}
	if info.LastResultJSON != `{"state":1}` {
		t.Errorf("LastResultJSON = %q, want %q", info.LastResultJSON, `{"state":1}`)
	}
	if info.LastSeen.IsZero() {
		t.Error("LastSeen still zero after Update")
	}
}

func TestServers_Remove(t *testing.T) {
	s, _ := newServerStore(t)

	found, err := s.Remove(context.Background(), "GHOST")
	if err != nil {
		t.Fatalf("Remove ghost: %v", err)
	}
	if found {
		t.Error("Remove of unregistered host returned found=true")
	}

	_ = s.Register(context.Background(), "SRV01")
	found, err = s.Remove(context.Background(), "SRV01")
	if err != nil || !found {
		t.Errorf("Remove SRV01 = %v, %v; want true, nil", found, err)
	}

	ok, _ := s.IsRegistered(context.Background(), "SRV01")
	if ok {
		t.Error("SRV01 still registered after Remove")
	}
}

func TestServers_AllSortedByHostname(t *testing.T) {
	s, _ := newServerStore(t)

	for _, h := range []string{"CHARLIE", "ALPHA", "BRAVO"} {
		_ = s.Register(context.Background(), h)
	}

	list, err := s.All(context.Background())
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	want := []string{"ALPHA", "BRAVO", "CHARLIE"}
	if len(list) != len(want) {
		t.Fatalf("len = %d, want %d", len(list), len(want))
	}
	for i := range want {
		if list[i].Hostname != want[i] {
			t.Errorf("list[%d].Hostname = %q, want %q", i, list[i].Hostname, want[i])
		}
	}
}

func TestServers_GetUnknownReturnsNil(t *testing.T) {
	s, _ := newServerStore(t)

	info, err := s.Get(context.Background(), "GHOST")
	if err != nil {
		t.Fatalf("Get ghost: %v", err)
	}
	if info != nil {
		t.Errorf("Get of unregistered host = %+v, want nil", info)
	}
}

func TestServers_Import(t *testing.T) {
	s, _ := newServerStore(t)

	now := time.Now().UTC().Truncate(time.Millisecond)
	infos := []ServerInfo{
		{Hostname: "ALPHA", RegisteredAt: now.Add(-24 * time.Hour), LastSeen: now.Add(-time.Minute), LastResultJSON: `{"state":1}`},
		{Hostname: "BRAVO", RegisteredAt: now.Add(-48 * time.Hour), LastSeen: time.Time{}, LastResultJSON: ""},
	}
	n, err := s.Import(context.Background(), infos)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if n != 2 {
		t.Errorf("Import inserted=%d, want 2", n)
	}

	alpha, _ := s.Get(context.Background(), "ALPHA")
	if alpha == nil {
		t.Fatal("ALPHA missing after Import")
	}
	if alpha.LastResultJSON != `{"state":1}` {
		t.Errorf("ALPHA LastResultJSON = %q", alpha.LastResultJSON)
	}
	if alpha.LastSeen.IsZero() {
		t.Error("ALPHA LastSeen zero after Import (expected non-zero)")
	}

	bravo, _ := s.Get(context.Background(), "BRAVO")
	if bravo == nil {
		t.Fatal("BRAVO missing after Import")
	}
	if bravo.LastResultJSON != "" {
		t.Errorf("BRAVO LastResultJSON = %q, want empty", bravo.LastResultJSON)
	}
	if !bravo.LastSeen.IsZero() {
		t.Errorf("BRAVO LastSeen = %v, want zero", bravo.LastSeen)
	}

	// Import is idempotent — re-running imports 0 new rows.
	n, err = s.Import(context.Background(), infos)
	if err != nil {
		t.Fatalf("re-Import: %v", err)
	}
	if n != 0 {
		t.Errorf("re-Import inserted=%d, want 0 (idempotent)", n)
	}
}
