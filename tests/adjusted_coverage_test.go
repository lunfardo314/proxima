package tests

import (
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// Tests of the adjusted coverage (kb/dag_semantics.md, adjusted coverage; kb/coverage_adjustment_B.md):
// the sequencers' decision score, computed beside the classical coverage. Each test builds one slot of
// sequencer chains on the distribution branch, endorsing each other in a ring, cuts branches out of
// it, and checks the score against values derived from the outputs by hand.

// attachGood attaches a transaction (its past cone is pulled from the txstore) and waits for the result.
func attachGood(t *testing.T, td *longConflictTestData, tx *transaction.Transaction) *vertex.WrappedTx {
	var wg sync.WaitGroup
	wg.Add(1)
	vid := attacher.AttachTransaction(tx, td.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
		wg.Done()
	}))
	wg.Wait()
	require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String(), "%s: %v", tx.IDShortString(), vid.GetError())
	return vid
}

// handedIn is what the adjusted coverage counts for a milestone's sequencer output handed into a
// branch's history from outside it: its weight without the inflation the milestone minted, which is not
// on the ledger yet.
func handedIn(tx *transaction.Transaction) uint64 {
	o := tx.SequencerOutput()
	return ledger.Coverage(o.Output, o.ID, tx.Timestamp()) - tx.InflationAmount()
}

// unconsumedBonus is the baseline adjustment of a transaction that leaves the branch's sequencer output
// unconsumed: the inflation of that output.
func unconsumedBonus(branch *transaction.Transaction) uint64 {
	return branch.SequencerOutput().Output.Inflation()
}

// buildSlot starts nChains sequencer chains and stores them in the txstore. The first outsideRing chains
// stay at their first milestone; the others go on for howLong milestones, endorsing each other in a
// ring, and are returned as slot. The harness builds milestones without a declared coverage delta, so
// the caller runs on a test ledger without the coverage-advance rule
// (reinitTestLedgerNoCoverageMonotonicity).
func buildSlot(t *testing.T, nChains, outsideRing, howLong int, withTagAlong bool) (td *longConflictTestData, begin []*transaction.Transaction, slot [][]*transaction.Transaction) {
	td = initLongConflictTestData(t, nChains, nChains, 0)
	td.makeSeqBeginnings(false)
	td.txBytesAttach()
	begin = make([]*transaction.Transaction, nChains)
	for i := range begin {
		begin[i] = td.seqChain[i][0]
	}
	td.storeTransactions(begin...)
	if withTagAlong {
		slot = td.makeSlotTransactionsWithTagAlong(howLong, begin[outsideRing:])
		td.storeTransactions(td.transferChain...)
	} else {
		slot = td.makeSlotTransactions(howLong, begin[outsideRing:])
	}
	for _, chain := range slot {
		td.storeTransactions(chain...)
	}
	return
}

// firstMove builds, with an incremental attacher, a follower's move that extends its own milestone and
// endorses a branch, and returns its classical coverage and its score. The follower's chain output is
// the only value it hands into the branch's history, so the score must be the branch's halved plus that
// output (without its inflation) and the unconsumed branch bonus. Both transactions are attached
// already: attaching a known one again would not call back.
func firstMove(t *testing.T, td *longConflictTestData, follower, branch *transaction.Transaction, vidFollower, vidBranch *vertex.WrappedTx) (coverage, score uint64) {
	ts := branch.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
	a, err := attacher.NewIncrementalAttacher("test-first-move", td.wrk, ts, vidFollower.SequencerWrappedOutput(), vidBranch)
	require.NoError(t, err)
	defer a.Close()
	score, ok := a.FinalScore(ts)
	require.True(t, ok)
	blScore := vidBranch.GetScoreP()
	require.NotNil(t, blScore)
	require.EqualValues(t, handedIn(follower)+unconsumedBonus(branch), score-*blScore>>1)
	return a.FinalLedgerCoverage(ts), score
}

// TestAdjustedCoverageFollowerAfterCut: a branch L extends the last milestone of chain 0. The last
// milestones of the other chains came after L's cut, so they are outside L's history. A follower that
// extends its own last milestone and endorses L brings in only that milestone's chain output: its
// adjusted delta is exactly that output's weight (plus the unconsumed branch bonus). A follower on the
// branch's own chain consumes the branch's output, inside the branch's state, and scores like the
// classical coverage. One slot later the same holds for outputs carrying chain inflation, earned by a
// move across the slot boundary: they count without it.
func TestAdjustedCoverageFollowerAfterCut(t *testing.T) {
	defer reinitTestLedgerNoCoverageMonotonicity()()
	const nChains, howLong = 3, 3
	td, _, slot := buildSlot(t, nChains, 0, howLong, false)

	branchL := td.makeBranch(slot[0][howLong-1].SequencerOutput().MustAsChainOutput(), td.distributionBranchTx)
	td.storeTransactions(branchL)
	vidL := attachGood(t, td, branchL)
	scoreL := vidL.GetScoreP()
	require.NotNil(t, scoreL, "a branch attached by this node has a score")

	// the first moves of the next slot, attached by the milestone attacher. Branch M extends chain 0's
	// first move only; making it attaches that move (computeStemAggregates), the loop attaches the others.
	next := td.extendToNextSlot(slot, branchL)
	td.storeTransactions(next...)
	branchM := td.makeBranch(next[0].SequencerOutput().MustAsChainOutput(), branchL)
	td.storeTransactions(branchM)
	vidNext := make([]*vertex.WrappedTx, len(next))
	for i, tx := range next {
		vid := td.wrk.GetVertex(tx.ID())
		if i > 0 {
			vid = attachGood(t, td, tx)
		}
		require.NotNil(t, vid)
		vidNext[i] = vid
		score := vid.GetScoreP()
		require.NotNil(t, score)
		inherited := *scoreL >> 1
		if i == 0 {
			// extends the branch's own output: what the classical coverage counts too
			require.EqualValues(t, vid.GetLedgerCoverage()-vidL.GetLedgerCoverage()>>1, *score-inherited)
			continue
		}
		expected := handedIn(slot[i][howLong-1]) + unconsumedBonus(branchL)
		require.EqualValues(t, expected, *score-inherited, "follower %d", i)
	}

	// the other first moves crossed the slot boundary, so they mint chain inflation; they are outside
	// M's history and are handed in one slot later
	vidM := attachGood(t, td, branchM)
	for i := 1; i < nChains; i++ {
		inflation := next[i].InflationAmount()
		t.Logf("follower %d: handed-in output carries inflation %d", i, inflation)
		require.Positive(t, inflation, "the case needs a handed-in output carrying inflation")
		firstMove(t, td, next[i], branchM, vidNext[i], vidM)
	}
	td.stopAndWait()
}

// TestAdjustedCoverageEarlyCut is the case the adjustment exists for. A first slot of sequencer chains
// is committed by branch P, so the chains' capital is in P's state. In the next slot chain 0 stays at
// its first move, the others go on in a ring, and two branches compete on the following slot edge: E
// extends chain 0's first move (an early cut, holding none of the slot's activity), L the last
// milestone of a ring chain. A follower, the other ring chain, extends its last milestone and endorses
// one of them. By classical coverage the follower gains more on E: the slot's spends of P's state,
// missing from E's state, count in full in the follower's delta, while L has them already, at half. By
// the adjusted coverage only the value the follower hands into the branch's history counts, its own
// chain output, and L wins.
func TestAdjustedCoverageEarlyCut(t *testing.T) {
	defer reinitTestLedgerNoCoverageMonotonicity()()
	const nChains, howLong = 3, 3
	td, _, slot := buildSlot(t, nChains, 0, howLong, false)

	branchP := td.makeBranch(slot[0][howLong-1].SequencerOutput().MustAsChainOutput(), td.distributionBranchTx)
	td.storeTransactions(branchP)
	attachGood(t, td, branchP)
	next := td.extendToNextSlot(slot, branchP)
	td.storeTransactions(next...)
	ring := td.makeSlotTransactions(howLong, next[1:])
	for _, chain := range ring {
		td.storeTransactions(chain...)
	}

	branchE := td.makeBranch(next[0].SequencerOutput().MustAsChainOutput(), branchP)
	branchL := td.makeBranch(ring[0][howLong-1].SequencerOutput().MustAsChainOutput(), branchP)
	require.EqualValues(t, branchE.Slot(), branchL.Slot(), "the two branches compete on one slot edge")
	td.storeTransactions(branchE, branchL)
	vidE := attachGood(t, td, branchE)
	vidL := attachGood(t, td, branchL)
	follower := ring[1][howLong-1]
	vidFollower := attachGood(t, td, follower)
	covE, scoreE := firstMove(t, td, follower, branchE, vidFollower, vidE)
	covL, scoreL := firstMove(t, td, follower, branchL, vidFollower, vidL)
	t.Logf("on E: coverage %d, score %d", covE, scoreE)
	t.Logf("on L: coverage %d, score %d", covL, scoreL)

	require.Greater(t, covE, covL, "classical coverage rewards the early cut")
	require.Greater(t, scoreL, scoreE, "the adjusted coverage does not")
	td.stopAndWait()
}

// TestAdjustedCoverageNonSequencerPayment: each milestone takes a tag-along from a chain of transfers,
// each transfer spending the previous one's remainder, and the chains start from a non-sequencer
// transaction producing their origins. A non-sequencer transaction one of whose outputs a member of
// the inside set consumes is a member itself, so the transfers, the transaction behind the first one
// and the chain origins transaction are all inside: their spends of baseline-state outputs (the fork
// output, the aux output) count where they are made, as in the classical coverage. Nothing in this
// branch's history comes from another lineage, so its score equals its classical coverage.
func TestAdjustedCoverageNonSequencerPayment(t *testing.T) {
	defer reinitTestLedgerNoCoverageMonotonicity()()
	const nChains, howLong = 2, 3
	td, _, slot := buildSlot(t, nChains, 0, howLong, true)

	branch := td.makeBranch(slot[0][howLong-1].SequencerOutput().MustAsChainOutput(), td.distributionBranchTx)
	td.storeTransactions(branch)
	vid := attachGood(t, td, branch)
	score := vid.GetScoreP()
	require.NotNil(t, score)
	t.Logf("coverage %d, score %d", vid.GetLedgerCoverage(), *score)
	require.EqualValues(t, vid.GetLedgerCoverage(), *score)
	td.stopAndWait()
}
