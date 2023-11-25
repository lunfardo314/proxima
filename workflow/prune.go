package workflow

import (
	"runtime"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/utangle"
	"go.uber.org/zap"
)

func (w *Workflow) startPruner() {
	prunnerLog := general.NewLogger("[prune]", w.configParams.logLevel, w.configParams.logOutput, "")
	prunnerLog.Info("STARTING..")
	go w.pruneOrphanedLoop(prunnerLog)
	go w.cutFinalLoop(prunnerLog)
}

func (w *Workflow) pruneOrphanedLoop(log *zap.SugaredLogger) {
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

		log.Infof("SLOT %d. Pruned %d orphaned transactions and %d branches out of total %d vertices in %v, deleted slots: %d. Alloc (gortn): %.1f MB (%d)",
			core.LogicalTimeNow().TimeSlot(), nOrphaned, nOrphanedBranches, nVertices, time.Since(startTime), nDeletedSlots,
			float32(mstats.Alloc*10/(1024*1024))/10,
			runtime.NumGoroutine())
	}
	log.Infof("Prune loop stopped")
}

func (w *Workflow) cutFinalLoop(log *zap.SugaredLogger) {
	CutFinalPeriod := core.TimeSlotDuration() / 2

	for w.working.Load() {
		time.Sleep(1 * time.Second)

		if w.utxoTangle.SinceLastCutFinal() < CutFinalPeriod {
			continue
		}

		if txID, numTx := w.utxoTangle.CutFinalBranchIfExists(utangle.TipSlots); txID != nil {
			log.Infof("CUT FINAL BRANCH %s, num tx: %d", txID.StringShort(), numTx)
			w.utxoTangle.SetLastCutFinal(time.Now())
		}
	}
	log.Infof("Branch cutter loop stopped")
}
