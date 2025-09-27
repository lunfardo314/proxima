package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

const (
	ownMilestoneCleanupPeriod     = time.Second
	ownMilestoneMapRecreatePeriod = time.Minute
)

func (seq *Sequencer) FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs base.LedgerTime) []vertex.WrappedOutput {
	seq.ownMilestonesMutex.RLock()
	defer seq.ownMilestonesMutex.RUnlock()

	_, ok := seq.ownMilestones[rootOutput.VID]
	seq.Assertf(ok, "FutureConeOwnMilestonesOrdered: milestone output %s of chain %s is expected to be among set of own milestones (%d)",
		rootOutput.IDStringShort, seq.sequencerID.StringShort, len(seq.ownMilestones))

	ordered := util.KeysSorted(seq.ownMilestones, func(vid1, vid2 *vertex.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*vertex.WrappedTx](rootOutput.VID)
	ret := []vertex.WrappedOutput{rootOutput}
	for _, vid := range ordered {
		if vid.IsBad() || !vid.IsSequencerMilestone() || !ledger.ValidTransactionPace(vid.Timestamp(), targetTs) {
			continue
		}
		pred := vid.SequencerPredecessor(func(txid base.TransactionID) *vertex.WrappedTx {
			return attacher.AttachTxID(txid, seq, attacher.WithInvokedBy("FutureConeOwnMilestonesOrdered"))
		})
		if !visited.Contains(pred) {
			continue
		}
		visited.Insert(vid)
		seqOut := vid.SequencerWrappedOutput()
		_, ok = seqOut.OutputWithChainID()
		util.Assertf(ok, "not a chain output:\nid=%s\n%s", seqOut.IDStringShort(), seqOut.OutputWithID().LinesHR("    ").String())
		ret = append(ret, seqOut)
	}
	return ret
}

func (seq *Sequencer) IsConsumedInThePastPath(oid base.OutputID, ms *vertex.WrappedTx, getStateReader func() multistate.SugaredStateReader) bool {
	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	if seq.ownMilestones[ms].consumed.Contains(oid) {
		return true
	}
	ret := getStateReader().OutputIsConsumed(oid)
	if ret {
		seq.ownMilestones[ms].consumed.Insert(oid)
	}
	return ret
}

func (seq *Sequencer) OwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := seq.GetLatestMilestone(seq.sequencerID)
	if ret != nil {
		seq.AddOwnMilestone(ret)
		chainOut := ret.FindChainOutput(&seq.sequencerID)
		if chainOut.Output == nil {
			return vertex.WrappedOutput{}
		}
		return attacher.AttachOutputWithID(*chainOut, seq, attacher.WithInvokedBy("OwnLatestMilestoneOutput"))
	}
	// there's no own milestone in the tippool, find in one of the baseline states of other sequencers or in LRB
	return seq.bootstrapOwnMilestoneOutput()
}

// _collectConsumed collects a set of output IDs consumed along the past chain of the milestone contained in the cache
// Cannot collect consumed outputs behind branches
func (seq *Sequencer) _collectConsumed(ms *vertex.WrappedTx) set.Set[base.OutputID] {
	ret := set.New[base.OutputID]()

	for ms != nil {
		var msPred *vertex.WrappedTx

		ms.RUnwrap(vertex.UnwrapOptions{
			Vertex: func(v *vertex.Vertex) {
				v.Tx.ForEachInput(func(i byte, oid base.OutputID) bool {
					ret.Insert(oid)
					return true
				})
				if seqData := v.Tx.SequencerTransactionData(); seqData != nil {
					// continue along own predecessors in the cache
					msPred = v.Inputs[seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex]
					if _, predIsOwnMilestone := seq.ownMilestones[msPred]; !predIsOwnMilestone {
						msPred = nil
					}
				}
			},
		})
		ms = msPred
	}
	return ret
}

// AddOwnMilestone adds new milestone to the cash of own milestones
func (seq *Sequencer) AddOwnMilestone(vid *vertex.WrappedTx) {
	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	if _, already := seq.ownMilestones[vid]; already {
		return
	}
	seq.ownMilestones[vid] = outputsWithTime{
		consumed: seq._collectConsumed(vid),
		since:    time.Now(),
	}
	if seq.metrics != nil {
		seq.metrics.ownMilestones.Set(float64(len(seq.ownMilestones)))
	}
}

func (seq *Sequencer) purgeOwnMilestones(ttl time.Duration) (int, int) {
	horizon := time.Now().Add(-ttl)

	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	count := 0
	for vid, withTime := range seq.ownMilestones {
		if withTime.since.Before(horizon) {
			delete(seq.ownMilestones, vid)
			count++
		}
	}

	return count, len(seq.ownMilestones)
}

func (seq *Sequencer) recreateMapOwnMilestones() {
	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	seq.ownMilestones = maps.Clone(seq.ownMilestones)
}
