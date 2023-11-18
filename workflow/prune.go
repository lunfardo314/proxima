package workflow

import (
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle"
)

func (w *Workflow) startPruner() {
	w.log.Info("STARTING pruner..")
	go w.pruneOrphanedLoop()
	go w.cutFinalLoop()
}

func (w *Workflow) pruneOrphanedLoop() {
	PruneOrphanedPeriod := core.TimeSlotDuration()

	for w.working.Load() {
		time.Sleep(1 * time.Second)

		if w.utxoTangle.SinceLastPrunedOrphaned() < PruneOrphanedPeriod {
			continue
		}

		startTime := time.Now()
		nVertices := w.utxoTangle.NumVertices()
		nOrphaned, nOrphanedBranches, nDeletedSlots := w.utxoTangle.PruneOrphaned(utangle.TipSlots)

		w.utxoTangle.SetLastPrunedOrphaned(time.Now())

		var mstats runtime.MemStats
		runtime.ReadMemStats(&mstats)

		w.log.Infof("SLOT %d. Pruned %d orphaned transactions and %d branches out of total %d vertices in %v, deleted slots: %d. Alloc (gortn): %.1f MB (%d)",
			core.LogicalTimeNow().TimeSlot(), nOrphaned, nOrphanedBranches, nVertices, time.Since(startTime), nDeletedSlots,
			float32(mstats.Alloc*10/(1024*1024))/10,
			runtime.NumGoroutine())
	}
	w.log.Infof("Prunner loop stopped")
}

func (w *Workflow) cutFinalLoop() {
	CutFinalPeriod := core.TimeSlotDuration() / 2

	for w.working.Load() {
		time.Sleep(1 * time.Second)

		if w.utxoTangle.SinceLastCutFinal() < CutFinalPeriod {
			continue
		}

		if txID, numTx := w.utxoTangle.CutFinalBranchIfExists(utangle.TipSlots); txID != nil {
			w.log.Infof("CUT FINAL BRANCH %s, num tx: %d", txID.StringShort(), numTx)
			w.utxoTangle.SetLastCutFinal(time.Now())
		}
	}
	w.log.Infof("Branch cutter loop stopped")
}
