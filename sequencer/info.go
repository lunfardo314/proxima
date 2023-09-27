package sequencer

import (
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
)

func (seq *Sequencer) updateInfo(msOutput utangle.WrappedOutput) {
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
	if od := ParseMilestoneData(o.Output); od != nil {
		branchIndex = od.BranchHeight
		msIndex = od.ChainHeight
	}

	seq.log.Infof("%s %d/%d: %s, cov: %s<-%s, in/out: %d/%d, feeOut: %d, mem: %d/%d",
		msType,
		msIndex,
		branchIndex,
		wOut.IDShort(),
		util.GoThousands(info.LedgerCoverage),
		util.GoThousands(info.PrevLedgerCoverage),
		info.In,
		info.Out,
		info.NumConsumedFeeOutputs,
		info.NumFeeOutputsInMempool,
		info.NumOtherMsInMempool,
	)
}
