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
		InflationAmount:        msOutput.VID.InflationAmount(),
		NumConsumedFeeOutputs:  nConsumed,
		NumFeeOutputsInTippool: seq.factory.tipPool.numOutputsInBuffer(),
		NumOtherMsInTippool:    seq.factory.tipPool.numOtherMilestones(),
		LedgerCoverage:         seq.factory.utangle.LedgerCoverage(msOutput.VID),
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

	seq.log.Infof("%s %d/%d: %s, bl: %s, cov: %s<-%s (infl: %s), in/out: %d/%d, feeOut: %d, proposal: %v x %d, mem: %d/%d",
		msType,
		msIndex,
		branchIndex,
		wOut.IDShort(),
		wOut.VID.BaselineBranch().IDShort(),
		util.GoThousands(info.LedgerCoverage),
		util.GoThousands(info.PrevLedgerCoverage),
		util.GoThousands(info.InflationAmount),
		info.In,
		info.Out,
		info.NumConsumedFeeOutputs,
		info.AvgProposalDuration,
		info.NumProposals,
		info.NumFeeOutputsInTippool,
		info.NumOtherMsInTippool,
	)
	const printForkSet = false
	if printForkSet {
		if msType == "MS" {
			seq.log.Infof("MS FORK SET:\n%s", wOut.VID.PastTrackLines("       ").String())
		}
	}
	const printTx = false
	if printTx {
		seq.log.Infof("=============================\n%s", wOut.VID.Lines().String())
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
