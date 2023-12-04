package workflow

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle"
	"go.uber.org/zap"
)

func (w *Workflow) startPruner() {
	prunerLog := global.NewLogger("[prune]", w.configParams.logLevel, w.configParams.logOutput, "")
	prunerLog.Info("STARTING..")
	go w.pruneOrphanedLoop(prunerLog)
	go w.cutFinalLoop(prunerLog)
}

func (w *Workflow) pruneOrphanedLoop(log *zap.SugaredLogger) {
	PruneOrphanedPeriod := core.TimeSlotDuration()

	for w.working.Load() {
		time.Sleep(1 * time.Second)

		if w.utxoTangle.SyncStatus().SinceLastPrunedOrphaned() < PruneOrphanedPeriod {
			continue
		}

		startTime := time.Now()
		nVertices := w.utxoTangle.NumVertices()
		nOrphaned, nOrphanedBranches, nDeletedSlots := w.utxoTangle.PruneOrphaned(utangle.TipSlots)

		w.utxoTangle.SyncStatus().SetLastPrunedOrphaned(time.Now())

		log.Infof("SLOT %d. Pruned %d orphaned transactions and %d branches out of total %d vertices in %v, deleted slots: %d",
			core.LogicalTimeNow().TimeSlot(), nOrphaned, nOrphanedBranches, nVertices, time.Since(startTime), nDeletedSlots)
	}
	log.Infof("Prune loop stopped")
}

func (w *Workflow) cutFinalLoop(log *zap.SugaredLogger) {
	CutFinalPeriod := core.TimeSlotDuration() / 2

	for w.working.Load() {
		time.Sleep(1 * time.Second)

		if w.utxoTangle.SyncStatus().SinceLastCutFinal() < CutFinalPeriod {
			continue
		}

		if txID, numTx := w.utxoTangle.CutFinalBranchIfExists(utangle.TipSlots); txID != nil {
			log.Infof("CUT FINAL BRANCH %s, num tx: %d", txID.StringShort(), numTx)
			w.utxoTangle.SyncStatus().SetLastCutFinal(time.Now())
		}
	}
	log.Infof("Branch cutter loop stopped")
}
