package tippool

import (
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/core_modules"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/exp/rand"
)

type (
	environment interface {
		global.NodeGlobal
	}

	Input struct {
		*vertex.WrappedTx
	}

	// SequencerTips is a collection with input queue, which keeps all latest sequencer
	// transactions for each sequencer id. One transaction per sequencer
	// TODO input queue is not very much needed because TPS of sequencer transactions is low
	SequencerTips struct {
		*core_modules.CoreModule[Input]
		mutex                           sync.RWMutex
		latestMilestones                map[base.ChainID]_activeMilestoneData
		expectedSequencerActivityPeriod time.Duration
		latestMilestoneAddedWhen        time.Time
		latestSequencerData             map[base.ChainID]LatestSequencerTipData
	}

	_activeMilestoneData struct {
		*vertex.WrappedTx
		lastActivity         time.Time
		loggedActive         bool
		loggedInactive       bool
		loggedCoverageNotSet *base.TransactionID
	}

	LatestSequencerTipData struct {
		LatestMilestoneTxID base.TransactionID
		LastBranchTxID      *base.TransactionID
		MilestoneCount      int
		LastActivity        time.Time
	}

	LatestSequencerTipDataJSONAble struct {
		LatestMilestoneTxID  string `json:"latest_milestone_txid"`
		LastBranchTxID       string `json:"last_branch_txid,omitempty"`
		MilestoneCount       int    `json:"milestone_count"`
		LastActivityUnixNano int64  `json:"last_activity_unix_nano"`
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
		latestMilestones:                make(map[base.ChainID]_activeMilestoneData),
		expectedSequencerActivityPeriod: time.Duration(expectedSequencerActivityPeriodInSlots) * ledger.Const.SlotDuration(),
		latestSequencerData:             make(map[base.ChainID]LatestSequencerTipData),
	}
	ret.CoreModule = core_modules.New[Input](env, Name, ret.consume)
	ret.CoreModule.Start()

	ret.RepeatInBackground(Name+"_purge_and_log_loop", purgeLoopPeriod, func() bool {
		ret.purgeAndLog()
		return true
	}, true)
	return ret
}

func (t *SequencerTips) consume(inp Input) {
	seqID := inp.SequencerID.Load()
	t.Assertf(seqID != nil, "inp.VID.SequencerID != nil")
	t.Tracef(TraceTag, "seq milestone IN: %s of %s", inp.IDShortString, seqID.StringShort)

	t.mutex.Lock()
	defer t.mutex.Unlock()

	t.updateLatestSequencerData(inp.WrappedTx, *seqID)

	storedNew := false
	old, prevExists := t.latestMilestones[*seqID]
	if prevExists {
		if old.WrappedTx == inp.WrappedTx {
			// repeating, ignore
			return
		}
		if ledger.TooCloseOnTimeAxis(old.ID(), inp.ID()) {
			// this means there's a bug in the sequencer because it submits transactions too close in the ledger time window
			t.Log().Warnf("[tippool] %s and %s: too close on time axis. seqID: %s",
				old.IDShortString(), inp.IDShortString(), seqID.StringShort())
		}
		if t.replaceOldWithNew(old.WrappedTx, inp.WrappedTx) {
			old.WrappedTx = inp.WrappedTx
			old.lastActivity = time.Now()
			t.latestMilestones[*seqID] = old
			t.latestMilestoneAddedWhen = time.Now()
			storedNew = true
		} else {
			t.Tracef(TraceTag, "incoming milestone %s didn't replace existing %s", inp.IDShortString, old.IDShortString)
		}
	} else {
		t.latestMilestones[*seqID] = _activeMilestoneData{
			WrappedTx:    inp.WrappedTx,
			lastActivity: time.Now(),
		}
		t.latestMilestoneAddedWhen = time.Now()
		storedNew = true
	}
	prevStr := "<none>"
	if prevExists {
		prevStr = old.IDShortString()
	}
	if storedNew {
		t.Tracef(TraceTag, "new milestone: seqID: %s,  %s (replaced: %s)", seqID.StringShort, inp.IDShortString, prevStr)
	}
}

func (t *SequencerTips) updateLatestSequencerData(vid *vertex.WrappedTx, seqID base.ChainID) {
	seqData := t.latestSequencerData[seqID]
	seqData.LatestMilestoneTxID = vid.ID()
	if vid.IsSequencerMilestone() {
		seqData.LastBranchTxID = util.Ref(vid.ID())
	}
	seqData.MilestoneCount++
	seqData.LastActivity = time.Now()
	t.latestSequencerData[seqID] = seqData
}

func (t *SequencerTips) isActive(m *_activeMilestoneData) bool {
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
	return vertex.IsPreferredMilestoneAgainstTheOther(new, old)
}

// GetLatestActiveMilestone will return nil if the sequencer is not in the list
func (t *SequencerTips) GetLatestActiveMilestone(seqID base.ChainID) *vertex.WrappedTx {
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	ret, ok := t.latestMilestones[seqID]
	if !ok {
		return nil
	}
	return ret.WrappedTx
}

// filterLatestActiveMilestones returns sequencer transactions from sequencer tippool. Optionally filters
// Not sorted, random order
func (t *SequencerTips) filterLatestActiveMilestones(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	flt := func(_ base.ChainID, _ *vertex.WrappedTx) bool { return true }
	if len(filter) > 0 {
		flt = filter[0]
	}

	t.mutex.RLock()
	defer t.mutex.RUnlock()

	ret := make([]*vertex.WrappedTx, 0, len(t.latestMilestones))
	for seqID, ms := range t.latestMilestones {
		if ms.WrappedTx.GetLedgerCoverageP() == nil {
			// prevent excessive logging
			if ms.loggedCoverageNotSet == nil || *ms.loggedCoverageNotSet != ms.WrappedTx.ID() {
				t.Log().Warnf("[tippool] %s: ledger coverage is not set", ms.WrappedTx.IDShortString())
				ms.loggedCoverageNotSet = util.Ref(ms.WrappedTx.ID())
				t.latestMilestones[seqID] = ms
			}
			continue
		}
		if flt(seqID, ms.WrappedTx) {
			ret = append(ret, ms.WrappedTx)
		}
	}
	return ret
}

// LatestActiveMilestonesDescending returns sequencer transactions from sequencer tippool. Optionally filters
// Sorts in the descending preference order (essentially by ledger coverage)
func (t *SequencerTips) LatestActiveMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	ret := t.filterLatestActiveMilestones(filter...)
	sort.Slice(ret, func(i, j int) bool {
		return vertex.IsPreferredMilestoneAgainstTheOther(ret[i], ret[j])
	})
	t.Tracef(TraceTag, "LatestActiveMilestonesDescending: len(ret) = %d", len(ret))
	return ret
}

// LatestActiveMilestonesShuffled returns sequencer transactions from sequencer tippool. Optionally filters.
// Randomizes order
func (t *SequencerTips) LatestActiveMilestonesShuffled(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx {
	ret := t.filterLatestActiveMilestones(filter...)
	rand.Shuffle(len(ret), func(i, j int) {
		ret[i], ret[j] = ret[j], ret[i]
	})
	return ret
}

func (t *SequencerTips) NumSequencerTips() int {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	return len(t.latestMilestones)
}

const activityTTL = 40 * time.Second

// purgeAndLog removes all transactions with baseline == nil, i.e. all non-branch sequencers which are virtualTx
func (t *SequencerTips) purgeAndLog() {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	for chainID, md := range t.latestMilestones {
		nothingLogged := !md.loggedActive && !md.loggedInactive

		if t.isActive(&md) {
			if md.loggedInactive || nothingLogged {
				t.Log().Infof("[tippool] sequencer %s is ACTIVE", chainID.StringShort())
				md.loggedInactive = false
				md.loggedActive = true
				t.latestMilestones[chainID] = md
			}
		} else {
			if md.loggedActive || nothingLogged {
				t.Log().Infof("[tippool] sequencer %s is INACTIVE", chainID.StringShort())
				md.loggedInactive = true
				md.loggedActive = false
				t.latestMilestones[chainID] = md
			}
		}
		if time.Since(md.lastActivity) > activityTTL {
			delete(t.latestMilestones, chainID)
			//md.UnReference()
			t.Log().Infof("[tippool] chainID %s has been removed from the sequencer tippool", chainID.StringShort())
		}
	}
}

func (t *SequencerTips) GetKnownLatestSequencerDataJSONAble() map[string]LatestSequencerTipDataJSONAble {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	ret := make(map[string]LatestSequencerTipDataJSONAble)

	for seqID, sd := range t.latestSequencerData {
		d := LatestSequencerTipDataJSONAble{
			LatestMilestoneTxID:  sd.LatestMilestoneTxID.StringHex(),
			MilestoneCount:       sd.MilestoneCount,
			LastActivityUnixNano: sd.LastActivity.UnixNano(),
		}
		if sd.LastBranchTxID != nil {
			d.LastBranchTxID = sd.LastBranchTxID.StringHex()
		}
		ret[seqID.StringHex()] = d
	}
	return ret
}
