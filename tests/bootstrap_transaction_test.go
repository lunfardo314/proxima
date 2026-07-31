package tests

import (
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// TestBootstrapTransactionAfterGap covers the bootstrap path: when the sequencer's own milestone
// is more than one slot in the past — the cold-restart situation, where nothing arrives from
// gossip and there is nothing to endorse — the first milestone it issues must be a bootstrap
// transaction, i.e. one with an explicit baseline, and it must land in the early ticks of the slot
// so the rest of the slot is left for coverage consolidation on top of it. After that the chain is
// current again and the sequencer produces ordinary milestones and branches.
func TestBootstrapTransactionAfterGap(t *testing.T) {
	const maxSlots = 5

	testData := initWorkflowTest(t, 1)

	// let the chain output age past the bootstrap condition (own milestone stale by more
	// than one slot) with no sequencer running at all — no branches, nothing to endorse
	time.Sleep(3 * ledger.SlotDuration())

	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)

	// the milestone is classified while it is still a live vertex: after the run the memDAG may
	// have pruned it to a form which no longer carries the transaction body
	type milestone struct {
		id          string
		ts          base.LedgerTime
		isBootstrap bool
		isBranch    bool
	}
	var mutex sync.Mutex
	submitted := make([]milestone, 0)
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		mutex.Lock()
		defer mutex.Unlock()
		submitted = append(submitted, milestone{
			id:          ms.IDShortString(),
			ts:          ms.Timestamp(),
			isBootstrap: ms.IsBootstrapMode(),
			isBranch:    ms.IsBranchTransaction(),
		})
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	// Start a few ticks into a slot: with the default one-slot start delay the sequencer's loop
	// then begins in the early ticks of a slot, which is where bootstrap transactions are issued.
	// Beginning at the slot edge instead, its first opportunity is the branch, and the branch
	// proposer can extend the stale branch directly — a valid path out of the gap, but not this one.
	time.Sleep(time.Until(ledger.ClockTime(ledger.TimeNow().NextSlotBoundary().AddTicks(6))))

	// the watcher also reports the tip which was already in the tippool when the sequencer
	// started (the branch it starts from); milestones of this run are the ones after startTs
	startTs := ledger.TimeNow()
	seq.Start()
	testData.waitStop()

	mutex.Lock()
	defer mutex.Unlock()

	issued := make([]milestone, 0, len(submitted))
	numBootstrap := 0
	numBranches := 0
	for _, ms := range submitted {
		if ms.ts.Before(startTs) {
			continue
		}
		issued = append(issued, ms)
		if ms.isBootstrap {
			numBootstrap++
		}
		if ms.isBranch {
			numBranches++
		}
	}
	t.Logf("issued %d milestones: %d bootstrap, %d branches", len(issued), numBootstrap, numBranches)
	require.True(t, len(issued) > 0, "sequencer issued no milestones")

	// inside the slot the gap can only be left by a bootstrap transaction: the base-extend
	// proposer needs a predecessor in the target slot and the factory needs something to
	// endorse, and after the gap there is neither
	require.True(t, issued[0].isBootstrap,
		"first milestone after the gap must be a bootstrap transaction, got %s", issued[0].id)

	// issued early in the slot, leaving the rest of it for consolidation
	require.LessOrEqual(t, int(issued[0].ts.Tick), base.TicksPerSlot/4,
		"bootstrap transaction %s issued outside the early ticks of the slot", issued[0].id)

	// the network takes off from the bootstrap transaction: branches follow it
	require.EqualValues(t, 1, numBootstrap, "bootstrap is left once, then the chain is current again")
	require.True(t, numBranches > 0, "no branch followed the bootstrap transaction")
}
