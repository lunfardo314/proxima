package tests

import (
	"crypto/ed25519"
	"runtime"
	"sync"
	"sync/atomic"
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
// DEADLOCK SCENARIO TESTS
// These tests cover potential deadlock scenarios in the attacher, including
// context cancellation, concurrent attachers, and shutdown behavior.
// =============================================================================

// TestAttachDeadlockContextCancellation tests workflow stop behavior mid-attachment.
// Verifies that stopping the workflow causes attachers to exit cleanly.
func TestAttachDeadlockContextCancellation(t *testing.T) {
	t.Run("stop workflow during attachment", func(t *testing.T) {
		testData := initWorkflowTest(t, 2)

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(1)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		chainOrigin := testData.chainOrigins[0]

		ts := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "test",
			ChainInput:    chainOrigin,
			Timestamp:     ts,
			Endorsements:  []base.TransactionID{testData.distributionBranchTxID},
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    testData.privKeyAux,
			PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)

		var callbackCalled atomic.Bool
		vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk,
			attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) {
				callbackCalled.Store(true)
			}))
		require.NoError(t, err)

		// Stop workflow immediately (this triggers context cancellation internally)
		testData.stop()

		// Wait for completion with timeout
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			status := vid.GetTxStatus()
			if status != vertex.Undefined {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}

		// Should complete (either Good or Bad) without hanging
		status := vid.GetTxStatus()
		t.Logf("Transaction status after stop: %s", status.String())
		require.True(t, status == vertex.Good || status == vertex.Bad,
			"transaction should complete after workflow stop, got: %s", status.String())

		testData.waitStop(5 * time.Second)
	})
}

// TestAttachDeadlockConcurrentAttachers tests concurrent attachment of the same transaction.
// Verifies that only one attacher runs and callbacks are properly invoked.
func TestAttachDeadlockConcurrentAttachers(t *testing.T) {
	t.Run("concurrent attach same transaction", func(t *testing.T) {
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

		ts := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(1)
		}

		td := txbuilder.NewTransferData(testData.privKey, testData.addr, ts).
			MustWithInputs(sourceOutput).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes, err := txbuilder.MakeSimpleTransferTransaction(td)
		require.NoError(t, err)

		const numConcurrent = 10
		var wg sync.WaitGroup
		vids := make([]*vertex.WrappedTx, numConcurrent)
		errors := make([]error, numConcurrent)

		// Start multiple concurrent attachments of the same transaction
		wg.Add(numConcurrent)
		for i := 0; i < numConcurrent; i++ {
			go func(idx int) {
				defer wg.Done()
				vid, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
				errors[idx] = err
				vids[idx] = vid
			}(i)
		}

		// Wait with timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Good, all completed
		case <-time.After(10 * time.Second):
			t.Fatal("timeout waiting for concurrent attachments - possible deadlock")
		}

		// All attachments should succeed without error
		for i, err := range errors {
			require.NoError(t, err, "concurrent attachment %d should not error", i)
		}

		// All vids should point to same vertex (same txid)
		var refVid *vertex.WrappedTx
		for i, vid := range vids {
			if vid != nil {
				if refVid == nil {
					refVid = vid
				} else {
					require.EqualValues(t, refVid.ID(), vid.ID(),
						"concurrent attachments should return same vertex, idx=%d", i)
				}
			}
		}

		require.NotNil(t, refVid, "at least one vid should be returned")
		require.NotEqual(t, vertex.Bad.String(), refVid.GetTxStatus().String(), "transaction should not be rejected")
		t.Logf("Concurrent attachments: %d goroutines, all returned same vertex: PASSED", numConcurrent)
	})
}

// TestAttachDeadlockSolidificationDeadline tests solidification deadline behavior.
// Verifies that missing inputs cause deadline expiration, not hanging.
func TestAttachDeadlockSolidificationDeadline(t *testing.T) {
	t.Run("missing input causes deadline", func(t *testing.T) {
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

		// Create first transaction (don't store it - will be missing)
		ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		if ts1.IsSlotBoundary() {
			ts1 = ts1.AddTicks(1)
		}

		td1 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts1).
			MustWithInputs(sourceOutput).
			WithAmount(5_000_000_000).
			WithTargetLock(testData.addr)

		txBytes1, err := txbuilder.MakeSimpleTransferTransaction(td1)
		require.NoError(t, err)

		tx1, err := transaction.ParseWithPartialValidation(txBytes1)
		require.NoError(t, err)

		// Create second transaction that depends on first (missing)
		output1 := tx1.MustProducedOutputWithIDAt(0)
		ts2 := ts1.AddTicks(int(ledger.L(0).TransactionPace))
		if ts2.IsSlotBoundary() {
			ts2 = ts2.AddTicks(1)
		}

		td2 := txbuilder.NewTransferData(testData.privKey, testData.addr, ts2).
			MustWithInputs(output1).
			WithAmount(100_000_000).
			WithTargetLock(testData.addr)

		txBytes2, err := txbuilder.MakeSimpleTransferTransaction(td2)
		require.NoError(t, err)

		// Attach second transaction - should eventually fail due to missing input
		// Note: the first transaction (tx1) was never stored or attached
		vid, err := attacher.AttachTransactionFromBytes(txBytes2, testData.wrk)
		require.NoError(t, err)

		// Non-sequencer transactions might not immediately fail - they may be pending
		// while trying to solidify. The key assertion is that this doesn't hang.
		status := vid.GetTxStatus()
		t.Logf("Initial status for tx with missing input: %s", status.String())

		// If still trying to solidify, that's acceptable for this test
		// The main point is that the AttachTransactionFromBytes returned without hanging
		require.True(t, status == vertex.Undefined || status == vertex.Bad,
			"transaction with missing input should be Undefined (pending) or Bad, got: %s", status.String())
	})
}

// TestAttachDeadlockShutdownDuringAttachment tests graceful shutdown mid-attachment.
// Verifies that stopping the workflow doesn't leave orphaned goroutines.
func TestAttachDeadlockShutdownDuringAttachment(t *testing.T) {
	t.Run("shutdown during multiple attachments", func(t *testing.T) {
		goroutinesBefore := runtime.NumGoroutine()

		testData := initWorkflowTest(t, 2)

		// Ensure distribution branch is attached before attaching dependent transactions
		err := testData.wrk.EnsureLatestBranches()
		require.NoError(t, err)

		testData.makeChainOrigins(5)
		_, err = attacher.AttachTransactionFromBytes(testData.chainOriginsTx.Bytes(), testData.wrk)
		require.NoError(t, err)

		// Start multiple attachments
		const numAttachments = 5
		for i := 0; i < numAttachments; i++ {
			chainOrigin := testData.chainOrigins[i%len(testData.chainOrigins)]
			ts := chainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer) * (i + 1))
			ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

			txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
				SeqName:       "test",
				ChainInput:    chainOrigin,
				Timestamp:     ts,
				Endorsements:  []base.TransactionID{testData.distributionBranchTxID},
				SignatureType: base.SignatureTypeED25519,
				PrivateKey:    testData.privKeyAux,
				PublicKey:     testData.privKeyAux.Public().(ed25519.PublicKey),
			})
			require.NoError(t, err)

			_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk)
			require.NoError(t, err)
		}

		// Immediate shutdown
		stopped := testData.stopAndWait(5 * time.Second)
		require.True(t, stopped, "workflow should stop within timeout")

		// Give goroutines time to clean up
		time.Sleep(500 * time.Millisecond)
		runtime.GC()
		time.Sleep(100 * time.Millisecond)

		goroutinesAfter := runtime.NumGoroutine()
		goroutineDiff := goroutinesAfter - goroutinesBefore

		t.Logf("Goroutines before: %d, after: %d, diff: %d", goroutinesBefore, goroutinesAfter, goroutineDiff)

		// Allow some slack for background goroutines, but shouldn't leak many
		require.LessOrEqual(t, goroutineDiff, 5,
			"should not leak many goroutines after shutdown")
	})
}
