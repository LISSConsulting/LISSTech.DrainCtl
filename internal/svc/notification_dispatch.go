//go:build windows

package svc

import (
	"log/slog"
	"slices"
	"sync"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
)

// One delivery worker preserves trigger order and bounds pending work while
// external endpoints are slow. The state lock keeps pruning serialized too.
var notificationBatches = make(chan notificationBatch, 16)
var notificationWorkerOnce sync.Once

type notificationBatch struct {
	targets    []dc.NotificationTarget
	exclusions []string
	state      *dc.NotifyState
	result     dc.CheckResult
	trigger    dc.Trigger
	changedBy  string
}

func startNotificationWorker() {
	go func() {
		for batch := range notificationBatches {
			batch.state.DispatchMu.Lock()
			dc.SendNotificationWithExclusions(batch.targets, batch.exclusions, batch.state, &batch.result, batch.trigger, batch.changedBy)
			batch.state.DispatchMu.Unlock()
		}
	}()
}

func sendAsyncNotification(targets []dc.NotificationTarget, exclusions []string, state *dc.NotifyState, result *dc.CheckResult, trigger dc.Trigger, changedBy string) {
	if result == nil || len(targets) == 0 {
		return
	}
	notificationWorkerOnce.Do(startNotificationWorker)
	batch := notificationBatch{targets: slices.Clone(targets), exclusions: slices.Clone(exclusions), state: state, result: *result, trigger: trigger, changedBy: changedBy}
	select {
	case notificationBatches <- batch:
	default:
		slog.Warn("notification batch skipped: delivery capacity reached", "trigger", trigger)
	}
}
