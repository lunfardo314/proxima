// Package sync implements sequential branch-by-branch syncing.
// When the node falls behind by more than SyncThresholdUp slots, the sync module
// requests a branch list from a trusted API source and pulls branches one at a time.
// Transactions with timestamps beyond the current sync frontier are dropped from attachment.
package sync

import (
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
)

const (
	Name = "sync"

	defaultThresholdUp   = 5
	defaultThresholdDown = 3
	syncLoopPeriod       = time.Second
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		PullFromPeers(txid base.TransactionID) int
		AddPulledTransaction(txid base.TransactionID)
	}

	Sync struct {
		environment
		source        *client.APIClient
		thresholdUp   uint32
		thresholdDown uint32
		isSyncing     atomic.Bool
		// cached branch list (oldest first), protected by the sync loop goroutine (no concurrent access)
		branchList   []base.TransactionID
		frontierSlot atomic.Uint32
	}
)

// Start initializes and starts the sync module. If sync.source is not configured, the module is inactive.
func Start(env environment) *Sync {
	sourceURL := viper.GetString("sync.source")
	if sourceURL == "" {
		env.Log().Infof("[%s] no sync source configured, sync module inactive", Name)
		return nil
	}

	thUp := viper.GetInt("sync.threshold_up")
	if thUp <= 0 {
		thUp = defaultThresholdUp
	}
	thDown := viper.GetInt("sync.threshold_down")
	if thDown <= 0 {
		thDown = defaultThresholdDown
	}

	ret := &Sync{
		environment:   env,
		source:        client.NewWithGoogleDNS(sourceURL, 10*time.Second),
		thresholdUp:   uint32(thUp),
		thresholdDown: uint32(thDown),
	}

	env.RepeatInBackground(Name, syncLoopPeriod, func() bool {
		ret.syncTick()
		return true
	})

	env.Log().Infof("[%s] started, source: %s, threshold up: %d, down: %d", Name, sourceURL, thUp, thDown)
	return ret
}

// IsSyncing returns true when the node is in sync catch-up mode
func (s *Sync) IsSyncing() bool {
	if s == nil {
		return false
	}
	return s.isSyncing.Load()
}

// SyncFrontierSlot returns the slot of the branch currently being synced.
// Transactions with timestamps after this slot should be dropped from attachment.
// Returns 0 when not syncing (meaning no filtering).
func (s *Sync) SyncFrontierSlot() uint32 {
	if s == nil {
		return 0
	}
	return s.frontierSlot.Load()
}

func (s *Sync) syncTick() {
	nowSlot := ledger.TimeNow().Slot

	// find latest committed healthy slot
	healthySlot, found := multistate.FindLatestHealthySlot(s.StateStore(), global.FractionHealthyBranch)
	if !found {
		return
	}

	gap := nowSlot - healthySlot

	// hysteresis: clear sync if gap is small enough
	if gap <= s.thresholdDown {
		if s.isSyncing.Load() {
			s.Log().Infof("[%s] caught up (gap=%d), exiting sync mode", Name, gap)
			s.isSyncing.Store(false)
			s.frontierSlot.Store(0)
			s.branchList = nil
		}
		return
	}

	// enter sync mode if gap exceeds threshold
	if !s.isSyncing.Load() {
		if gap >= s.thresholdUp {
			s.Log().Infof("[%s] entering sync mode (gap=%d, healthy slot=%d, now=%d)", Name, gap, healthySlot, nowSlot)
			s.isSyncing.Store(true)
			s.branchList = nil
		} else {
			return
		}
	}

	// request branch list if empty
	if len(s.branchList) == 0 {
		branches, lrbSlot, err := s.source.GetBranchList(healthySlot, 100)
		if err != nil {
			s.Log().Warnf("[%s] failed to get branch list from sync source: %v", Name, err)
			return
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] sync source returned empty branch list (source LRB slot=%d)", Name, lrbSlot)
			return
		}
		s.branchList = branches
		s.Log().Infof("[%s] received %d branches from sync source (slots %d..%d, source LRB=%d)",
			Name, len(branches), branches[0].Slot(), branches[len(branches)-1].Slot(), lrbSlot)
	}

	// remove already committed branches from head of list
	for len(s.branchList) > 0 {
		_, committed := multistate.FetchRootRecord(s.StateStore(), s.branchList[0])
		if !committed {
			break
		}
		s.branchList = s.branchList[1:]
	}

	if len(s.branchList) == 0 {
		// all branches committed, will re-request next tick if still behind
		return
	}

	// set frontier to the slot of the branch we're syncing
	target := s.branchList[0]
	s.frontierSlot.Store(target.Slot())

	// pull the branch
	s.AddPulledTransaction(target)
	nPeers := s.PullFromPeers(target)
	s.Log().Infof("[%s] pulling branch %s (slot %d), %d remaining, requested from %d peers",
		Name, target.StringShort(), target.Slot(), len(s.branchList)-1, nPeers)
}
