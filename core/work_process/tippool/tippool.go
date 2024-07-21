package tippool

import (
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/queue"
)

type (
	Environment interface {
		global.NodeGlobal
		GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader
	}

	Input struct {
		VID *vertex.WrappedTx
	}

	// SequencerTips is a collection with input queue, which keeps all latest sequencer transactions for each sequencer ID
	// One transaction per sequencer
	SequencerTips struct {
		*queue.Queue[Input]
		Environment
		mutex            sync.RWMutex
		latestMilestones map[ledger.ChainID]*vertex.WrappedTx
	}
)

const (
	Name           = "tippool"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *SequencerTips {
	return &SequencerTips{
		Queue:            queue.NewQueueWithBufferSize[Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment:      env,
		latestMilestones: make(map[ledger.ChainID]*vertex.WrappedTx),
	}
}

func (t *SequencerTips) Start() {
	t.MarkWorkProcessStarted(Name)
	t.AddOnClosed(func() {
		t.MarkWorkProcessStopped(Name)
	})
	t.Queue.Start(t, t.Environment.Ctx())
	go t.purgeLoop()
}

func (t *SequencerTips) Consume(inp Input) {
	seqIDIncoming, ok := inp.VID.SequencerIDIfAvailable()
	t.Assertf(ok, "sequencer milestone expected")
	t.Environment.Tracef(TraceTag, "seq milestone IN: %s of %s", inp.VID.IDShortString, seqIDIncoming.StringShort)

	t.mutex.Lock()
	defer t.mutex.Unlock()

	storedNew := false
	old, prevExists := t.latestMilestones[seqIDIncoming]
	if prevExists {
		if old == inp.VID {
			// repeating, ignore
			return
		}
		if ledger.TooCloseOnTimeAxis(&old.ID, &inp.VID.ID) {
			// this means there's a bug in the sequencer because it submits transactions too close in the ledger time window
			t.Environment.Log().Warnf("tippool: %s and %s: too close on time axis", old.IDShortString(), inp.VID.IDShortString())
		}
		if t.oldReplaceWithNew(old, inp.VID) {
			if inp.VID.Reference() {
				old.UnReference()
				t.latestMilestones[seqIDIncoming] = inp.VID
				storedNew = true
			}
		} else {
			t.Tracef(TraceTag, "tippool: incoming milestone %s didn't replace existing %s", inp.VID.IDShortString, old.IDShortString)
		}
	} else {
		if inp.VID.Reference() {
			t.latestMilestones[seqIDIncoming] = inp.VID
			storedNew = true
		}
	}
	prevStr := "<none>"
	if prevExists {
		prevStr = old.IDShortString()
	}
	if storedNew {
		t.Tracef(TraceTag, "new milestone stored in sequencer tippool: %s (prev: %s)", inp.VID.IDShortString, prevStr)
	}
}

// oldReplaceWithNew compares timestamps, chooses the younger one.
// If timestamps equal, chooses the preferred one, older is preferred
func (t *SequencerTips) oldReplaceWithNew(old, new *vertex.WrappedTx) bool {
	t.Assertf(old != new, "old != new")
	tsOld := old.Timestamp()
	tsNew := new.Timestamp()
	switch {
	case tsOld.Before(tsNew):
		return true
	case tsOld.After(tsNew):
		return false
	}
	t.Assertf(tsNew == tsOld, "tsNew==tsOld")
	return vertex.IsPreferredMilestoneAgainstTheOther(new, old, false)
}

// GetLatestMilestone will return nil if sequencer is not in the list
func (t *SequencerTips) GetLatestMilestone(seqID ledger.ChainID) *vertex.WrappedTx {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return t.latestMilestones[seqID]
}

// LatestMilestonesDescending returns sequencer transactions from sequencer tippool. Optionally filters
// Sorts in the descending preference order (essentially by ledger coverage)
func (t *SequencerTips) LatestMilestonesDescending(filter ...func(seqID ledger.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	flt := func(_ ledger.ChainID, _ *vertex.WrappedTx) bool { return true }
	if len(filter) > 0 {
		flt = filter[0]
	}

	t.mutex.RLock()
	defer t.mutex.RUnlock()

	ret := make([]*vertex.WrappedTx, 0, len(t.latestMilestones))
	for seqID, ms := range t.latestMilestones {
		if flt(seqID, ms) {
			ret = append(ret, ms)
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return vertex.IsPreferredMilestoneAgainstTheOther(ret[i], ret[j], false)
	})
	return ret
}

func (t *SequencerTips) NumSequencerTips() int {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return len(t.latestMilestones)
}

const purgeLoopPeriod = time.Second

// purgeLoop periodically removes all vertices which cannot be endorsed
func (t *SequencerTips) purgeLoop() {
	for {
		select {
		case <-t.Ctx().Done():
			return
		case <-time.After(purgeLoopPeriod):
			t.purge()
		}
	}
}

// purge removes all transactions with baseline == nil, i.e. all non-branch sequencers which are virtualTx
func (t *SequencerTips) purge() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	toDelete := make([]ledger.ChainID, 0)

	for chainID, md := range t.latestMilestones {
		if md.BaselineBranch() == nil {
			toDelete = append(toDelete, chainID)
		}
	}

	for _, chainID := range toDelete {
		t.latestMilestones[chainID].UnReference()
		delete(t.latestMilestones, chainID)
		t.Environment.Log().Infof("chainID %s has been removed from the sequencer tip pool", chainID.StringShort())
	}
}
