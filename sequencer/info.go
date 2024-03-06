package sequencer

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func (seq *Sequencer) updateInfo(ms *vertex.WrappedTx) {
	seq.infoMutex.Lock()
	defer seq.infoMutex.Unlock()

	seq.Assertf(ms.IsSequencerMilestone(), "msOutput.VID.IsSequencerMilestone()")

	nConsumed := ms.NumInputs() - 1
	if ms.IsBranchTransaction() {
		nConsumed -= 1
	}
	seq.info = Info{
		In:                     ms.NumInputs(),
		Out:                    ms.NumProducedOutputs(),
		InflationAmount:        ms.InflationAmount(),
		NumConsumedFeeOutputs:  nConsumed,
		NumFeeOutputsInTippool: seq.factory.NumOutputsInBuffer(),
		NumOtherMsInTippool:    seq.factory.NumMilestones(),
		LedgerCoverage:         ms.LedgerCoverageSum(),
		PrevLedgerCoverage:     seq.info.LedgerCoverage,
	}
}

func (seq *Sequencer) Info() Info {
	seq.infoMutex.RLock()
	defer seq.infoMutex.RUnlock()

	return seq.info
}

func (seq *Sequencer) LogMilestoneSubmitDefault(ms *vertex.WrappedTx) {
	info := seq.Info()
	msType := "MS"
	if ms.IsBranchTransaction() {
		msType = "BRANCH"
	}

	var sequencerOutput *ledger.OutputWithID
	ms.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			sequencerOutput = v.Tx.SequencerOutput()
		},
	})
	if sequencerOutput == nil {
		seq.log.Errorf("LogMilestoneSubmitDefault: can't unwrap milestone output %s", ms.IDShortString())
		return
	}

	var branchIndex, msIndex uint32
	if od := ledger.ParseMilestoneData(sequencerOutput.Output); od != nil {
		branchIndex = od.BranchHeight
		msIndex = od.ChainHeight
	}

	seq.log.Debugf("%s %d/%d: %s, bl: %s, cov: %s<-%s (infl: %s), in/out: %d/%d, feeOut: %d, mem: %d/%d",
		msType,
		msIndex,
		branchIndex,
		sequencerOutput.IDShort(),
		ms.BaselineBranch().IDShortString(),
		util.GoTh(info.LedgerCoverage),
		util.GoTh(info.PrevLedgerCoverage),
		util.GoTh(info.InflationAmount),
		info.In,
		info.Out,
		info.NumConsumedFeeOutputs,
		info.NumFeeOutputsInTippool,
		info.NumOtherMsInTippool,
	)
	const printTx = false
	if printTx {
		seq.log.Infof("=============================\n%s", ms.Lines().String())
	}
}

//func (seq *Sequencer) LogStats() {
//	stats := seq.factory.getStatsAndReset()
//
//	seq.log.Debugf("milestones (count: %d, cached %d, removed since reset: %d), outputs: (count: %d, pool: %d, removed: %d), sequencers: %d",
//		stats.ownMilestoneCount, stats.numOwnMilestones, stats.removedMilestonesSinceReset,
//		stats.tipPoolStats.outputCount, stats.tipPoolStats.numOutputs, stats.tipPoolStats.removedOutputsSinceReset,
//		stats.numOtherSequencers,
//	)
//}
