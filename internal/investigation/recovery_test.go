//go:build windows

package investigation

import (
	"context"
	"testing"
	"time"
)

type recoveryTestStorage struct {
	controllerTestStorage
	recovered int64
	nowMS     int64
}

func (s *recoveryTestStorage) RecoverRunning(_ context.Context, nowMS int64) (int64, error) {
	s.nowMS = nowMS
	return s.recovered, nil
}

type recoveryTestClient struct{ calls int }

func (c *recoveryTestClient) Send(context.Context, SendConfiguration, []byte, []string, []string, SendAuthorization) (ProviderOutcome, error) {
	c.calls++
	return ProviderOutcome{}, nil
}

func TestWorkerRecoverDoesNotSendOrReconstructWork(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	store := &recoveryTestStorage{recovered: 2}
	client := &recoveryTestClient{}
	worker := &Worker{Store: store, Client: client, Now: func() time.Time { return now }}

	recovered, err := worker.Recover(context.Background())
	if err != nil || recovered != 2 || store.nowMS != now.UnixMilli() {
		t.Fatalf("Recover = (%d, %v), now=%d", recovered, err, store.nowMS)
	}
	if handled, err := worker.RunOne(context.Background()); err != nil || handled {
		t.Fatalf("RunOne after recovery = (%v, %v), want no work", handled, err)
	}
	if client.calls != 0 {
		t.Fatalf("provider sends after recovery = %d, want 0", client.calls)
	}
}
