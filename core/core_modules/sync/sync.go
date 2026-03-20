// Package sync implements sequential branch-by-branch syncing.
// When the node falls behind by more than SyncThresholdUp slots, the sync module
// requests a branch list from a trusted API source and pulls branches one at a time.
// Transactions with timestamps beyond the current sync frontier are dropped from attachment.
package sync

import (
	"fmt"
	"strings"
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
	pullRepeatInterval   = 5 * time.Second
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		TxBytesStore() global.TxBytesStore
		TxBytesFromStoreIn(txBytesWithMetadata []byte) (base.TransactionID, error)
		PullFromPeers(txid base.TransactionID) int
		AddPulledTransaction(txid base.TransactionID)
	}

	Sync struct {
		environment
		sources       []*client.APIClient
		sourceIdx     int // current source index, cycles on failure
		thresholdUp   uint32
		thresholdDown uint32
		isSyncing     atomic.Bool
		// cached branch list (oldest first), protected by the sync loop goroutine (no concurrent access)
		branchList   []base.TransactionID
		frontierSlot atomic.Uint32
		lastPullTime time.Time    // when the current branch was last pulled
		wakeup       chan struct{} // signaled when a branch commits
	}
)

// IsSelfURL returns true if the URL points to this node's own API
func IsSelfURL(url string, selfAPIPort int) bool {
	selfSuffix := fmt.Sprintf(":%d", selfAPIPort)
	for _, prefix := range []string{"http://127.0.0.1", "http://localhost", "https://127.0.0.1", "https://localhost"} {
		if strings.HasPrefix(url, prefix+selfSuffix) {
			return true
		}
	}
	return false
}

// Start initializes and starts the sync module. If no sync sources configured, the module is inactive.
func Start(env environment) *Sync {
	sourceURLs := viper.GetStringSlice("sync.sources")
	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] no sync sources configured, sync module inactive", Name)
		return nil
	}

	// filter out self
	selfAPIPort := viper.GetInt("api.port")
	filtered := make([]string, 0, len(sourceURLs))
	for _, url := range sourceURLs {
		if IsSelfURL(url, selfAPIPort) {
			env.Log().Infof("[%s] skipping self URL %s", Name, url)
			continue
		}
		filtered = append(filtered, url)
	}
	sourceURLs = filtered

	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] all sync sources are self, sync module inactive", Name)
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

	sources := make([]*client.APIClient, len(sourceURLs))
	for i, url := range sourceURLs {
		sources[i] = client.NewWithGoogleDNS(url, 10*time.Second)
	}

	ret := &Sync{
		environment:   env,
		sources:       sources,
		thresholdUp:   uint32(thUp),
		thresholdDown: uint32(thDown),
		wakeup:        make(chan struct{}, 1),
	}

	go ret.syncLoop()

	env.Log().Infof("[%s] started, sources: %v, threshold up: %d, down: %d", Name, sourceURLs, thUp, thDown)
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

// NotifyBranchCommitted wakes up the sync loop to check for the next branch immediately.
func (s *Sync) NotifyBranchCommitted() {
	if s == nil {
		return
	}
	select {
	case s.wakeup <- struct{}{}:
	default:
	}
}

// syncLoop runs until shutdown. Wakes up on branch commit notification or periodic timer.
func (s *Sync) syncLoop() {
	timer := time.NewTimer(syncLoopPeriod)
	defer timer.Stop()

	for {
		select {
		case <-s.Ctx().Done():
			return
		case <-s.wakeup:
		case <-timer.C:
		}
		s.syncTick()
		timer.Reset(syncLoopPeriod)
	}
}

// requestBranchList tries each source starting from the current index, cycling on failure.
// A source that returns an empty list (its LRB is not ahead of fromSlot) is skipped.
func (s *Sync) requestBranchList(fromSlot uint32) ([]base.TransactionID, uint32, error) {
	n := len(s.sources)
	var lastErr error
	for i := 0; i < n; i++ {
		idx := (s.sourceIdx + i) % n
		branches, lrbSlot, err := s.sources[idx].GetBranchList(fromSlot, 100)
		if err != nil {
			lastErr = err
			s.Log().Warnf("[%s] source %d failed: %v, trying next", Name, idx, err)
			continue
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] source %d returned empty list (LRB slot=%d <= from_slot=%d), trying next", Name, idx, lrbSlot, fromSlot)
			continue
		}
		s.sourceIdx = idx
		return branches, lrbSlot, nil
	}
	// advance index for next attempt so we don't always start from the same source
	s.sourceIdx = (s.sourceIdx + 1) % n
	if lastErr != nil {
		return nil, 0, fmt.Errorf("all %d sync sources failed, last error: %v", n, lastErr)
	}
	return nil, 0, nil
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
		branches, lrbSlot, err := s.requestBranchList(healthySlot)
		if err != nil {
			s.Log().Warnf("[%s] %v", Name, err)
			return
		}
		if len(branches) == 0 {
			s.Log().Infof("[%s] sync source returned empty branch list (LRB slot=%d)", Name, lrbSlot)
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
		s.Log().Infof("[%s] branch %s committed, %d remaining", Name, s.branchList[0].StringShort(), len(s.branchList)-1)
		s.branchList = s.branchList[1:]
		s.lastPullTime = time.Time{} // reset so next branch is pulled immediately
	}

	if len(s.branchList) == 0 {
		// all branches committed, will re-request next tick if still behind
		return
	}

	// set frontier to the slot of the branch we're syncing
	target := s.branchList[0]
	s.frontierSlot.Store(target.Slot())

	// mark as pulled so it passes the sync filter if it arrives via gossip
	s.AddPulledTransaction(target)

	if s.lastPullTime.IsZero() {
		s.Log().Infof("[%s] pulling branch %s, %d remaining", Name, target.StringShort(), len(s.branchList)-1)

		// try local txstore first — the branch may already be there from gossip
		if txBytes := s.TxBytesStore().GetTxBytesWithMetadata(&target); len(txBytes) > 0 {
			if _, err := s.TxBytesFromStoreIn(txBytes); err != nil {
				s.Log().Warnf("[%s] re-inject from txstore failed: %v", Name, err)
			}
		} else {
			s.PullFromPeers(target)
		}
		s.lastPullTime = time.Now()
	} else if time.Since(s.lastPullTime) >= pullRepeatInterval {
		// re-pull after timeout if not yet committed
		s.PullFromPeers(target)
		s.lastPullTime = time.Now()
	}
}
