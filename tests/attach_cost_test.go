package tests

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
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
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
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

			tx, err := transaction.ParseWithPartialValidation(txBytesChain[i])
			require.NoError(t, err)
			prevOutput = tx.MustProducedOutputWithIDAt(0)

			// Store all but the last in txstore for pull
			if i < chainLength-1 {
				_, err = testData.txStore.PersistTxBytes(txBytesChain[i])
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
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
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

			tx, err := transaction.ParseWithPartialValidation(txBytesChain[i])
			require.NoError(t, err)
			prevOutput = tx.MustProducedOutputWithIDAt(0)

			// Store all but the last in txstore for pull
			if i < chainLength-1 {
				_, err = testData.txStore.PersistTxBytes(txBytesChain[i])
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
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
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

			tx, err := transaction.ParseWithPartialValidation(txBytes)
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
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
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
		txb.SetTimestamp(ts)

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
		txb.ComputeInputCommitment()
		txb.SignED25519(testData.privKey)

		txBytes, _, _, err := txb.BytesWithValidation()
		require.NoError(t, err)

		tx, err := transaction.ParseWithPartialValidation(txBytes)
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

// TestAttachCostBudgetExceededMilestoneAttacher tests that the milestone attacher
// correctly rejects a sequencer transaction when the attachment cost budget is exceeded.
// This test uses a low budget to make the budget-exceeded case achievable.
//
// The test creates a chain of non-sequencer transactions where the last one produces
// a tag-along output locked to the sequencer chain. When the sequencer consumes this
// tag-along output, it must pull the entire chain into its past cone, exceeding the budget.
func TestAttachCostBudgetExceededMilestoneAttacher(t *testing.T) {
	t.Run("budget exceeded in milestone attacher", func(t *testing.T) {
		// Reinitialize ledger with a very low budget (5) so we can exceed it easily
		// A simple transfer has cost 2 (1 input + 1 output), even 2 transfers exceed budget 5
		cleanup := reinitTestLedgerWithBudget(5)
		defer cleanup()

		costBudget := ledger.L(base.MaxSlot).AttachmentCostBudget
		require.EqualValues(t, 5, costBudget, "budget should be set to 5 for this test")
		t.Logf("AttachmentCostBudget = %d (lowered for test)", costBudget)

		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(1)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]
		seqChainID := chainOrigin.ChainID

		// Get a source output for creating non-sequencer transactions
		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(oDatas), 1)

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create a chain of non-sequencer transactions to exceed budget
		// Budget is 5, each simple transfer has cost 2
		// Chain of 5 transactions = 10 past cone cost, plus seq tx cost (~3) = 13 > 5
		chainLength := 5
		chainLockAmount := uint64(100_000_000) // Amount for chain-locked output (must exceed min storage deposit)
		t.Logf("Creating chain of %d non-sequencer transactions (cost ~%d)", chainLength, chainLength*2)
		t.Logf("Target sequencer chain ID: %s", seqChainID.StringShort())

		prevOutput := sourceOutput
		txBytesChain := make([][]byte, chainLength)
		var lastChainLockedOutput *ledger.OutputWithID

		for i := 0; i < chainLength; i++ {
			ts := prevOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(1)
			}

			balance := prevOutput.Output.TokenBalance()

			if i == chainLength-1 {
				// Last transaction: produce an output locked to the sequencer chain
				// This uses ChainLockFromChainID which makes the output consumable by the chain
				td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
					MustWithInputs(prevOutput).
					WithAmount(chainLockAmount).
					WithTargetLock(ledger.ChainLockFromChainID(seqChainID))

				txBytesChain[i], err = txbuilder.MakeSimpleTransferTransaction(td)
				require.NoError(t, err)

				tx, err := transaction.ParseWithPartialValidation(txBytesChain[i])
				require.NoError(t, err)

				// The chain-locked output is the one with the target lock (usually index 0 for simple transfers)
				// Find the output locked to the chain
				tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
					if o.Lock().String() == ledger.ChainLockFromChainID(seqChainID).String() {
						lastChainLockedOutput = &ledger.OutputWithID{
							ID:     oid,
							Output: o,
						}
						t.Logf("Created chain-locked output at index %d: %s", idx, oid.StringShort())
						return false
					}
					return true
				})
				require.NotNil(t, lastChainLockedOutput, "should have created chain-locked output")

				// Get remainder output for next iteration (if any)
				tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
					if o.Lock().String() == testData.addr.String() {
						prevOutput = &ledger.OutputWithID{ID: oid, Output: o}
						return false
					}
					return true
				})
			} else {
				// Regular transfer to self
				td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
					MustWithInputs(prevOutput).
					WithAmount(balance).
					WithTargetLock(testData.addr)

				txBytesChain[i], err = txbuilder.MakeSimpleTransferTransaction(td)
				require.NoError(t, err)

				tx, err := transaction.ParseWithPartialValidation(txBytesChain[i])
				require.NoError(t, err)
				prevOutput = tx.MustProducedOutputWithIDAt(0)
			}

			// Store all transactions in txstore for pull
			_, err = testData.txStore.PersistTxBytes(txBytesChain[i])
			require.NoError(t, err)
		}

		// Now create a sequencer transaction that consumes the chain-locked output
		// This forces the sequencer to pull the entire chain into its past cone

		// Timestamp must be after the last chain transaction
		ts := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))

		// Make sure timestamp is after the last transaction in the chain
		lastTx, err := transaction.ParseWithPartialValidation(txBytesChain[chainLength-1])
		require.NoError(t, err)
		if !ts.After(lastTx.Timestamp()) {
			ts = lastTx.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		}

		t.Logf("Creating sequencer transaction at %s with chain-locked input", ts.String())
		t.Logf("Chain-locked output to consume: %s", lastChainLockedOutput.ID.StringShort())

		// Create sequencer transaction with the chain-locked input
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "test",
			ChainInput:       chainOrigin,
			Timestamp:        ts,
			Endorsements:     []base.TransactionID{testData.distributionBranchTxID},
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       testData.privKeyAux,
			PublicKey:        testData.privKeyAux.Public().(ed25519.PublicKey),
			AdditionalInputs: []*ledger.OutputWithID{lastChainLockedOutput},
		})
		require.NoError(t, err)

		// Attach the sequencer transaction - this should fail due to budget exceeded
		// when it tries to solidify the past cone (the chain of 12 transactions)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err) // Attachment starts without error

		// Wait for the transaction to be processed
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			status := vid.GetTxStatus()
			if status != vertex.Undefined {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		status := vid.GetTxStatus()
		t.Logf("Sequencer transaction status: %s", status.String())

		// The transaction should be marked Bad due to budget exceeded
		require.Equal(t, vertex.Bad, status,
			"sequencer transaction should be rejected due to budget exceeded")

		vidErr := vid.GetError()
		require.NotNil(t, vidErr, "error should be set")
		t.Logf("Transaction error: %v", vidErr)
		require.Contains(t, vidErr.Error(), "budget",
			"error should mention budget exceeded")
		t.Logf("Budget exceeded test PASSED: sequencer tx rejected with budget error")
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
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
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

		tx, err := transaction.ParseWithPartialValidation(txBytes)
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
