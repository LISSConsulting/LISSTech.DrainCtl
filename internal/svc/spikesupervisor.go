//go:build windows

package svc

import (
	"log/slog"
	"reflect"

	dc "github.com/LISSConsulting/LISSTech.DrainCtl"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/evtspike"
)

// applyEvtSpikeConfigReload calls sub.Reload when the EvtSpike block has
// changed between loads. Execute constructs and Start()s the subsystem
// unconditionally, so an Enabled toggle rides through Reload's Enabled-diff
// branch rather than a nil-sub shortcut. Equality is reflect.DeepEqual so the
// helper accepts two whole EvtSpikeConfig snapshots — both scalar and
// channel-list mutations are detected by one compare.
//
// Split out from Execute's configCh branch so the T068 integration test can
// drive the exact code path Execute wires in, without a runnable SCM.
func applyEvtSpikeConfigReload(sub *evtspike.Subsystem, oldCfg, newCfg dc.EvtSpikeConfig) error {
	if reflect.DeepEqual(oldCfg, newCfg) {
		return nil
	}
	slog.Info("config=reloaded-evtspike")
	return sub.Reload(newCfg)
}
