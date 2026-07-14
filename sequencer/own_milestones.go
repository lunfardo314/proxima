package sequencer

import (
	"sort"
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
		if vid.IsBad() || !vid.IsSequencerTransaction() || !ledger.ValidTransactionPace(vid.Timestamp(), targetTs) {
			continue
		}
		pred := vid.SequencerPredecessor(func(txid base.TransactionID) *vertex.WrappedTx {
			return attacher.AttachTxID(txid, seq, attacher.WithInvokedBy("FutureConeOwnMilestonesOrdered"))
		})
		if !visited.Contains(pred) {
			continue
		}
		visited.Insert(vid)
		ret = append(ret, vid.SequencerWrappedOutput())
	}
	return ret
}

// OwnMilestoneOutputsInMemDAGDescending returns the sequencer's own milestone chain outputs
// currently tracked in the memDAG, newest timestamp first. It is the memDAG-first extend-candidate
// set for the factory: extend candidates are tried from here before touching branch state. Spent
// or stale candidates are harmless — the incremental attacher rejects a double-spend as a conflict.
func (seq *Sequencer) OwnMilestoneOutputsInMemDAGDescending() []vertex.WrappedOutput {
	seq.ownMilestonesMutex.RLock()
	vids := make([]*vertex.WrappedTx, 0, len(seq.ownMilestones))
	for vid := range seq.ownMilestones {
		vids = append(vids, vid)
	}
	seq.ownMilestonesMutex.RUnlock()

	sort.Slice(vids, func(i, j int) bool {
		return vids[j].Timestamp().Before(vids[i].Timestamp()) // newest first
	})
	ret := make([]vertex.WrappedOutput, 0, len(vids))
	for _, vid := range vids {
		if vid.IsBad() || !vid.IsSequencerTransaction() {
			continue
		}
		if wOut := vid.SequencerWrappedOutput(); wOut.VID != nil {
			ret = append(ret, wOut)
		}
	}
	return ret
}

// IsConsumedInThePastPath checks if an output is consumed in the past chain of the given milestone.
// Uses a two-phase locking pattern to avoid holding ownMilestonesMutex during slow state reader I/O.
//
// Previously, this method held ownMilestonesMutex (write lock) for the entire call, including
// getStateReader().OutputIsConsumed() which acquires branches.mutex -> trie reads.
// When branches.mutex was held by a slow _commitPendingBranch (trie iteration for GC),
// this created a lock convoy: proposer holds ownMilestonesMutex waiting on branches.mutex,
// while other proposers, recreateMapOwnMilestones, and the sequencer loop all block on
// ownMilestonesMutex, causing a >10s stall that triggers the deadlock detector.
//
// Fix: RLock for cache check (allows concurrent readers), no lock during I/O, brief Lock only to update cache.
func (seq *Sequencer) IsConsumedInThePastPath(oid base.OutputID, ms *vertex.WrappedTx, getStateReader func() multistate.SugaredStateReader) bool {
	// phase 1: check cache under read lock (concurrent with other readers)
	seq.ownMilestonesMutex.RLock()
	if seq.ownMilestones[ms].consumed.Contains(oid) {
		seq.ownMilestonesMutex.RUnlock()
		return true
	}
	seq.ownMilestonesMutex.RUnlock()

	// phase 2: I/O outside any lock — may block on branches.mutex without holding ownMilestonesMutex
	ret := getStateReader().OutputIsConsumed(oid)

	// phase 3: brief write lock to update cache on hit
	if ret {
		seq.ownMilestonesMutex.Lock()
		seq.ownMilestones[ms].consumed.Insert(oid)
		seq.ownMilestonesMutex.Unlock()
	}
	return ret
}

func (seq *Sequencer) OwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := seq.GetLatestMilestone(seq.sequencerID)
	if ret != nil {
		chainOut := ret.FindChainOutput(&seq.sequencerID)
		if chainOut != nil && chainOut.Output != nil {
			seq.AddOwnMilestone(ret)
			return attacher.AttachOutputWithID(*chainOut, seq, attacher.WithInvokedBy("OwnLatestMilestoneOutput"))
		}
		// chain output not found in tippool milestone, fall through to bootstrap
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
				v.ForEachInputID(func(i byte, oid base.OutputID) bool {
					ret.Insert(oid)
					return true
				})
				if seqData := v.SequencerTransactionData(); seqData != nil {
					// continue along own predecessors in the cache.
					// Sequencer chain origins have no chain predecessor
					// (PredecessorInputIndex == 0xff): stop the walk.
					predIdx := seqData.SequencerOutputData.ChainConstraint.PredecessorInputIndex
					if predIdx == 0xff || int(predIdx) >= len(v.Inputs) {
						return
					}
					msPred = v.Inputs[predIdx]
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
