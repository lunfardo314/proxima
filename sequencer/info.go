package sequencer

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func (seq *Sequencer) updateInfo(msOutput utangle.WrappedOutput) {
	seq.infoMutex.Lock()
	defer seq.infoMutex.Unlock()

	msCounter := seq.info.MsCounter
	branchCounter := seq.info.BranchCounter
	util.Assertf(msOutput.VID.IsSequencerMilestone(), "msOutput.VID.IsSequencerMilestone()")
	msCounter++
	if msOutput.VID.IsBranchTransaction() {
		branchCounter++
	}

	nConsumed := msOutput.VID.NumInputs() - 1
	if msOutput.VID.IsBranchTransaction() {
		nConsumed -= 1
	}
	seq.info = Info{
		MsCounter:              msCounter,
		BranchCounter:          branchCounter,
		In:                     msOutput.VID.NumInputs(),
		Out:                    msOutput.VID.NumProducedOutputs(),
		NumConsumedFeeOutputs:  nConsumed,
		NumFeeOutputsInMempool: seq.factory.tipPool.numOutputsInBuffer(),
		NumOtherMsInMempool:    seq.factory.tipPool.numOtherMilestones(),
		LedgerCoverage:         msOutput.VID.LedgerCoverage(utangle.TipSlots),
		PrevLedgerCoverage:     seq.info.LedgerCoverage,
	}
}

func (seq *Sequencer) Info() Info {
	seq.infoMutex.RLock()
	defer seq.infoMutex.RUnlock()

	return seq.info
}

func (seq *Sequencer) LogMilestoneSubmitDefault(vid *utangle.WrappedTx) {
	info := seq.Info()
	msType := "MS"
	if vid.IsBranchTransaction() {
		msType = "BRANCH"
	}
	seq.log.Infof("%s #%d(%d): %s, cov: %s<-%s, in/out: %d/%d, feeOut: %d, mem: %d/%d",
		msType,
		info.MsCounter,
		info.BranchCounter,
		vid.IDShort(),
		util.GoThousands(info.LedgerCoverage),
		util.GoThousands(info.PrevLedgerCoverage),
		info.In,
		info.Out,
		info.NumConsumedFeeOutputs,
		info.NumFeeOutputsInMempool,
		info.NumOtherMsInMempool,
	)
}
