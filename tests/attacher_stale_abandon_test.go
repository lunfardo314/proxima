package tests

import (
	"crypto/ed25519"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/stretchr/testify/require"
)

// TestAttacherStaleAbandonedPastTTL is the regression test for the sync wedge where a milestone
// attacher stuck on a dependency that never resolves spins in lazyRepeat for hours, pinning its
// whole past cone, until the memDAG size backstop force-detaches its vertex.
//
// A sequencer milestone is built consuming an input whose producer transaction is never stored,
// attached or reachable (the node has no peers), so it can never solidify. With the memDAG
// wall-clock vertex TTL lowered to a couple of slots, the attacher must self-abort well before the
// (much longer) pull-solidification deadline — bounding its lifetime to the TTL so it stops spinning
// and no live attacher outlives its vertex. The abort is a clean abandon: the node keeps running.
func TestAttacherStaleAbandonedPastTTL(t *testing.T) {
	// hand-built sequencer milestone can't declare the attacher-computed coverageDelta
	defer reinitTestLedgerNoCoverageMonotonicity()()

	testData := initWorkflowTest(t, 1)
	defer testData.stopAndWait()
	require.NoError(t, testData.wrk.EnsureLatestBranches())

	// Lower the wall-clock vertex TTL so the attacher ages past it in a few seconds, well before the
	// 60s pull deadline (30 attempts x 2s). Keep the size cap high so the size backstop does not fire
	// (we exercise the proactive self-abort, not the forced detach).
	defer memdag.SetGCTuningForTesting(50000, 2, 12)()

	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))
	sourceOutput, err := oDatas[0].Parse()
	require.NoError(t, err)

	// missing producer: a valid transfer, chain-locked to the bootstrap sequencer so the milestone
	// can unlock it — but NEVER stored or attached, so its output can never solidify.
	ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	if ts1.IsSlotBoundary() {
		ts1 = ts1.AddTicks(1)
	}
	txBytes1, err := utxodb.MakeSimpleTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, ts1).
		MustWithInputs(sourceOutput).
		WithAmount(1_000_000_000).
		WithTargetLock(ledger.ChainLockFromChainID(testData.bootstrapChainID)))
	require.NoError(t, err)
	tx1, err := transaction.ParseWithPartialValidation(txBytes1)
	require.NoError(t, err)
	missingInput := tx1.MustProducedOutputWithIDAt(1)

	// sequencer milestone consuming the missing input
	branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
	require.EqualValues(t, 1, len(branches))
	chainOut := branches[0].SequencerOutput.MustAsChainOutput()
	ts := base.MaximumTime(chainOut.Timestamp(), missingInput.Timestamp()).
		AddTicks(int(ledger.L(0).TransactionPaceSequencer))
	txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:          "testSeq",
		Timestamp:        ts,
		ChainInput:       chainOut,
		AdditionalInputs: []*ledger.OutputWithID{missingInput},
		SignatureType:    base.SignatureTypeED25519,
		PrivateKey:       genesisPrivateKey,
		PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	wg.Add(1)
	var cbErr error
	start := time.Now()
	_, err = attacher.AttachTransactionFromBytes(txBytes, testData.wrk,
		attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, e error) {
			cbErr = e
			wg.Done()
		}))
	require.NoError(t, err)

	// the attacher must finish (not hang); with TTL=2 slots it self-aborts in a few seconds
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("attacher did not finish: stuck milestone was not abandoned")
	}
	elapsed := time.Since(start)

	t.Logf("stuck milestone finished after %v, err: %v", elapsed, cbErr)
	require.Error(t, cbErr)
	require.True(t, strings.Contains(cbErr.Error(), "stale attacher abandoned"),
		"expected stale-abandoned abort, got: %v", cbErr)
	// well under the ~60s pull-solidification deadline: the TTL bound is what stopped it
	require.Less(t, elapsed, 30*time.Second)
	require.False(t, testData.wrk.IsShuttingDown(), "node must keep running after abandoning a stale attacher")
}
