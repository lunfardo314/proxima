package forward_sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// stallTestEnv is a node whose state holds only genesis and which can never obtain a branch:
// ForceCommitBranch commits nothing and pulls go nowhere. That is the situation of a node whose
// sync source listed branches and then went away, or listed branches nobody has.
type stallTestEnv struct {
	*global.Global
	stateStore   global.Store
	txBytesStore global.TxBytesStore
	pulls        atomic.Int64
}

func (e *stallTestEnv) StateStore() global.Store          { return e.stateStore }
func (e *stallTestEnv) TxBytesStore() global.TxBytesStore { return e.txBytesStore }
func (e *stallTestEnv) TxBytesFromStoreIn([]byte) (base.TransactionID, error) {
	return base.TransactionID{}, nil
}
func (e *stallTestEnv) PullFromPeers(base.TransactionID) int    { e.pulls.Add(1); return 0 }
func (e *stallTestEnv) AddPulledTransaction(base.TransactionID) {}
func (e *stallTestEnv) ForceCommitBranch(base.TransactionID)    {}
func (e *stallTestEnv) LatestBranchSlotFromPeers() uint32       { return 3 }

// fakeSource serves get_branch_list over a fixed chain, oldest first, the way a real source walks
// back from to_branch. The first failFirst requests fail, as when the source is unreachable.
type fakeSource struct {
	chain     []base.TransactionID
	failFirst int
	mutex     sync.Mutex
	nRequests int
	requests  []string // "<to_branch short>@<from_slot>"
}

func (f *fakeSource) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mutex.Lock()
	f.nRequests++
	n := f.nRequests
	f.mutex.Unlock()

	var resp api.BranchList
	toBranch, err := base.TransactionIDFromHexString(r.URL.Query().Get("to_branch"))
	fromSlot, _ := strconv.Atoi(r.URL.Query().Get("from_slot"))
	f.mutex.Lock()
	f.requests = append(f.requests, toBranch.StringShort()+"@"+strconv.Itoa(fromSlot))
	f.mutex.Unlock()
	switch {
	case n <= f.failFirst:
		resp.Error.Error = "source unreachable"
	case err != nil:
		resp.Error.Error = "bad to_branch"
	default:
		for _, b := range f.chain {
			if int(b.Slot()) >= fromSlot {
				resp.Branches = append(resp.Branches, b.StringHex())
			}
			if b == toBranch {
				break
			}
		}
		resp.TopSlot = toBranch.Slot()
	}
	data, _ := json.Marshal(resp)
	_, _ = w.Write(data)
}

func (f *fakeSource) sawRequest(req string) bool {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	for _, r := range f.requests {
		if r == req {
			return true
		}
	}
	return false
}

// newStallTest builds a sync process over a genesis-only state, a source serving the chain
// genesis -> b1 -> b2 -> target, and registers target as the pending sync target.
func newStallTest(t *testing.T, failFirst int) (*Sync, *stallTestEnv, *fakeSource, base.TransactionID) {
	ledger.InitWithTestingLedgerData()
	stateStore := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStore)
	_, genesis := multistate.FetchEarliestBranchIDList(stateStore)
	require.Len(t, genesis, 1)

	b1 := base.RandomTransactionID(true, 0, base.LedgerTime{Slot: 1})
	b2 := base.RandomTransactionID(true, 0, base.LedgerTime{Slot: 2})
	target := base.RandomTransactionID(true, 0, base.LedgerTime{Slot: 3})
	src := &fakeSource{chain: []base.TransactionID{genesis[0], b1, b2, target}, failFirst: failFirst}
	server := httptest.NewServer(src)
	t.Cleanup(server.Close)

	env := &stallTestEnv{
		Global:       global.NewDefault(),
		stateStore:   stateStore,
		txBytesStore: txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore()),
	}
	s := &Sync{
		environment: env,
		sources:     []*client.APIClient{client.NewWithGoogleDNS(server.URL)},
		sourceURLs:  []string{server.URL},
		pullAhead:   defaultPullAhead,
		commitBatch: defaultCommitBatch,
		wakeup:      make(chan struct{}, 1),
	}
	require.True(t, global.AddSyncTarget(target))
	t.Cleanup(func() { global.RemoveSyncTarget(target) })
	return s, env, src, target
}

func targetPending(target base.TransactionID) bool {
	lowest, ok := global.LowestSyncTarget()
	return ok && lowest == target
}

// TestStallReapsTargetWhoseBranchesNeverArrive: the source lists the branches up to the target,
// but none of them can be fetched. The node must count those ticks as stalled and reap the target
// within the reap budget. Before, the stall counter was reset on every tick while a branch list was
// held, so the node re-pulled the window forever and stayed in sync mode until restarted.
func TestStallReapsTargetWhoseBranchesNeverArrive(t *testing.T) {
	s, env, _, target := newStallTest(t, 0)

	ticks := 0
	for ; ticks < syncTargetReapStallTicks+10 && targetPending(target); ticks++ {
		s.syncTick()
	}
	require.Positive(t, env.pulls.Load(), "the listed branches were never pulled")
	require.False(t, targetPending(target), "target still pending after %d ticks without a commit", ticks)
	require.True(t, global.SyncTargetReapedRecently(target))
}

// TestFailedProbeIsRetried: the fork probe of a new target fails because the source is briefly
// unreachable. The target must not be driven from the floor left by a previous target: the next
// tick probes again and starts from the common branch it finds (genesis, slot 0). Before, the target
// was adopted before the probe and the next tick requested its chain from the stale floor.
func TestFailedProbeIsRetried(t *testing.T) {
	s, _, src, target := newStallTest(t, 1)
	s.syncedToSlot = 2 // floor left over from a previous target

	s.syncTick()
	require.Equal(t, base.TransactionID{}, s.driveTarget, "target adopted although its probe failed")

	s.syncTick()
	require.Equal(t, target, s.driveTarget)
	require.Zero(t, s.syncedToSlot, "floor not taken from the probe")
	require.False(t, src.sawRequest(target.StringShort()+"@2"), "chain requested from the stale floor")
	require.NotEmpty(t, s.branchList)
}
