package sequencer

import (
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
)

func (seq *Sequencer) updateInfo(ms *vertex.WrappedTx) {
	seq.infoMutex.Lock()
	defer seq.infoMutex.Unlock()

	seq.Assertf(ms.IsSequencerTransaction(), "msOutput.VID.IsSequencerTransaction()")

	nConsumed := ms.NumInputs() - 1
	if ms.IsBranchTransaction() {
		nConsumed -= 1
	}
	seq.info = Info{
		In:                     ms.NumInputs(),
		Out:                    ms.NumProducedOutputs(),
		InflationAmount:        ms.InflationAmount(),
		NumConsumedFeeOutputs:  nConsumed,
		NumFeeOutputsInTippool: seq.NumOutputsInBuffer(),
		NumOtherMsInTippool:    seq.NumMilestones(),
		LedgerCoverage:         ms.GetLedgerCoverage(),
		PrevLedgerCoverage:     seq.info.LedgerCoverage,
	}
}

func (seq *Sequencer) Info() Info {
	seq.infoMutex.RLock()
	defer seq.infoMutex.RUnlock()

	return seq.info
}

func (seq *Sequencer) LedgerCoverage() uint64 {
	return seq.Info().LedgerCoverage
}

// ConsensusContribution returns this sequencer's current consensus mass:
// tokenBalance + frozenCoverage[0] of its own latest milestone chain output.
// Returns 0 when no own milestone is known yet (or it can't be unwrapped).
// Used by the network-mapping overlay (see peering/network_connectivity.md).
func (seq *Sequencer) ConsensusContribution() uint64 {
	ms := seq.GetLatestMilestone(seq.sequencerID)
	if ms == nil {
		return 0
	}
	var ret uint64
	ms.Unwrap(vertex.UnwrapOptions{
		Vertex: func(v *vertex.Vertex) {
			if so := v.SequencerOutput(); so != nil {
				a := so.Output.Amounts()
				ret = a.TokenBalance() + uint64(a.FrozenCoverageAt(0))
			}
		},
	})
	return ret
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
			sequencerOutput = v.SequencerOutput()
		},
	})
	if sequencerOutput == nil {
		seq.log.Errorf("LogMilestoneSubmitDefault: can't unwrap milestone output %s", ms.IDShortString())
		return
	}

	var branchCounter uint32
	var txCounter uint64
	if cc := sequencerOutput.Output.ChainConstraint(); cc != nil {
		txCounter = cc.TransitionCounter
		branchCounter = cc.BranchCounter
	}

	bl, ok := ms.BaselineBranch()
	seq.Assertf(ok, "LogMilestoneSubmitDefault: can't unwrap baseline branch for milestone %s", ms.IDShortString())
	seq.log.Debugf("%s %d/%d: %s, bl: %s, cov: %s<-%s (infl: %s), in/out: %d/%d, feeOut: %d, mem: %d/%d",
		msType,
		txCounter,
		branchCounter,
		sequencerOutput.IDShort(),
		bl.StringShort(),
		util.Th(info.LedgerCoverage),
		util.Th(info.PrevLedgerCoverage),
		util.Th(info.InflationAmount),
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
