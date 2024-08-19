package sequencer

import (
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
)

func (seq *Sequencer) FutureConeOwnMilestonesOrdered(rootOutput vertex.WrappedOutput, targetTs ledger.Time) []vertex.WrappedOutput {
	seq.ownMilestonesMutex.RLock()
	defer seq.ownMilestonesMutex.RUnlock()

	seq.Tracef(TraceTag, "FutureConeOwnMilestonesOrdered for root output %s. Total %d own milestones",
		rootOutput.IDShortString, len(seq.ownMilestones))

	_, ok := seq.ownMilestones[rootOutput.VID]
	seq.Assertf(ok, "FutureConeOwnMilestonesOrdered: milestone output %s of chain %s is expected to be among set of own milestones (%d)",
		rootOutput.IDShortString, seq.sequencerID.StringShort, len(seq.ownMilestones))

	ordered := util.KeysSorted(seq.ownMilestones, func(vid1, vid2 *vertex.WrappedTx) bool {
		// by timestamp -> equivalent to topological order, ascending, i.e. older first
		return vid1.Timestamp().Before(vid2.Timestamp())
	})

	visited := set.New[*vertex.WrappedTx](rootOutput.VID)
	ret := []vertex.WrappedOutput{rootOutput}
	for _, vid := range ordered {
		switch {
		case vid.IsBadOrDeleted():
			continue
		case !vid.IsSequencerMilestone():
			continue
		case !visited.Contains(vid.SequencerPredecessor()):
			continue
		case !ledger.ValidTransactionPace(vid.Timestamp(), targetTs):
			continue
		}
		visited.Insert(vid)
		ret = append(ret, vid.SequencerWrappedOutput())
	}
	return ret
}

func (seq *Sequencer) IsConsumedInThePastPath(wOut vertex.WrappedOutput, ms *vertex.WrappedTx) bool {
	seq.ownMilestonesMutex.RLock()
	defer seq.ownMilestonesMutex.RUnlock()

	return seq.ownMilestones[ms].consumed.Contains(wOut)
}

func (seq *Sequencer) OwnLatestMilestoneOutput() vertex.WrappedOutput {
	ret := seq.GetLatestMilestone(seq.sequencerID)
	if ret != nil {
		seq.AddOwnMilestone(ret)
		return ret.SequencerWrappedOutput()
	}
	// there's no own milestone in the tippool (startup)
	// find in one of baseline states of other sequencers
	return seq.bootstrapOwnMilestoneOutput()
}

func (seq *Sequencer) AddOwnMilestone(vid *vertex.WrappedTx) {
	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	if _, already := seq.ownMilestones[vid]; already {
		return
	}

	vid.MustReference()

	withTime := outputsWithTime{
		consumed: set.New[vertex.WrappedOutput](),
		since:    time.Now(),
	}
	if vid.IsSequencerMilestone() {
		if prev := vid.SequencerPredecessor(); prev != nil {
			if prevConsumed, found := seq.ownMilestones[prev]; found {
				withTime.consumed.AddAll(prevConsumed.consumed)
			}
		}
		vid.Unwrap(vertex.UnwrapOptions{Vertex: func(v *vertex.Vertex) {
			v.ForEachInputDependency(func(i byte, vidInput *vertex.WrappedTx) bool {
				withTime.consumed.Insert(vertex.WrappedOutput{
					VID:   vidInput,
					Index: v.Tx.MustOutputIndexOfTheInput(i),
				})
				return true
			})
		}})
	}
	seq.ownMilestones[vid] = withTime
}

func (seq *Sequencer) purgeOwnMilestones(ttl time.Duration) int {
	horizon := time.Now().Add(-ttl)

	seq.ownMilestonesMutex.Lock()
	defer seq.ownMilestonesMutex.Unlock()

	toDelete := make([]*vertex.WrappedTx, 0)
	for vid, withTime := range seq.ownMilestones {
		if withTime.since.Before(horizon) {
			toDelete = append(toDelete, vid)
		}
	}

	for _, vid := range toDelete {
		vid.UnReference()
		delete(seq.ownMilestones, vid)
	}
	return len(toDelete)
}

func (seq *Sequencer) purgeLoop() {
	ttl := time.Duration(seq.MilestonesTTLSlots()) * ledger.L().ID.SlotDuration()

	for {
		select {
		case <-seq.Ctx().Done():
			return

		case <-time.After(time.Second):
			if n := seq.purgeOwnMilestones(ttl); n > 0 {
				seq.Infof1("purged %d own milestones", n)
			}
		}
	}
}
