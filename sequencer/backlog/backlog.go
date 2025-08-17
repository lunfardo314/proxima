package backlog

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/maps"
)

type (
	Environment interface {
		global.NodeGlobal
		attacher.Environment
		ListenToAccount(account ledger.Accountable, fun func(wOut vertex.WrappedOutput))
		SequencerID() base.ChainID
		SequencerName() string
		GetLatestMilestone(seqID base.ChainID) *vertex.WrappedTx
		LatestMilestonesDescending(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		LatestMilestonesShuffled(filter ...func(seqID base.ChainID, vid *vertex.WrappedTx) bool) []*vertex.WrappedTx
		NumSequencerTips() int
		BacklogTTLSlots() (int, int)
		MustEnsureBranch(txid base.TransactionID) *vertex.WrappedTx
		EvidenceBacklogSize(size int)
	}

	TagAlongBacklog struct {
		Environment
		mutex                    sync.RWMutex
		outputs                  map[vertex.WrappedOutput]time.Time
		outputCount              int
		removedOutputsSinceReset int
		lastOutputArrived        time.Time
	}

	Stats struct {
		NumOtherSequencers       int
		NumOutputs               int
		OutputCount              int
		RemovedOutputsSinceReset int
	}
)

const TraceTag = "backlog"

func New(env Environment) (*TagAlongBacklog, error) {
	seqID := env.SequencerID()
	ret := &TagAlongBacklog{
		Environment: env,
		outputs:     make(map[vertex.WrappedOutput]time.Time),
	}
	env.Tracef(TraceTag, "starting input backlog for the sequencer %s..", env.SequencerName)

	// start listening to chain-locked account. Tag-along outputs
	env.ListenToAccount(ledger.ChainLockFromChainID(seqID), func(wOut vertex.WrappedOutput) {
		env.Tracef(TraceTag, "[%s] output IN: %s", ret.SequencerName, wOut.IDStringShort)

		ret.mutex.Lock()
		defer ret.mutex.Unlock()

		if _, already := ret.outputs[wOut]; already {
			env.Tracef(TraceTag, "repeating output %s", wOut.IDStringShort)
			return
		}
		if !ret.checkCandidate(wOut) {
			return
		}
		// new output -> put it into the map
		nowis := time.Now()
		ret.outputs[wOut] = nowis
		ret.lastOutputArrived = nowis
		ret.outputCount++
		//wOut.VID.Reference()
		env.Tracef(TraceTag, "output included into input backlog: %s (total: %d)", wOut.IDStringShort, len(ret.outputs))
	})

	const (
		backlogCleanupPeriod = time.Second
		recreateMapPeriod    = time.Minute
	)
	// start periodic cleanup in background
	env.RepeatInBackground(env.SequencerName()+"_backlogCleanup", backlogCleanupPeriod, func() bool {
		if n := ret.purgeBacklog(); n > 0 {
			ret.Log().Infof("deleted %d outputs from the backlog", n)
		}
		return true
	})
	// start periodic reallocation of the map
	env.RepeatInBackground(env.SequencerName()+"_backlogRecreateMap", recreateMapPeriod, func() bool {
		ret.recreateMap()
		return true
	})

	return ret, nil
}

func (b *TagAlongBacklog) ArrivedOutputsSince(t time.Time) bool {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	return b.lastOutputArrived.After(t)
}

// checkCandidate if returns false, it is unreferenced, otherwise referenced
func (b *TagAlongBacklog) checkCandidate(wOut vertex.WrappedOutput) bool {
	if wOut.VID.IsBranchTransaction() {
		// outputs of branch transactions are filtered out
		return false
	}
	if wOut.VID.GetTxStatus() == vertex.Bad {
		return false
	}
	o, err := wOut.VID.OutputAt(wOut.Index)
	if err != nil {
		return false
	}
	if o == nil {
		return true
	}
	lock := wOut.Lock()
	if _, idx := o.ChainConstraint(); idx != 0xff {
		// filter out all chain constrained outputs
		return false
	}
	if lock.Name() != ledger.ChainLockName {
		// filter out all which cannot be consumed by the sequencer
		return false
	}

	// TODO --
	//if dl, ok := lock.(*ledger.DelegationLock); ok {
	//	seqID := b.SequencerID()
	//	if !ledger.EqualAccountables(ledger.ChainLockFromChainID(seqID), dl.TargetLock) {
	//		// filter out delegation locks is delegation target cannot be consumed
	//		return false
	//	}
	//}
	return true
}

// CandidatesToEndorseSorted returns descending (by coverage) list of transactions which can be endorsed from the given timestamp
func (b *TagAlongBacklog) CandidatesToEndorseSorted(targetTs base.LedgerTime) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot
	ownSeqID := b.SequencerID()
	return b.LatestMilestonesDescending(func(seqID base.ChainID, vid *vertex.WrappedTx) bool {
		if _, ok := vid.BaselineBranch(); !ok {
			return false
		}
		return vid.Slot() == targetSlot && seqID != ownSeqID && ledger.ValidSequencerPace(vid.Timestamp(), targetTs)
	})
}

// CandidatesToEndorseShuffled returns randomly ordered list of transactions which can be endorsed from the given timestamp
func (b *TagAlongBacklog) CandidatesToEndorseShuffled(targetTs base.LedgerTime) []*vertex.WrappedTx {
	targetSlot := targetTs.Slot
	ownSeqID := b.SequencerID()
	return b.LatestMilestonesShuffled(func(seqID base.ChainID, vid *vertex.WrappedTx) bool {
		if _, ok := vid.BaselineBranch(); !ok {
			return false
		}
		return vid.Slot() == targetSlot && seqID != ownSeqID && ledger.ValidSequencerPace(vid.Timestamp(), targetTs)
	})
}

func (b *TagAlongBacklog) GetOwnLatestMilestoneTx() *vertex.WrappedTx {
	return b.GetLatestMilestone(b.SequencerID())
}

func (b *TagAlongBacklog) FilterAndSortOutputs(filter func(wOut vertex.WrappedOutput) bool) []vertex.WrappedOutput {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	ret := util.KeysFiltered(b.outputs, filter)
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Timestamp().Before(ret[j].Timestamp())
	})
	return ret
}

func (b *TagAlongBacklog) NumOutputsInBuffer() int {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	return len(b.outputs)
}

func (b *TagAlongBacklog) getStatsAndReset() (ret Stats) {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	ret = Stats{
		NumOtherSequencers:       b.NumSequencerTips(),
		NumOutputs:               len(b.outputs),
		OutputCount:              b.outputCount,
		RemovedOutputsSinceReset: b.removedOutputsSinceReset,
	}
	b.removedOutputsSinceReset = 0
	return
}

func (b *TagAlongBacklog) numOutputs() int {
	b.mutex.RLock()
	defer b.mutex.RUnlock()

	return len(b.outputs)
}

func (b *TagAlongBacklog) purgeBacklog() int {
	ttlTagAlongSlots, ttlDelegationSlots := b.BacklogTTLSlots()
	_ = ttlDelegationSlots
	horizonTagAlong := time.Now().Add(-time.Duration(ttlTagAlongSlots) * ledger.L().ID.SlotDuration())
	//horizonDelegation := time.Now().Add(-time.Duration(ttlDelegationSlots) * ledger.L().ChainID.SlotDuration())

	b.mutex.Lock()
	defer b.mutex.Unlock()

	count := 0
	for wOut, whenAdded := range b.outputs {
		del := true
		switch wOut.LockName() {
		case ledger.ChainLockName:
			del = whenAdded.Before(horizonTagAlong)
		// TODO --
		//case ledger.DelegationLockName:
		//	del = whenAdded.Before(horizonDelegation)
		default:
			b.Log().Fatalf("unexpected type of the lock in backlog: '%s'", wOut.LockName())
		}
		if del {
			delete(b.outputs, wOut)
			count++
			//wOut.VID.UnReference()
		}
	}
	b.EvidenceBacklogSize(len(b.outputs))
	return count
}

func (b *TagAlongBacklog) recreateMap() {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	b.outputs = maps.Clone(b.outputs)
}

// LoadSequencerStartTips loads tip transactions relevant to the sequencer startup from persistent state to the memDAG
func (b *TagAlongBacklog) LoadSequencerStartTips(seqID base.ChainID) error {
	branchData := multistate.FindLatestReliableBranch(b.StateStore(), global.FractionHealthyBranch)
	//branchData := b.Branches().FindLatestReliableBranch(global.FractionHealthyBranch)
	if branchData == nil {
		return fmt.Errorf("LoadSequencerStartTips: can't find latest reliable branch (LRB) with franction %s", global.FractionHealthyBranch.String())
	}
	loadedTxs := set.New[*vertex.WrappedTx]()
	nowSlot := ledger.TimeNow().Slot
	brid := branchData.TxID()
	b.Log().Infof("loading sequencer tips for %s from branch %s, %d slots back from (current slot is %d)",
		seqID.StringShort(), brid.StringShort(), nowSlot-brid.Slot(), nowSlot)

	rdr := multistate.MustNewSugaredReadableState(b.StateStore(), branchData.Root, 0)
	vidBranch := b.MustEnsureBranch(branchData.Stem.ID.TransactionID())
	loadedTxs.Insert(vidBranch)

	// load sequencer output for the chain
	chainOut, err := rdr.GetChainOutputWithID(seqID)
	if err != nil {
		return fmt.Errorf("LoadSequencerStartTips: can't load chain output for %s: %w", seqID.StringShort(), err)
	}
	wOut := attacher.AttachOutputWithID(*chainOut, b, attacher.WithInvokedBy("LoadSequencerStartTips"))
	loadedTxs.Insert(wOut.VID)

	b.Log().Infof("loaded sequencer start output from branch %s\n%s",
		vidBranch.IDShortString(), chainOut.LinesSource("         ").String())

	// load pending tag-along outputs
	oids, err := rdr.GetUTXOIDsInAccount(ledger.ChainLockFromChainID(seqID).AccountID())
	util.AssertNoError(err)
	for _, oid := range oids {
		o := rdr.MustGetOutputWithID(oid)
		wOut = attacher.AttachOutputWithID(*o, b, attacher.WithInvokedBy("LoadSequencerStartTips"))
		b.Log().Infof("loaded tag-along input for sequencer %s: %s from branch %s", seqID.StringShort(), oid.StringShort(), vidBranch.IDShortString())
		loadedTxs.Insert(wOut.VID)
	}
	// post a new tx event for each transaction
	for vid := range loadedTxs {
		b.PostEventNewTransaction(vid)
	}
	return nil
}
