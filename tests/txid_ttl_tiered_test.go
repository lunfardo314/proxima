package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// TestTieredTxIDPruning runs a single idle sequencer long enough to cross BOTH retention horizons
// (short non-branch and short branch TTLs injected via the ledger options) and verifies the tiered
// GC of claude/txid_ttl_tiered.md end to end:
//   - an old, fully-consumed non-branch milestone is pruned (not known in the latest state);
//   - an old branch is pruned AND its RootRecord deleted atomically;
//   - a recent branch (within the branch horizon) is retained, RootRecord present;
//   - the genesis branch (slot 0) is never pruned — its RootRecord survives (the earliest-retained
//     marker advances past it to the contiguous floor);
//   - the sequencer keeps producing cleanly while its own past records are pruned (the "flip":
//     forgetting an old fully-consumed txid never breaks ongoing attachment, since txIDs are
//     collision-free and a reachable committed dependency always still has a surviving output).
func TestTieredTxIDPruning(t *testing.T) {
	const (
		nonBranchTTL = 8
		branchTTL    = 16
		maxSlots     = 40 // > branchTTL so early branches cross the branch horizon and get pruned
	)
	// Reinit the ledger with short retention so a short run crosses both GC horizons.
	ledger.ResetForTesting()
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
		ledger.WithTxIDStateTTLSlots(nonBranchTTL),
		ledger.WithBranchTxIDStateTTLSlots(branchTTL),
	)
	defer func() { reinitTestLedger() }()

	testData := initWorkflowTest(t, 1, true)

	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)

	// Collect, in order, every branch the sequencer produces, the first non-branch milestone, and
	// the most recent branch.
	var mu sync.Mutex
	var branchIDs []base.TransactionID
	var firstSeq, lastBranch base.TransactionID
	var haveFirstSeq bool
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		mu.Lock()
		defer mu.Unlock()
		id := ms.ID()
		if ms.IsBranchTransaction() {
			branchIDs = append(branchIDs, id)
			lastBranch = id
		} else if !haveFirstSeq {
			firstSeq, haveFirstSeq = id, true
		}
	})
	seq.OnExitOnce(func() { testData.stop() })
	seq.Start()
	testData.waitStop()

	require.NoError(t, testData.wrk.EnsureLatestBranches())
	require.True(t, haveFirstSeq && len(branchIDs) > 0)

	store := testData.wrk.StateStore()
	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	latest := multistate.FetchLatestCommittedSlot(store)

	// Pick a fully-consumed sequencer branch that is old enough to have crossed the branch horizon
	// (slot well below latest-branchTTL) and well above the anchor / distribution branch.
	var oldBranch base.TransactionID
	var haveOldBranch bool
	for _, id := range branchIDs {
		if id.Slot() >= 5 && id.Slot()+branchTTL+2 <= latest {
			oldBranch, haveOldBranch = id, true
			break
		}
	}
	require.True(t, haveOldBranch, "need an old fully-consumed sequencer branch to test")

	// The genesis branch (slot 0) is never a prune target, so its RootRecord must survive regardless
	// of age (the earliest-retained-slot marker advances past it to the contiguous floor).
	_, genesisFound := multistate.FetchRootRecord(store, base.GenesisTransactionID())
	require.True(t, genesisFound, "genesis RootRecord must survive pruning")

	// The distribution branch (slot 1) is old but has permanently-unspent outputs (the distributed
	// funds), so it is correctly NOT pruned regardless of age — a txID with any live output keeps its
	// record. This is the load-bearing invariant behind the whole tiered design.
	require.True(t, rdr.KnowsCommittedTransaction(testData.distributionBranchTxID), "branch with live outputs must be retained")
	_, distFound := multistate.FetchRootRecord(store, testData.distributionBranchTxID)
	require.True(t, distFound, "branch with live outputs keeps its RootRecord")

	// Recent branch is within the branch horizon: known + RootRecord present.
	require.True(t, rdr.KnowsCommittedTransaction(lastBranch), "recent branch must be retained")
	_, lastFound := multistate.FetchRootRecord(store, lastBranch)
	require.True(t, lastFound, "recent branch RootRecord must be present")

	// Old fully-consumed sequencer branch: trie record pruned AND RootRecord deleted atomically.
	require.False(t, rdr.KnowsCommittedTransaction(oldBranch), "old fully-consumed branch txID must be pruned")
	_, oldBranchFound := multistate.FetchRootRecord(store, oldBranch)
	require.False(t, oldBranchFound, "old branch RootRecord must be deleted atomically with the trie prune")

	// First sequencer (non-branch) milestone is old and fully consumed: pruned at the short horizon.
	require.False(t, rdr.KnowsCommittedTransaction(firstSeq), "old fully-consumed non-branch milestone must be pruned")
}
