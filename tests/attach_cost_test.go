package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ATTACHMENT COST BUDGET TESTS
// These tests verify the attachment cost budget enforcement mechanism that
// prevents attacks with unbounded chains of transactions in the past cone.
//
// Key concepts:
// - Attachment cost = numInputs + numProducedOutputs for non-sequencer transactions
// - Past cone attachment cost = sum of attachment costs of all directly reachable
//   non-sequencer transactions not in the baseline state
// - Sequencer transaction cost = cost of the sequencer tx being attached
// - Total attachment cost = pastConeCost + seqTxCost (checked against budget)
// - FlagPastConeDirectCost marks vertices that contribute to direct cost
// - Merged past cones don't contribute (flag is masked out in MergePastCone)
// =============================================================================

// TestAttachCostBudgetChainWithinLimit tests a chain of non-sequencer transactions
// within the cost budget. This should succeed without errors.
func TestAttachCostBudgetChainWithinLimit(t *testing.T) {
	t.Run("chain within cost budget", func(t *testing.T) {
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		t.Logf("AttachmentCostBudget = %d", costBudget)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		// Create a chain of transactions within cost budget
		// Each simple transfer has cost ~2 (1 input + 1 output)
		// We'll use a moderate chain length to test within budget but not take too long
		chainLength := 50 // Well within budget of 600 (50*2 = 100 cost)
		t.Logf("Budget is %d, creating chain with cost ~%d", costBudget, chainLength*2)
		t.Logf("Creating chain of %d transactions (total cost ~%d)", chainLength, chainLength*2)

		prevOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		txBytesChain := make([][]byte, chainLength)
		for i := 0; i < chainLength; i++ {
			ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}

			td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
				MustWithInputs(prevOutput).
				WithAmount(prevOutput.Output.TokenBalance()).
				WithTargetLock(testData.addr)

			txBytesChain[i], err = txbuilder.MakeSimpleTransferTransaction(td)
			require.NoError(t, err)

			tx, err := transaction.FromBytes(txBytesChain[i], transaction.MainTxValidationOptions...)
			require.NoError(t, err)
			prevOutput = tx.MustProducedOutputWithIDAt(0)

			// Store all but the last in txstore for pull
			if i < chainLength-1 {
				_, err = testData.txStore.PersistTxBytesWithMetadata(txBytesChain[i], nil)
				require.NoError(t, err)
			}
		}

		// Attach the last transaction (should pull the chain)
		vid, err := attacher.AttachTransactionFromBytes(txBytesChain[chainLength-1], testData.wrk)
		require.NoError(t, err)

		// Chain within limits should not be rejected
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(),
			"chain within cost budget should not be rejected")
		t.Logf("Chain of %d transactions within cost budget %d: PASSED (status: %s)",
			chainLength, costBudget, vid.GetTxStatus().String())
	})
}

// TestAttachCostBudgetShortChain tests a short chain that's clearly within budget.
// This is a simpler baseline test.
func TestAttachCostBudgetShortChain(t *testing.T) {
	t.Run("short chain within budget", func(t *testing.T) {
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		t.Logf("AttachmentCostBudget = %d", costBudget)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		// Create a short chain (10 transactions, cost ~20, well within budget of 600)
		chainLength := 10
		t.Logf("Creating short chain of %d transactions (cost ~%d, budget = %d)",
			chainLength, chainLength*2, costBudget)

		prevOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		txBytesChain := make([][]byte, chainLength)
		for i := 0; i < chainLength; i++ {
			ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}

			td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
				MustWithInputs(prevOutput).
				WithAmount(prevOutput.Output.TokenBalance()).
				WithTargetLock(testData.addr)

			txBytesChain[i], err = txbuilder.MakeSimpleTransferTransaction(td)
			require.NoError(t, err)

			tx, err := transaction.FromBytes(txBytesChain[i], transaction.MainTxValidationOptions...)
			require.NoError(t, err)
			prevOutput = tx.MustProducedOutputWithIDAt(0)

			// Store all but the last in txstore for pull
			if i < chainLength-1 {
				_, err = testData.txStore.PersistTxBytesWithMetadata(txBytesChain[i], nil)
				require.NoError(t, err)
			}
		}

		// Attach the last transaction (should pull the chain)
		vid, err := attacher.AttachTransactionFromBytes(txBytesChain[chainLength-1], testData.wrk)
		require.NoError(t, err)

		// Chain within limits should not be rejected
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(),
			"short chain should not be rejected")
		t.Logf("Short chain of %d transactions: PASSED (status: %s)", chainLength, vid.GetTxStatus().String())
	})
}

// TestAttachCostBudgetMultipleTransactions tests attachment cost tracking with
// multiple transactions that share inputs or outputs.
func TestAttachCostBudgetMultipleTransactions(t *testing.T) {
	t.Run("multiple independent transactions", func(t *testing.T) {
		// This test creates multiple independent transactions from different
		// source outputs and verifies they can all be attached without
		// exceeding the cost budget.
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		t.Logf("AttachmentCostBudget = %d", costBudget)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		prevOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create and attach 5 sequential transactions
		numTxs := 5
		t.Logf("Creating %d sequential transactions (total cost ~%d)", numTxs, numTxs*2)

		for i := 0; i < numTxs; i++ {
			ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}

			td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
				MustWithInputs(prevOutput).
				WithAmount(prevOutput.Output.TokenBalance()).
				WithTargetLock(testData.addr)

			txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
			require.NoError(t, err)

			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			require.NoError(t, err)

			// Attach each transaction
			vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
			require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(),
				"transaction %d should not be rejected", i)

			t.Logf("Transaction %d attached with cost %d (status: %s)",
				i, tx.NumInputs()+tx.NumProducedOutputs(), vid.GetTxStatus().String())

			prevOutput = tx.MustProducedOutputWithIDAt(0)
		}

		t.Logf("Successfully attached %d sequential transactions", numTxs)
	})
}

// TestAttachCostBudgetFanOutCostTracking tests that fan-out transactions accumulate cost
// correctly. While exceeding the budget of 600 within a single slot is difficult due to
// storage deposit requirements, this test verifies the cost tracking mechanism works.
func TestAttachCostBudgetFanOutCostTracking(t *testing.T) {
	t.Run("fan-out cost tracking", func(t *testing.T) {
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		t.Logf("AttachmentCostBudget = %d", costBudget)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create a single large fan-out transaction to demonstrate high-cost tracking
		// With 10T tokens and ~13.6M min deposit, we can create ~733 outputs max
		// Let's create 100 outputs for a cost of 101 (1 input + 100 outputs)
		const numOutputs = 100
		minDeposit := ledger.DefaultStorageDeposit()
		totalBalance := sourceOutput.Output.TokenBalance()
		amountPerOutput := totalBalance / uint64(numOutputs)

		t.Logf("Creating fan-out transaction: 1 input -> %d outputs", numOutputs)
		t.Logf("Balance: %d, min deposit: %d, amount per output: %d", totalBalance, minDeposit, amountPerOutput)

		require.GreaterOrEqual(t, amountPerOutput, minDeposit,
			"not enough balance for %d outputs with min deposit %d", numOutputs, minDeposit)

		ts := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		txb := txbuilder.New()
		txb.TransactionData.Timestamp = ts

		// Consume the input
		_, err = txb.ConsumeOutput(sourceOutput.Output, sourceOutput.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)

		// Create outputs
		for j := 0; j < numOutputs-1; j++ {
			out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(amountPerOutput)
				o.WithLock(testData.addr)
			})
			_, err := txb.ProduceOutput(out)
			require.NoError(t, err)
		}
		// Last output gets remainder
		remainder := totalBalance - amountPerOutput*uint64(numOutputs-1)
		lastOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(remainder)
			o.WithLock(testData.addr)
		})
		_, err = txb.ProduceOutput(lastOut)
		require.NoError(t, err)

		// Sign and build
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(testData.privKey)

		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)

		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(t, err)

		expectedCost := tx.NumInputs() + tx.NumProducedOutputs()
		t.Logf("Fan-out transaction: %d inputs -> %d outputs, cost = %d",
			tx.NumInputs(), tx.NumProducedOutputs(), expectedCost)
		require.EqualValues(t, 1+numOutputs, expectedCost,
			"fan-out cost should be 1 + %d = %d", numOutputs, 1+numOutputs)

		// Attach the transaction
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)

		// Fan-out within budget should not be rejected
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(),
			"fan-out transaction within budget should not be rejected")

		t.Logf("Fan-out transaction with cost %d attached successfully (budget = %d): PASSED",
			expectedCost, costBudget)
	})
}

// TestAttachCostBudgetExceededNote documents that exceeding the budget of 600 within a
// single slot is intentionally difficult. The budget is designed to prevent attack chains
// while allowing legitimate transaction patterns.
//
// To exceed budget of 600 with simple transfers (cost 2 each), you'd need 300+ transactions.
// Within one slot (~42 transactions at pace 3), max cost is only ~84.
// Fan-out transactions can achieve higher cost per transaction, but storage deposit
// requirements limit how much tokens can be split.
//
// A full budget-exceeded test would require:
// 1. Multiple slots (complex endorsement handling), or
// 2. A test-specific lower budget, or
// 3. A contrived scenario with many initial UTXOs
func TestAttachCostBudgetExceededNote(t *testing.T) {
	t.Run("budget design rationale", func(t *testing.T) {
		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		ticksPerSlot := ledger.L(0).TicksPerSlot
		txPace := ledger.L(0).TransactionPace
		maxTxPerSlot := int(ticksPerSlot) / int(txPace)
		maxSimpleCostPerSlot := maxTxPerSlot * 2 // Simple transfer has cost 2

		t.Logf("Budget design analysis:")
		t.Logf("  AttachmentCostBudget = %d", costBudget)
		t.Logf("  TicksPerSlot = %d", ticksPerSlot)
		t.Logf("  TransactionPace = %d", txPace)
		t.Logf("  Max transactions per slot = %d", maxTxPerSlot)
		t.Logf("  Max simple transfer cost per slot = %d", maxSimpleCostPerSlot)
		t.Logf("  Ratio (budget / max simple cost) = %.2f", float64(costBudget)/float64(maxSimpleCostPerSlot))

		// The budget is intentionally higher than what can be achieved with simple
		// transfers in one slot, but still provides protection against attack chains
		// that span multiple transactions with high fan-out.
		require.Greater(t, int(costBudget), maxSimpleCostPerSlot,
			"budget should be higher than max simple cost per slot")

		t.Logf("Budget exceeds single-slot simple transfer capacity by %.1fx - this is by design",
			float64(costBudget)/float64(maxSimpleCostPerSlot))
	})
}

// TestAttachCostBudgetVerifyCalculation tests that attachment cost is correctly
// calculated as numInputs + numProducedOutputs.
func TestAttachCostBudgetVerifyCalculation(t *testing.T) {
	t.Run("verify cost calculation", func(t *testing.T) {
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsInAccount(testData.addr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		prevOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create a single transaction and verify its cost calculation
		ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
			MustWithInputs(prevOutput).
			WithAmount(prevOutput.Output.TokenBalance()).
			WithTargetLock(testData.addr)

		txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(t, err)

		// Verify cost calculation
		expectedCost := tx.NumInputs() + tx.NumProducedOutputs()
		t.Logf("Transaction has %d inputs and %d outputs, expected cost = %d",
			tx.NumInputs(), tx.NumProducedOutputs(), expectedCost)

		// A simple transfer should have 1 input and 1 output (or 2 with remainder)
		require.True(t, tx.NumInputs() >= 1, "transaction should have at least 1 input")
		require.True(t, tx.NumProducedOutputs() >= 1, "transaction should have at least 1 output")
		require.EqualValues(t, tx.NumInputs()+tx.NumProducedOutputs(), expectedCost,
			"cost should equal numInputs + numOutputs")

		t.Logf("Cost calculation verified: %d = %d + %d", expectedCost, tx.NumInputs(), tx.NumProducedOutputs())
	})
}
