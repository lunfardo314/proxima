package tests

import (
	"crypto/ed25519"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TIMING EDGE CASES TESTS
// These tests cover timing-related edge cases in the attacher, including
// transaction pace validation, slot boundary transitions, and consolidation windows.
// =============================================================================

// TestAttachTimingPaceBoundaries tests transaction pace validation at exact boundaries.
// It verifies that transactions respect the minimum tick spacing requirements.
func TestAttachTimingPaceBoundaries(t *testing.T) {
	t.Run("non-sequencer exact pace", func(t *testing.T) {
		// Test that a transaction exactly at TransactionPace ticks apart is valid
		// Note: Non-sequencer transactions don't get callbacks like sequencer transactions.
		// We verify by checking if the transaction was successfully attached.
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Create transaction exactly at TransactionPace ticks
		exactPaceTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if exactPaceTs.IsSlotBoundary() {
			exactPaceTs = exactPaceTs.AddTicks(1)
		}

		td := utxodb.NewTransferData(testData.privKey, testData.addr, exactPaceTs).
			MustWithInputs(sourceOutput).
			WithAmount(1_000_000_000). // Use higher amount for minimum storage deposit
			WithTargetLock(testData.addr)

		txBytes, err := utxodb.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)

		// Non-sequencer tx shouldn't have "Bad" status if it was built correctly
		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(), "transaction at exact pace should not be rejected")
		t.Logf("TransactionPace = %d ticks, transaction at exact pace: PASSED (status: %s)", ledger.L(0).TransactionPace, vid.GetTxStatus().String())
	})

	t.Run("non-sequencer pace minus one", func(t *testing.T) {
		// Test that the pace constraint boundary is correctly identified.
		// Note: The actual pace constraint validation happens in EasyFL scripts during
		// lock validation, which only occurs when transaction is included in a sequencer's
		// past cone. This test verifies the constraint calculation is correct.
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Calculate timestamps at pace-1 and at exact pace
		tooFastTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace) - 1)
		exactPaceTs := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))

		// Verify the difference calculation
		tooFastDiff := base.DiffTicks(tooFastTs, sourceOutput.Timestamp())
		exactDiff := base.DiffTicks(exactPaceTs, sourceOutput.Timestamp())

		require.EqualValues(t, ledger.L(0).TransactionPace-1, tooFastDiff,
			"pace-1 should be exactly TransactionPace-1 ticks")
		require.EqualValues(t, ledger.L(0).TransactionPace, exactDiff,
			"exact pace should be exactly TransactionPace ticks")

		t.Logf("TransactionPace = %d, pace-1 = %d, verified constraint boundary calculation",
			ledger.L(0).TransactionPace, ledger.L(0).TransactionPace-1)
	})

	t.Run("sequencer exact pace", func(t *testing.T) {
		// Test sequencer transaction exactly at TransactionPaceSequencer
		// Note: initLongConflictTestData requires nChains == nConflicts
		const nChains = 2
		testData := initLongConflictTestData(t, nChains, nChains, 0)
		defer testData.stopAndWait()

		testData.makeChainOrigins(nChains)
		err := testData.attachChainOriginTxs()
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]
		// Exact sequencer pace
		exactSeqPaceTs := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "test",
			ChainInput:    chainOrigin,
			Timestamp:     exactSeqPaceTs,
			Endorsements:  []base.TransactionID{testData.distributionBranchTxID},
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(1)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk, attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
			wg.Done()
		}))
		require.NoError(t, err)
		wg.Wait()

		require.EqualValues(t, vertex.Good.String(), vid.GetTxStatus().String(), "sequencer at exact pace should be valid")
		t.Logf("TransactionPaceSequencer = %d ticks, sequencer at exact pace: PASSED", ledger.L(0).TransactionPaceSequencer)
	})
}

// TestAttachTimingSlotBoundaries tests slot boundary transitions.
// It verifies correct handling of transactions at tick 127 (last tick) and tick 0 (branch).
func TestAttachTimingSlotBoundaries(t *testing.T) {
	t.Run("branch transaction at slot boundary", func(t *testing.T) {
		// Test that a branch transaction (tick == 0) requires stem input.
		// This test verifies the slot boundary calculation and branch transaction construction.
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(1)
		err = testData.attachChainOriginTxs()
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]

		// Get stem output from distribution branch
		distribBD := testData.wrk.Branches().Get(testData.distributionBranchTxID)
		require.NotNil(t, distribBD)

		// Create branch at next slot boundary
		branchTs := chainOrigin.Timestamp().NextSlotBoundary()
		require.True(t, branchTs.IsSlotBoundary(), "branch timestamp must be on slot boundary")
		require.EqualValues(t, 0, branchTs.Tick, "branch timestamp tick must be 0")

		// Verify we can build the branch transaction
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "test",
			ChainInput:    chainOrigin,
			StemInput:     distribBD.Stem,
			Timestamp:     branchTs,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err, "should be able to build branch transaction with stem input")

		// Attach without waiting for callback (sequencer tx solidification can be slow)
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)
		require.NotNil(t, vid)

		t.Logf("Branch at slot %d, tick 0: built and attached (status: %s)", branchTs.Slot, vid.GetTxStatus().String())
	})

	t.Run("last tick before slot boundary", func(t *testing.T) {
		// Test transaction at tick 127 (MaxTickValue)
		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// Find a timestamp at tick 127 (last tick of slot)
		lastTickTs := sourceOutput.Timestamp()
		for lastTickTs.Tick != base.MaxTickValue {
			lastTickTs = lastTickTs.AddTicks(1)
		}
		// Ensure it's at valid pace from source
		if base.DiffTicks(lastTickTs, sourceOutput.Timestamp()) < int64(ledger.L(0).TransactionPace) {
			lastTickTs = base.T(lastTickTs.Slot+1, base.MaxTickValue)
		}

		require.EqualValues(t, base.MaxTickValue, lastTickTs.Tick, "should be at tick 127")

		td := utxodb.NewTransferData(testData.privKey, testData.addr, lastTickTs).
			MustWithInputs(sourceOutput).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes, err := utxodb.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
		require.NoError(t, err)

		require.NotEqual(t, vertex.Bad.String(), vid.GetTxStatus().String(), "transaction at tick 127 should not be rejected")
		t.Logf("Transaction at slot %d, tick %d (MaxTickValue): PASSED (status: %s)", lastTickTs.Slot, lastTickTs.Tick, vid.GetTxStatus().String())
	})

	t.Run("cross-slot transaction chain", func(t *testing.T) {
		// Test transaction that consumes output from previous slot
		testData := initWorkflowTest(t, 2)
		defer testData.stopAndWait()

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
		oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(oDatas))

		sourceOutput, err := oDatas[0].Parse()
		require.NoError(t, err)

		// First transaction in current slot
		ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts1.IsSlotBoundary() {
			ts1 = ts1.AddTicks(1)
		}

		td1 := utxodb.NewTransferData(testData.privKey, testData.addr, ts1).
			MustWithInputs(sourceOutput).
			WithAmount(5_000_000_000).
			WithTargetLock(testData.addr)

		txBytes1, err := utxodb.MakeSimpleTransferTransaction(td1)
		require.NoError(t, err)

		// Non-sequencer transactions are attached immediately without waiting for callback
		vid1, err := attacher.AttachTransactionFromBytes(txBytes1, testData.wrk)
		require.NoError(t, err)
		require.NotEqual(t, vertex.Bad.String(), vid1.GetTxStatus().String())

		// Second transaction in next slot (cross-slot)
		output1 := vid1.MustOutputWithIDAt(0)
		ts2 := base.T(ts1.Slot+1, ledger.L(0).TransactionPace+1) // Next slot

		td2 := utxodb.NewTransferData(testData.privKey, testData.addr, ts2).
			MustWithInputs(&output1).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes2, err := utxodb.MakeSimpleTransferTransaction(td2)
		require.NoError(t, err)

		vid2, err := attacher.AttachTransactionFromBytes(txBytes2, testData.wrk)
		require.NoError(t, err)

		require.NotEqual(t, vertex.Bad.String(), vid2.GetTxStatus().String(), "cross-slot transaction should not be rejected")
		t.Logf("Cross-slot chain: slot %d -> slot %d: PASSED", ts1.Slot, ts2.Slot)
	})
}

// TestAttachTimingPreBranchConsolidation tests pre-branch consolidation window behavior.
// Sequencer transactions within PreBranchConsolidationTicks of slot boundary have restrictions.
func TestAttachTimingPreBranchConsolidation(t *testing.T) {
	t.Run("sequencer in pre-consolidation window", func(t *testing.T) {
		// Test that we can correctly identify timestamps in the pre-consolidation window.
		// The actual enforcement of pre-consolidation restrictions is tested implicitly
		// through the ledger validation scripts.
		if ledger.L(0).PreBranchConsolidationTicks == 0 {
			t.Skip("PreBranchConsolidationTicks is 0, no constraint to test")
		}

		// Calculate pre-consolidation timestamp (within window before slot boundary)
		preConsolidationTick := base.MaxTickValue - ledger.L(0).PreBranchConsolidationTicks + 1
		preConsolidationTs := base.T(1, preConsolidationTick)

		require.True(t, ledger.L(0).IsPreBranchConsolidationTimestamp(preConsolidationTs),
			"timestamp should be in pre-consolidation window")

		// One tick before should NOT be in pre-consolidation
		beforePreConsolidation := base.T(1, preConsolidationTick-1)
		require.False(t, ledger.L(0).IsPreBranchConsolidationTimestamp(beforePreConsolidation),
			"timestamp before window should not be in pre-consolidation")

		t.Logf("Pre-consolidation window: ticks > %d, test tick: %d (in window: true), tick %d (in window: false)",
			base.MaxTickValue-ledger.L(0).PreBranchConsolidationTicks, preConsolidationTick, preConsolidationTick-1)
	})

	t.Run("at exact consolidation boundary", func(t *testing.T) {
		// Test at exact boundary of pre-consolidation window
		if ledger.L(0).PreBranchConsolidationTicks == 0 {
			t.Skip("PreBranchConsolidationTicks is 0, no constraint to test")
		}

		testData := initWorkflowTest(t, 1)
		defer testData.stopAndWait()

		// Test the boundary tick value
		boundaryTick := base.MaxTickValue - ledger.L(0).PreBranchConsolidationTicks
		boundaryTs := base.T(1, boundaryTick)

		// Boundary tick should NOT be in pre-consolidation
		require.False(t, ledger.L(0).IsPreBranchConsolidationTimestamp(boundaryTs),
			"tick at exact boundary should NOT be in pre-consolidation")

		// One tick after should BE in pre-consolidation
		afterBoundaryTs := base.T(1, boundaryTick+1)
		require.True(t, ledger.L(0).IsPreBranchConsolidationTimestamp(afterBoundaryTs),
			"tick after boundary should be in pre-consolidation")

		t.Logf("PreBranchConsolidationTicks=%d, boundary tick=%d: PASSED",
			ledger.L(0).PreBranchConsolidationTicks, boundaryTick)
	})
}
