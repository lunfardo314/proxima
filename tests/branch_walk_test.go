package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// TestIterateBranchIDsBackMatchesFullWalk pins IterateBranchIDsBack, the cheap walk that reads only
// each branch's stem, against IterateBranchChainBack, which assembles the full branch data. One
// sequencer produces a chain of branches; from every latest branch both walks must visit the same
// branch IDs in the same order, down to where the chain leaves the store, and stop early the same
// way when the callback says so.
func TestIterateBranchIDsBackMatchesFullWalk(t *testing.T) {
	const maxSlots = 10
	testData := initWorkflowTest(t, 1, true)
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()
	testData.waitStop()

	store := testData.wrk.StateStore()
	latest := multistate.FetchLatestBranches(store)
	require.NotEmpty(t, latest)
	for _, tip := range latest {
		var full, cheap []base.TransactionID
		multistate.IterateBranchChainBack(store, tip, func(branchID *base.TransactionID, _ *multistate.BranchData) bool {
			full = append(full, *branchID)
			return true
		})
		multistate.IterateBranchIDsBack(store, tip.TxID(), func(branchID base.TransactionID) bool {
			cheap = append(cheap, branchID)
			return true
		})
		require.GreaterOrEqual(t, len(full), maxSlots, "the chain is shorter than the branches produced")
		require.Equal(t, full, cheap, "the cheap walk visits other branches than the full one")

		// stopping early: the callback refusing the third branch ends both walks there
		var stopped []base.TransactionID
		multistate.IterateBranchIDsBack(store, tip.TxID(), func(branchID base.TransactionID) bool {
			stopped = append(stopped, branchID)
			return len(stopped) < 3
		})
		require.Equal(t, full[:3], stopped)
	}

	// the cheap walk must read fewer keys than the full one over the same chain
	counting := &readCountingStore{Store: store}
	multistate.IterateBranchChainBack(counting, latest[0], func(*base.TransactionID, *multistate.BranchData) bool { return true })
	fullReads := counting.reads
	counting.reads = 0
	multistate.IterateBranchIDsBack(counting, latest[0].TxID(), func(base.TransactionID) bool { return true })
	t.Logf("store reads over the chain: full walk %d, cheap walk %d", fullReads, counting.reads)
	require.Less(t, counting.reads, fullReads)
}

// readCountingStore counts key reads, to compare what two walks cost.
type readCountingStore struct {
	global.Store
	reads int
}

func (s *readCountingStore) Get(key []byte) []byte {
	s.reads++
	return s.Store.Get(key)
}
