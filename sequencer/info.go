package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func (seq *Sequencer) updateInfo(msOutput utangle.WrappedOutput, avgProposalDuration time.Duration, numProposals int) {
	seq.infoMutex.Lock()
	defer seq.infoMutex.Unlock()

	util.Assertf(msOutput.VID.IsSequencerMilestone(), "msOutput.VID.IsSequencerMilestone()")

	nConsumed := msOutput.VID.NumInputs() - 1
	if msOutput.VID.IsBranchTransaction() {
		nConsumed -= 1
	}
	seq.info = Info{
		In:                     msOutput.VID.NumInputs(),
		Out:                    msOutput.VID.NumProducedOutputs(),
		NumConsumedFeeOutputs:  nConsumed,
		NumFeeOutputsInTippool: seq.factory.tipPool.numOutputsInBuffer(),
		NumOtherMsInTippool:    seq.factory.tipPool.numOtherMilestones(),
		LedgerCoverage:         msOutput.VID.LedgerCoverage(seq.factory.tangle.StateStore),
		PrevLedgerCoverage:     seq.info.LedgerCoverage,
		AvgProposalDuration:    avgProposalDuration,
		NumProposals:           numProposals,
	}
}

func (seq *Sequencer) Info() Info {
	seq.infoMutex.RLock()
	defer seq.infoMutex.RUnlock()

	return seq.info
}

func (seq *Sequencer) LogMilestoneSubmitDefault(wOut *utangle.WrappedOutput) {
	info := seq.Info()
	msType := "MS"
	if wOut.VID.IsBranchTransaction() {
		msType = "BRANCH"
	}

	o, err := wOut.Unwrap()
	if err != nil {
		seq.log.Errorf("LogMilestoneSubmitDefault: can't unwrap milestone output %s", wOut.IDShort())
		return
	}

	var branchIndex, msIndex uint32
	if od := txbuilder.ParseMilestoneData(o.Output); od != nil {
		branchIndex = od.BranchHeight
		msIndex = od.ChainHeight
	}

	seq.log.Infof("%s %d/%d: %s, bl: %s, cov: %s<-%s, in/out: %d/%d, feeOut: %d, proposal: %v x %d, mem: %d/%d",
		msType,
		msIndex,
		branchIndex,
		wOut.IDShort(),
		wOut.VID.BaselineBranchID().Short(),
		util.GoThousands(info.LedgerCoverage),
		util.GoThousands(info.PrevLedgerCoverage),
		info.In,
		info.Out,
		info.NumConsumedFeeOutputs,
		info.AvgProposalDuration,
		info.NumProposals,
		info.NumFeeOutputsInTippool,
		info.NumOtherMsInTippool,
	)
	if msType == "MS" {
		seq.log.Infof("MS DELTA:\n%s", wOut.VID.GetUTXOStateDelta().Lines("     ").String())
	}
}

func (seq *Sequencer) LogStats() {
	stats := seq.factory.getStatsAndReset()

	seq.log.Infof("milestones (count: %d, cached %d, removed: %d), outputs: (count: %d, pool: %d, removed: %d), sequencers: %d",
		stats.ownMilestoneCount, stats.numOwnMilestones, stats.removedMilestonesSinceReset,
		stats.tipPoolStats.outputCount, stats.tipPoolStats.numOutputs, stats.tipPoolStats.removedOutputsSinceReset,
		stats.numOtherSequencers,
	)
}
