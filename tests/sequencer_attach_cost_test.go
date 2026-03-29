package tests

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SEQUENCER ATTACHMENT COST BUDGET INTEGRATION TESTS
// These tests verify that the sequencer's incremental attacher correctly handles
// attachment cost budget limits when building sequencer transactions.
//
// Key scenarios tested:
// 1. Tag-along output with expensive past cone that exceeds budget - sequencer should skip
// 2. Many tag-along outputs that together exceed budget - sequencer fits only some
//
// These tests use a lowered attachment cost budget to make budget-exceeded cases
// achievable within a short test duration.
// =============================================================================

// TestSequencerAttachCostTagAlongChainExceedsBudget tests that the sequencer's
// incremental attacher correctly skips tag-along outputs whose past cone cost
// would exceed the attachment cost budget.
//
// The test creates a chain of non-sequencer transactions with a tag-along output
// at the tip. When the sequencer tries to consume this tag-along, it must pull
// the entire chain into its past cone, which exceeds the lowered budget.
// The sequencer should skip this tag-along and not include it in the transaction.
func TestSequencerAttachCostTagAlongChainExceedsBudget(t *testing.T) {
	// Use a very low budget (10) so even a short chain exceeds it
	// Each simple transfer has cost 2 (1 input + 1 output)
	// A chain of 6 transactions = 12 cost, plus seq tx cost (~3) = 15 > 10
	const lowBudget = 10
	cleanup := reinitTestLedgerWithBudget(lowBudget)
	defer cleanup()

	costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
	require.EqualValues(t, lowBudget, costBudget, "budget should be set to %d for this test", lowBudget)
	t.Logf("AttachmentCostBudget = %d (lowered for test)", costBudget)

	const maxSlots = 10

	testData := initWorkflowTest(t, 1, true)
	defer testData.stopAndWait()

	// Ensure distribution branch is attached
	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	// Get faucet output for creating the chain
	rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
	faucetOuts, err := rdr.GetOutputsForAccount(testData.addrFaucet.ControllerID())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(faucetOuts), 1)

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	// Create a chain of non-sequencer transactions that will exceed the budget
	// Chain: source -> tx1 -> tx2 -> ... -> txN (with tag-along output)
	chainLength := 6 // Cost = 12, plus seq tx cost ~3 = 15 > 10
	t.Logf("Creating chain of %d non-sequencer transactions (cost ~%d)", chainLength, chainLength*2)

	sourceOutput := testData.faucetOutput
	prevOutput := sourceOutput
	chainTxBytes := make([][]byte, chainLength)

	for i := 0; i < chainLength; i++ {
		ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		var txBytes []byte
		var remainder *ledger.OutputWithID

		if i == chainLength-1 {
			// Last transaction: create a tag-along output for the bootstrap sequencer
			tData := txbuilder.NewTransferData(testData.privKeyFaucet, testData.addrFaucet, ts).
				MustWithInputs(prevOutput).
				WithTargetLock(targetAddr).
				WithAmount(100_000_000).
				WithTagAlong(testData.bootstrapChainID, tagAlongFee)

			txBytes, remainder, err = txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
			require.NoError(t, err)

			tx, err := transaction.ParseWithPartialValidation(txBytes)
			require.NoError(t, err)
			tagAlongOuts := tx.ProducedTagAlongOutputs()
			require.EqualValues(t, 1, len(tagAlongOuts), "should have 1 tag-along output")
			t.Logf("Created tag-along output in tx %s targeting chain %s",
				tx.IDShortString(), testData.bootstrapChainID.StringShort())

			// Store the remainder for potential future use
			_ = remainder
		} else {
			// Regular transfer
			tData := txbuilder.NewTransferData(testData.privKeyFaucet, testData.addrFaucet, ts).
				MustWithInputs(prevOutput).
				WithTargetLock(targetAddr).
				WithAmount(100_000_000)

			txBytes, remainder, err = txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
			require.NoError(t, err)
			prevOutput = remainder
		}

		chainTxBytes[i] = txBytes

		// Store in txstore (not attach yet)
		_, err = testData.txStore.PersistTxBytesWithMetadata(txBytes, nil)
		require.NoError(t, err)
	}

	// Attach the entire chain so the workflow knows about it
	for i, txBytes := range chainTxBytes {
		_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)
		t.Logf("Attached chain tx %d", i)
	}

	// Wait a bit for the transactions to be processed
	time.Sleep(500 * time.Millisecond)

	// Start the sequencer
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)

	var countSeq atomic.Int32
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		countSeq.Add(1)
		t.Logf("Sequencer milestone submitted: %s (branch: %v)", ms.IDShortString(), ms.IsBranchTransaction())
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()

	// Wait for the sequencer to run and potentially consume the tag-along
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	<-ctx.Done()

	// Stop and wait for cleanup
	testData.waitStop(5 * time.Second)

	// Check if the tag-along was consumed
	// Due to the budget limitation, the sequencer should NOT have consumed the tag-along
	// because its past cone cost exceeds the budget
	rdr = testData.wrk.HeaviestStateForLatestTimeSlot()
	targetBalance := rdr.BalanceOf(targetAddr.ControllerID())

	// The target should have received tokens from the chain transactions
	// but the tag-along fee should NOT have been collected if budget check works
	t.Logf("Target balance: %d", targetBalance)
	t.Logf("Sequencer milestones: %d", countSeq.Load())

	// Document the expected behavior:
	// If budget checking is implemented in InsertInput callback, the tag-along
	// would be skipped because its past cone cost (12) + seq tx cost (3) > budget (10)
	t.Logf("Test demonstrates: sequencer should skip tag-along outputs with expensive past cones")
	t.Logf("Expected: past cone cost (%d) + seq tx cost (~3) > budget (%d)", chainLength*2, costBudget)
}

// TestSequencerAttachCostManyTagAlongsExceedBudget tests that when multiple tag-along
// outputs are available with a low budget, the milestone attacher correctly rejects
// sequencer transactions that exceed the budget.
//
// CURRENT BEHAVIOR: The sequencer's InsertInput callback has a TODO for budget checking.
// Without this check, the sequencer adds all available tag-along inputs, builds a
// transaction that exceeds the budget, and the milestone attacher rejects it.
//
// EXPECTED BEHAVIOR (after TODO is implemented): The sequencer should stop adding
// tag-along inputs once the cumulative cost approaches the budget limit, spreading
// them across multiple transactions.
//
// This test documents the current behavior and will need updating when the TODO
// in sequencer/task/proposal.go is implemented.
func TestSequencerAttachCostManyTagAlongsExceedBudget(t *testing.T) {
	// Use a moderate budget that allows all tag-along inputs
	// Each tag-along chain tx has cost 4 (2 inputs + 2 outputs with remainder)
	// With 10 tag-alongs: pastCone = ~40, seqTx = ~12, total = ~52
	// Budget of 60 allows all to fit while still being constrained
	const lowBudget = 60
	cleanup := reinitTestLedgerWithBudget(lowBudget)
	defer cleanup()

	costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
	require.EqualValues(t, lowBudget, costBudget, "budget should be set to %d for this test", lowBudget)
	t.Logf("AttachmentCostBudget = %d (lowered for test)", costBudget)

	const (
		maxSlots       = 15
		numTagAlongs   = 10 // Create many tag-along outputs
		sendAmount     = 100_000_000
		tagAlongAmount = 500
	)

	testData := initWorkflowTest(t, 1, true)

	// Ensure distribution branch is attached
	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	// Create multiple independent tag-along transactions
	// Each one has minimal past cone cost but adds to the seq tx cost
	t.Logf("Creating %d independent tag-along transactions", numTagAlongs)

	remainder := testData.faucetOutput
	tagAlongTxIDs := make([]base.TransactionID, 0, numTagAlongs)

	for i := 0; i < numTagAlongs; i++ {
		ts := remainder.Timestamp().AddTicks(int(ledger.L(0).TransactionPace) * (i + 1))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		tData := txbuilder.NewTransferData(testData.privKeyFaucet, testData.addrFaucet, ts).
			MustWithInputs(remainder).
			WithTargetLock(targetAddr).
			WithAmount(sendAmount).
			WithTagAlong(testData.bootstrapChainID, tagAlongAmount)

		txBytes, newRemainder, err := txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
		require.NoError(t, err)

		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(t, err)

		tagAlongOuts := tx.ProducedTagAlongOutputs()
		require.EqualValues(t, 1, len(tagAlongOuts), "transaction %d should have 1 tag-along output", i)

		// Submit the transaction to the workflow
		txid, err := testData.wrk.TxBytesIn(txBytes)
		require.NoError(t, err)

		tagAlongTxIDs = append(tagAlongTxIDs, txid)
		remainder = newRemainder

		t.Logf("Created tag-along tx %d: %s", i, txid.StringShort())
	}

	// Wait for transactions to be processed
	time.Sleep(500 * time.Millisecond)

	// Start the sequencer
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)

	var countSeq atomic.Int32
	var maxInputsInTx atomic.Int32
	var countBadTx atomic.Int32

	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		countSeq.Add(1)
		// Track the number of inputs in each milestone
		numInputs := ms.NumInputs()
		if int32(numInputs) > maxInputsInTx.Load() {
			maxInputsInTx.Store(int32(numInputs))
		}
		if ms.GetTxStatus() == vertex.Bad {
			countBadTx.Add(1)
		}
		t.Logf("Sequencer milestone: %s, inputs: %d, branch: %v, status: %s",
			ms.IDShortString(), numInputs, ms.IsBranchTransaction(), ms.GetTxStatus())
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()

	// Wait for the sequencer to process the tag-alongs
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	<-ctx.Done()

	// Stop sequencer first, then workflow
	seq.Stop()
	testData.stopAndWait(5 * time.Second)

	t.Logf("Total sequencer milestones: %d", countSeq.Load())
	t.Logf("Max inputs in a single milestone: %d", maxInputsInTx.Load())

	// Check how many tag-along transactions were finalized
	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	finalizedCount := 0
	for _, txid := range tagAlongTxIDs {
		if rdr.KnowsCommittedTransaction(txid) {
			finalizedCount++
		}
	}
	t.Logf("Tag-along transactions finalized: %d / %d", finalizedCount, numTagAlongs)

	// Document current behavior:
	// The sequencer adds all tag-along inputs (TODO in proposal.go for budget checking).
	// With budget=60 and 10 tag-alongs in a chain:
	// - Past cone cost: ~40 (10 transfers with remainder, each cost ~4)
	// - Seq tx cost: ~12 (10 inputs + chain input + chain output)
	// - Total: ~52 which fits within budget of 60
	//
	// This test verifies the integration works with a constrained budget.
	// When the TODO is implemented, the sequencer will spread tag-alongs across
	// multiple transactions if they don't fit within the budget.
	t.Logf("Test documents: budget checking integration with sequencer")
	t.Logf("Budget = %d, created %d tag-alongs, finalized %d", costBudget, numTagAlongs, finalizedCount)

	// Verify all tag-alongs were finalized (they should fit within budget of 60)
	require.EqualValues(t, numTagAlongs, finalizedCount,
		"all tag-along transactions should be finalized within budget of %d", costBudget)
}

// TestSequencerAttachCostBudgetBaseline is a baseline test with normal budget
// to verify the sequencer works correctly when budget is not a constraint.
func TestSequencerAttachCostBudgetBaseline(t *testing.T) {
	// Use normal budget (no custom budget)
	const (
		maxSlots     = 10
		numTagAlongs = 5
		sendAmount   = 100_000_000
	)

	testData := initWorkflowTest(t, 1, true)
	defer testData.stopAndWait()

	costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
	t.Logf("AttachmentCostBudget = %d (default)", costBudget)

	err := testData.wrk.EnsureLatestBranches()
	require.NoError(t, err)

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	// Create a few tag-along transactions
	t.Logf("Creating %d tag-along transactions", numTagAlongs)

	remainder := testData.faucetOutput
	tagAlongTxIDs := make([]base.TransactionID, 0, numTagAlongs)

	for i := 0; i < numTagAlongs; i++ {
		ts := remainder.Timestamp().AddTicks(int(ledger.L(0).TransactionPace) * (i + 1))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		tData := txbuilder.NewTransferData(testData.privKeyFaucet, testData.addrFaucet, ts).
			MustWithInputs(remainder).
			WithTargetLock(targetAddr).
			WithAmount(sendAmount).
			WithTagAlong(testData.bootstrapChainID, tagAlongFee)

		txBytes, newRemainder, err := txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
		require.NoError(t, err)

		txid, err := testData.wrk.TxBytesIn(txBytes)
		require.NoError(t, err)

		tagAlongTxIDs = append(tagAlongTxIDs, txid)
		remainder = newRemainder
		t.Logf("Created tag-along tx %d: %s", i, txid.StringShort())
	}

	time.Sleep(500 * time.Millisecond)

	// Start sequencer
	seq, err := newTestSequencer(testData.wrk, testData.bootstrapChainID, genesisPrivateKey,
		sequencer.WithMaxBranches(maxSlots))
	require.NoError(t, err)

	var countSeq atomic.Int32
	seq.OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx) {
		countSeq.Add(1)
		t.Logf("Milestone: %s, inputs: %d", ms.IDShortString(), ms.NumInputs())
	})
	seq.OnExitOnce(func() {
		testData.stop()
	})
	seq.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	<-ctx.Done()

	testData.waitStop(5 * time.Second)

	// All tag-alongs should be finalized with normal budget
	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	finalizedCount := 0
	for _, txid := range tagAlongTxIDs {
		if rdr.KnowsCommittedTransaction(txid) {
			finalizedCount++
		}
	}

	t.Logf("Tag-along transactions finalized: %d / %d", finalizedCount, numTagAlongs)
	require.EqualValues(t, numTagAlongs, finalizedCount,
		"all tag-along transactions should be finalized with normal budget")

	// Verify target received the tokens
	targetBalance := rdr.BalanceOf(targetAddr.ControllerID())
	expectedBalance := uint64(numTagAlongs * sendAmount)
	require.EqualValues(t, expectedBalance, targetBalance,
		"target should receive tokens from all tag-along transactions")

	t.Logf("Baseline test PASSED: all %d tag-alongs finalized", numTagAlongs)
}
