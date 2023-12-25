package workflow

import (
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/utangle_old"
	"go.uber.org/zap"
)

func (w *Workflow) startPruner() {
	prunerLog := global.NewLogger("[prune]", w.configParams.logLevel, w.configParams.logOutput, "")
	prunerLog.Info("STARTING..")
	go w.pruneOrphanedLoop(prunerLog)
	go w.cutFinalLoop(prunerLog)
}

const (
	pruneOrphanedLoopPeriod = time.Second
	cutFinalLoopPeriod      = time.Second
)

func (w *Workflow) pruneOrphanedLoop(log *zap.SugaredLogger) {
	PruneOrphanedPeriod := core.TimeSlotDuration()

	syncStatus := w.utxoTangle.SyncData()
	for w.working.Load() {
		time.Sleep(pruneOrphanedLoopPeriod)

		if w.utxoTangle.SyncData().SinceLastPrunedOrphaned() < PruneOrphanedPeriod {
			continue
		}
		if !syncStatus.IsSynced() {
			continue
		}
		startTime := time.Now()
		nVertices := w.utxoTangle.NumVertices()
		nOrphaned, nOrphanedBranches, nDeletedSlots := w.utxoTangle.PruneOrphaned(utangle_old.TipSlots)

		w.utxoTangle.SyncData().SetLastPrunedOrphaned(time.Now())

		log.Infof("SLOT %d. Pruned %d orphaned transactions and %d branches out of total %d dag in %v, deleted slots: %d",
			core.LogicalTimeNow().TimeSlot(), nOrphaned, nOrphanedBranches, nVertices, time.Since(startTime), nDeletedSlots)
	}
	log.Infof("Prune loop stopped")
}

func (w *Workflow) cutFinalLoop(log *zap.SugaredLogger) {
	CutFinalPeriod := core.TimeSlotDuration() / 2

	syncStatus := w.utxoTangle.SyncData()
	for w.working.Load() {
		time.Sleep(cutFinalLoopPeriod)

		if w.utxoTangle.SyncData().SinceLastCutFinal() < CutFinalPeriod {
			continue
		}
		if !syncStatus.IsSynced() {
			continue
		}

		if txID, numTx := w.utxoTangle.CutFinalBranchIfExists(utangle_old.TipSlots); txID != nil {
			log.Infof("CUT FINAL BRANCH %s, num tx: %d", txID.StringShort(), numTx)
			w.utxoTangle.SyncData().SetLastCutFinal(time.Now())
		}
	}
	log.Infof("Branch cutter loop stopped")
}
