// Package forward_sync implements forward-syncing: sequential branch-by-branch catch-up.
//
// Always-on background process. Monitors the gap between wall-clock slot and the
// latest committed healthy branch. When gap >= thresholdUp, starts pulling branches
// sequentially from trusted API sources. When gap <= thresholdDown, goes idle.
//
// Coexists with recursive pull: recursive attachers handle recent transactions
// (capped at maxAttachmentDepthForPull). When they stall at the depth cap,
// the gap grows, forward-sync kicks in and commits the missing branches,
// satisfying the stalled attachers' dependencies.
package forward_sync

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
	Name = "forward_sync"

	defaultThresholdUp   = 15
	defaultThresholdDown = 3
	defaultPullAhead     = 5
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
		ForceCommitBranch(branchID base.TransactionID)
	}

	Sync struct {
		environment
		sources       []*client.APIClient
		sourceIdx     int // current source index, cycles on failure
		thresholdUp   uint32
		thresholdDown uint32
		pullAhead     int // pull k-th branch ahead to parallelize past cone solidification
		syncing       bool
		// cached branch list (oldest first), protected by the sync loop goroutine (no concurrent access)
		branchList    []base.TransactionID
		currentTarget atomic.Uint32 // slot of the branch we're waiting for
		lastPullTime  time.Time     // when the current target branch was last pulled
		wakeup        chan struct{} // signaled when the target branch commits
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

// Start initializes and starts the sync module. Always active when sources are configured.
func Start(env environment) *Sync {
	sourceURLs := viper.GetStringSlice("sync.sources")
	if len(sourceURLs) == 0 {
		env.Log().Infof("[%s] no sync sources configured, forward-sync inactive", Name)
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
		env.Log().Infof("[%s] all sync sources are self, forward-sync inactive", Name)
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
	pullAhead := viper.GetInt("sync.pull_ahead")
	if pullAhead <= 0 {
		pullAhead = defaultPullAhead
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
		pullAhead:     pullAhead,
		wakeup:        make(chan struct{}, 1),
	}

	go ret.syncLoop()

	env.Log().Infof("[%s] started, sources: %v, threshold up: %d, down: %d, pull ahead: %d", Name, sourceURLs, thUp, thDown, pullAhead)
	return ret
}

// NotifyBranchCommitted wakes up the sync loop only when the current target branch commits.
func (s *Sync) NotifyBranchCommitted(branchSlot uint32) {
	if s == nil {
		return
	}
	if branchSlot < s.currentTarget.Load() {
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

	// hysteresis: go idle if gap is small enough
	if gap <= s.thresholdDown {
		if s.syncing {
			s.Log().Infof("[%s] caught up (gap=%d), going idle", Name, gap)
			s.syncing = false
			s.branchList = nil
		}
		return
	}

	// start syncing if gap exceeds threshold
	if !s.syncing {
		if gap >= s.thresholdUp {
			s.Log().Infof("[%s] starting forward-sync (gap=%d, healthy slot=%d, now=%d)", Name, gap, healthySlot, nowSlot)
			s.syncing = true
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

	// force-commit and remove all committed branches from head of list
	nCommitted := 0
	for len(s.branchList) > 0 {
		s.ForceCommitBranch(s.branchList[0])
		if _, ok := multistate.FetchRootRecord(s.StateStore(), s.branchList[0]); !ok {
			break
		}
		s.branchList = s.branchList[1:]
		nCommitted++
	}
	if nCommitted > 0 {
		s.Log().Infof("[%s] committed %d branches, %d remaining", Name, nCommitted, len(s.branchList))
		s.lastPullTime = time.Time{} // reset so next target is pulled immediately
	}

	if len(s.branchList) == 0 {
		// all branches committed, will re-request next tick if still behind
		return
	}

	// pick the target: k-th branch ahead (or last available)
	targetIdx := s.pullAhead - 1
	if targetIdx >= len(s.branchList) {
		targetIdx = len(s.branchList) - 1
	}
	target := s.branchList[targetIdx]

	// set current target for NotifyBranchCommitted filtering
	s.currentTarget.Store(target.Slot())

	// mark as pulled so it passes rate control as a wanted transaction
	s.AddPulledTransaction(target)

	if s.lastPullTime.IsZero() {
		s.Log().Infof("[%s] pulling branch %s (%d ahead), %d remaining",
			Name, target.StringShort(), targetIdx+1, len(s.branchList)-1)

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
		s.PullFromPeers(target)
		s.lastPullTime = time.Now()
	}
}
