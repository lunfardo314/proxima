package tippool

import (
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
)

type (
	environment interface {
		global.NodeGlobal
		GetStateReaderForTheBranch(branch *ledger.TransactionID) global.IndexedStateReader
	}

	Input struct {
		VID *vertex.WrappedTx
	}

	// SequencerTips is a collection with input queue, which keeps all latest sequencer
	// transactions for each sequencer ID. One transaction per sequencer
	// TODO input queue is not very much needed because TPS of sequencer transactions is low
	SequencerTips struct {
		*work_process.WorkProcess[Input]
		mutex                           sync.RWMutex
		latestMilestones                map[ledger.ChainID]_milestoneData
		expectedSequencerActivityPeriod time.Duration
	}

	_milestoneData struct {
		*vertex.WrappedTx
		lastActivity   time.Time
		loggedActive   bool
		loggedInactive bool
	}
)

const (
	Name            = "tippool"
	TraceTag        = Name
	purgeLoopPeriod = 5 * time.Second

	expectedSequencerActivityPeriodInSlots = 5
)

func New(env environment) *SequencerTips {
	ret := &SequencerTips{
		latestMilestones:                make(map[ledger.ChainID]_milestoneData),
		expectedSequencerActivityPeriod: time.Duration(expectedSequencerActivityPeriodInSlots) * ledger.L().ID.SlotDuration(),
	}
	ret.WorkProcess = work_process.New[Input](env, Name, ret.consume)
	ret.WorkProcess.Start()

	ret.RepeatInBackground(Name+"_purge_and_log_loop", purgeLoopPeriod, func() bool {
		ret.purgeAndLog()
		return true
	}, true)
	return ret
}

func (t *SequencerTips) consume(inp Input) {
	seqID := inp.VID.SequencerID.Load()
	t.Assertf(seqID != nil, "inp.VID.SequencerID != nil")
	t.Tracef(TraceTag, "seq milestone IN: %s of %s", inp.VID.IDShortString, seqID.StringShort)

	t.mutex.Lock()
	defer t.mutex.Unlock()

	storedNew := false
	old, prevExists := t.latestMilestones[*seqID]
	if prevExists {
		if old.WrappedTx == inp.VID {
			// repeating, ignore
			return
		}
		if ledger.TooCloseOnTimeAxis(&old.ID, &inp.VID.ID) {
			// this means there's a bug in the sequencer because it submits transactions too close in the ledger time window
			t.Log().Warnf("[tippool] %s and %s: too close on time axis. seqID: %s",
				old.IDShortString(), inp.VID.IDShortString(), seqID.StringShort())
		}
		if t.replaceOldWithNew(old.WrappedTx, inp.VID) {
			if inp.VID.Reference() {
				old.UnReference()
				old.WrappedTx = inp.VID
				old.lastActivity = time.Now()
				t.latestMilestones[*seqID] = old
				storedNew = true
			}
		} else {
			t.Tracef(TraceTag, "incoming milestone %s didn't replace existing %s", inp.VID.IDShortString, old.IDShortString)
		}
	} else {
		if inp.VID.Reference() {
			t.latestMilestones[*seqID] = _milestoneData{
				WrappedTx:    inp.VID,
				lastActivity: time.Now(),
			}
			storedNew = true
		}
	}
	prevStr := "<none>"
	if prevExists {
		prevStr = old.IDShortString()
	}
	if storedNew {
		t.Tracef(TraceTag, "new milestone: seqID: %s,  %s (replaced: %s)", seqID.StringShort, inp.VID.IDShortString, prevStr)
	}
}

func (t *SequencerTips) isActive(m *_milestoneData) bool {
	return time.Since(m.lastActivity) < t.expectedSequencerActivityPeriod
}

// replaceOldWithNew compares timestamps, chooses the younger one.
// If timestamps equal, chooses the preferred one, older is preferred
func (t *SequencerTips) replaceOldWithNew(old, new *vertex.WrappedTx) bool {
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
	ret, ok := t.latestMilestones[seqID]
	if !ok {
		return nil
	}
	return ret.WrappedTx
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
		if flt(seqID, ms.WrappedTx) {
			ret = append(ret, ms.WrappedTx)
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

// purgeAndLog removes all transactions with baseline == nil, i.e. all non-branch sequencers which are virtualTx
func (t *SequencerTips) purgeAndLog() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	toDelete := make([]ledger.ChainID, 0)
	for chainID, md := range t.latestMilestones {
		nothingLogged := !md.loggedActive && !md.loggedInactive

		if t.isActive(&md) {
			if md.loggedInactive || nothingLogged {
				t.Log().Infof("sequencer %s is ACTIVE", chainID.StringShort())
				md.loggedInactive = false
				md.loggedActive = true
				t.latestMilestones[chainID] = md
			}
		} else {
			if md.loggedActive || nothingLogged {
				t.Log().Infof("sequencer %s is INACTIVE", chainID.StringShort())
				md.loggedInactive = true
				md.loggedActive = false
				t.latestMilestones[chainID] = md
			}
		}
		if md.BaselineBranch() == nil {
			toDelete = append(toDelete, chainID)
		}
	}

	for _, chainID := range toDelete {
		t.latestMilestones[chainID].UnReference()
		delete(t.latestMilestones, chainID)
		t.Log().Infof("chainID %s has been removed from the sequencer tippool", chainID.StringShort())
	}
}
