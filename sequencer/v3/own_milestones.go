package v3

import (
	"sort"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer"
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

// OwnMilestoneOutputsInMemDAGAscending returns the sequencer's own milestone chain outputs
// currently tracked in the memDAG, oldest timestamp first — the order the factory's extend
// tiebreaker relies on to settle on the newest tip. It is the memDAG-first extend-candidate
// set for the factory: extend candidates are tried from here before touching branch state. Spent
// or stale candidates are harmless — the incremental attacher rejects a double-spend as a conflict.
// The set needs no further trimming: it is already bounded by the own-milestone TTL purge.
func (seq *Sequencer) OwnMilestoneOutputsInMemDAGAscending() []vertex.WrappedOutput {
	seq.ownMilestonesMutex.RLock()
	vids := make([]*vertex.WrappedTx, 0, len(seq.ownMilestones))
	for vid := range seq.ownMilestones {
		vids = append(vids, vid)
	}
	seq.ownMilestonesMutex.RUnlock()

	sort.Slice(vids, func(i, j int) bool {
		return vids[i].Timestamp().Before(vids[j].Timestamp()) // oldest first
	})
	ret := make([]vertex.WrappedOutput, 0, len(vids))
	stale := seq.ownTipStaleCheck()
	for _, vid := range vids {
		if vid.IsBad() {
			seq.warnOwnMilestoneBad(vid)
			continue
		}
		if !vid.IsSequencerTransaction() || stale(vid) {
			continue
		}
		if wOut := vid.SequencerWrappedOutput(); wOut.VID != nil {
			ret = append(ret, wOut)
		}
	}
	return ret
}

// warnOwnMilestoneBad logs, once per milestone, that an own milestone was found BAD. The sequencer's
// own transactions are built to be valid, so one turning BAD is a fault to be explained, never routine.
func (seq *Sequencer) warnOwnMilestoneBad(vid *vertex.WrappedTx) {
	seq.ownMilestonesMutex.Lock()
	entry, ok := seq.ownMilestones[vid]
	warn := ok && !entry.badWarned
	if warn {
		entry.badWarned = true
		seq.ownMilestones[vid] = entry
	}
	seq.ownMilestonesMutex.Unlock()
	if warn {
		seq.log.Warnf("own milestone %s is BAD: %v", vid.IDShortString(), vid.GetError())
	}
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

// ownTipStaleCheck returns the check whether an own milestone is too old to extend (see
// sequencer.OwnTipStale). The latest reliable branch is looked up at most once per check, and only
// for a milestone old enough to need it: a lookup per milestone cost a large share of the CPU.
// Without a reliable branch there is nothing to restart from, so the milestone is kept.
func (seq *Sequencer) ownTipStaleCheck() func(ms *vertex.WrappedTx) bool {
	now := uint32(ledger.TimeNow().Slot)
	var lrb *multistate.BranchData
	looked := false
	return func(ms *vertex.WrappedTx) bool {
		return sequencer.OwnTipStale(ms.Slot(), now, func() bool {
			if !looked {
				lrb, looked = seq.Branches().FindLatestReliableBranch(), true
			}
			return lrb == nil || seq.Branches().BranchKnowsTransaction(lrb.TxID(), ms.ID())
		})
	}
}

func (seq *Sequencer) OwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := seq.GetLatestMilestone(seq.sequencerID)
	if ret != nil && seq.ownTipStaleCheck()(ret) {
		seq.log.Infof("own latest milestone %s is stale and not in the latest reliable branch: restarting the chain from the LRB", ret.IDShortString())
		ret = nil
	}
	if ret != nil {
		chainOut := ret.FindChainOutput(&seq.sequencerID)
		if chainOut != nil && chainOut.Output != nil {
			seq.AddOwnMilestone(ret)
			return attacher.AttachOutputWithID(*chainOut, seq, attacher.WithInvokedBy("OwnLatestMilestoneOutput"))
		}
		// chain output not found in tippool milestone, fall through to bootstrap
	}
	// there's no own milestone in the tippool, find in one of the baseline states of other sequencers or in LRB
	return seq.ownMilestoneOutputFromLRB()
}

// classifyMove names, for SLOT STATS, how a move that changes the own chain's line (prefix "fm" for the
// slot's first move, "ra" for a re-anchor later in the slot) relates the own chain to the lineage it
// endorses. A sequencer that spends one own output on two lineages strands whoever holds the abandoned
// spend; these classes tell which shape a move has, in order of precedence:
//   - OwnBranch: no endorsement.
//   - SelfFork: the move abandons own milestones (not on the extended chain, so they spent an output it
//     also spends) which another sequencer's latest milestone of the slot, or its baseline, holds.
//   - CarryForeign, Carry: the endorsed lineage neither holds nor commits the extended milestone, which
//     brings left-out own history into it; foreign when that history endorses other sequencers'
//     milestones the lineage lacks, whose owners usually restart and so conflict with it.
//   - Restart: the move abandons own milestones nobody else holds (moving off the own losing history).
//   - Consistent: none of these.
func (seq *Sequencer) classifyMove(tx *transaction.Transaction, prefix string) string {
	if tx.NumEndorsements() == 0 {
		return prefix + "OwnBranch"
	}
	predOID, _, err := tx.SequencerChainPredecessor()
	if err != nil {
		return prefix + "Unknown"
	}
	ext := attacher.AttachTxID(predOID.TransactionID(), seq, attacher.WithInvokedBy("classifyMove"))
	var endorsedID base.TransactionID
	tx.ForEachEndorsement(func(_ byte, txid base.TransactionID) bool {
		endorsedID = txid
		return false
	})
	endorsed := attacher.AttachTxID(endorsedID, seq, attacher.WithInvokedBy("classifyMove"))

	// Own milestones older than the move and not on the extended chain are abandoned by it. Where the
	// walk back along the chain stops at a predecessor that is not a full vertex, older own milestones
	// are left undecided.
	own := seq.OwnMilestoneOutputsInMemDAGAscending()
	onChain := make(map[*vertex.WrappedTx]struct{})
	var undecided base.LedgerTime
	if len(own) > 0 {
		oldest := own[0].VID.Timestamp()
		for v := ext; v != nil && !v.Timestamp().Before(oldest); {
			onChain[v] = struct{}{}
			pred := v.SequencerPredecessor(func(base.TransactionID) *vertex.WrappedTx { return nil })
			if pred == nil {
				undecided = v.Timestamp()
			}
			v = pred
		}
	}
	var abandoned []*vertex.WrappedTx
	for _, o := range own {
		if _, ok := onChain[o.VID]; !ok && o.VID.Timestamp().Before(tx.Timestamp()) && o.VID.Timestamp().After(undecided) {
			abandoned = append(abandoned, o.VID)
		}
	}
	if len(abandoned) > 0 {
		// the lineages live in the move's slot: other sequencers' milestones of that slot
		others := seq.LatestMilestonesDescending(func(seqID base.ChainID, vid *vertex.WrappedTx) bool {
			return seqID != seq.sequencerID && vid.Slot() == tx.Slot()
		})
		baselines := make(map[base.TransactionID]struct{})
		for _, vid := range others {
			if bl, ok := vid.BaselineBranch(); ok {
				baselines[bl] = struct{}{}
			}
		}
		for _, a := range abandoned {
			for _, o := range others {
				if o.PastConeReferences(a) {
					return prefix + "SelfFork"
				}
			}
			for bl := range baselines {
				if a.Slot() < bl.Slot() && seq.Branches().BranchKnowsTransaction(bl, a.ID()) {
					return prefix + "SelfFork"
				}
			}
		}
	}

	if carried := seq.carriedHistory(ext, endorsed, predOID); carried != nil {
		for _, v := range carried {
			var endorsements []*vertex.WrappedTx
			v.RUnwrap(vertex.UnwrapOptions{Vertex: func(vx *vertex.Vertex) {
				endorsements = append(endorsements, vx.Endorsements...)
			}})
			for _, e := range endorsements {
				if id := e.SequencerID.Load(); id != nil && *id != seq.sequencerID && !endorsedHolds(seq, endorsed, e) {
					return prefix + "CarryForeign"
				}
			}
		}
		return prefix + "Carry"
	}
	if len(abandoned) > 0 {
		return prefix + "Restart"
	}
	return prefix + "Consistent"
}

// carriedHistory returns the own milestones the move brings into the endorsed lineage: the extended
// chain back to the first milestone the lineage holds, or nil when the lineage holds or commits the
// extended milestone itself.
func (seq *Sequencer) carriedHistory(ext, endorsed *vertex.WrappedTx, predOID base.OutputID) []*vertex.WrappedTx {
	if endorsedHolds(seq, endorsed, ext) {
		return nil
	}
	if baseline, ok := endorsed.BaselineBranch(); ok {
		if committed, err := seq.Branches().GetChainOutputFromBranch(baseline, seq.sequencerID); err == nil && committed.ID == predOID {
			return nil
		}
	}
	var ret []*vertex.WrappedTx
	for v := ext; v != nil && !endorsedHolds(seq, endorsed, v); v = v.SequencerPredecessor(func(base.TransactionID) *vertex.WrappedTx { return nil }) {
		ret = append(ret, v)
	}
	return ret
}

// endorsedHolds tells whether the endorsed lineage holds the milestone: in its past cone or in its baseline.
func endorsedHolds(seq *Sequencer, endorsed, vid *vertex.WrappedTx) bool {
	if endorsed == vid || endorsed.PastConeReferences(vid) {
		return true
	}
	baseline, ok := endorsed.BaselineBranch()
	return ok && vid.Slot() < baseline.Slot() && seq.Branches().BranchKnowsTransaction(baseline, vid.ID())
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
