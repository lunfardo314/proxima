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

// abandonedDependencySetup builds milestone A, which consumes an output of a producer transaction the
// node does not have, and milestone B continuing A's chain, so A is B's baseline direction. A and B are
// in the txstore, as on a node; the producer is not. The memDAG TTL is lowered to 2 slots.
func abandonedDependencySetup(t *testing.T) (testData *workflowTestData, txBytesProducer, txBytesA, txBytesB []byte, txidA base.TransactionID, cleanup func()) {
	restoreLedger := reinitTestLedgerNoCoverageMonotonicity()
	testData = initWorkflowTest(t, 1)
	require.NoError(t, testData.wrk.EnsureLatestBranches())
	restoreGC := memdag.SetGCTuningForTesting(50000, 2, 12)
	cleanup = func() {
		restoreGC()
		testData.stopAndWait()
		restoreLedger()
	}

	rdr := testData.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsForController(testData.addr.ControllerID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))
	sourceOutput, err := oDatas[0].Parse()
	require.NoError(t, err)

	ts1 := sourceOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	if ts1.IsSlotBoundary() {
		ts1 = ts1.AddTicks(1)
	}
	txBytesProducer, err = utxodb.MakeSimpleTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, ts1).
		MustWithInputs(sourceOutput).
		WithAmount(1_000_000_000).
		WithTargetLock(ledger.ChainLockFromChainID(testData.bootstrapChainID)))
	require.NoError(t, err)
	tx1, err := transaction.ParseWithPartialValidation(txBytesProducer)
	require.NoError(t, err)
	extraInput := tx1.MustProducedOutputWithIDAt(1)

	branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
	require.EqualValues(t, 1, len(branches))
	chainOut := branches[0].SequencerOutput.MustAsChainOutput()
	tsA := base.MaximumTime(chainOut.Timestamp(), extraInput.Timestamp()).
		AddTicks(int(ledger.L(0).TransactionPaceSequencer))
	txBytesA, err = txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:          "testSeq",
		Timestamp:        tsA,
		ChainInput:       chainOut,
		AdditionalInputs: []*ledger.OutputWithID{extraInput},
		SignatureType:    base.SignatureTypeED25519,
		PrivateKey:       genesisPrivateKey,
		PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)
	txA, err := transaction.ParseWithPartialValidation(txBytesA)
	require.NoError(t, err)
	txidA = txA.ID()

	txBytesB, err = txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:       "testSeq",
		Timestamp:     tsA.AddTicks(int(ledger.L(0).TransactionPaceSequencer)),
		ChainInput:    txA.SequencerOutput().MustAsChainOutput(),
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    genesisPrivateKey,
		PublicKey:     genesisPrivateKey.Public().(ed25519.PublicKey),
	})
	require.NoError(t, err)

	for _, b := range [][]byte{txBytesA, txBytesB} {
		_, err = testData.wrk.TxBytesStore().PersistTxBytes(b)
		require.NoError(t, err)
	}
	return
}

// attachAndWait attaches a milestone and returns the error its attacher finished with
func attachAndWait(t *testing.T, testData *workflowTestData, txBytes []byte, timeout time.Duration) error {
	errCh := make(chan error, 1)
	_, err := attacher.AttachTransactionFromBytes(txBytes, testData.wrk,
		attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, e error) { errCh <- e }))
	require.NoError(t, err)
	select {
	case e := <-errCh:
		return e
	case <-time.After(timeout):
		t.Fatalf("attacher did not finish within %v", timeout)
		return nil
	}
}

// TestAttacherRevivesAbandonedDependency reproduces the 2026-10-27 sync wedge. Milestone A is stuck on a
// producer transaction the node does not have, so its attacher is abandoned at the memDAG TTL and A is
// left Undefined. The producer then arrives, and milestone B, built on A's chain output, is attached
// while A is still in the memDAG. B must restart A's attachment and solidify, instead of waiting on the
// abandoned A until B is itself abandoned: on a catching-up node every later milestone built on A did
// that, in waves, holding the attacher cap until the node was restarted.
func TestAttacherRevivesAbandonedDependency(t *testing.T) {
	testData, txBytesProducer, txBytesA, txBytesB, txidA, cleanup := abandonedDependencySetup(t)
	defer cleanup()

	errA := attachAndWait(t, testData, txBytesA, 30*time.Second)
	require.Error(t, errA)
	require.True(t, strings.Contains(errA.Error(), "stale attacher abandoned"), "expected A to be abandoned, got: %v", errA)

	// the producer arrives; B, built on the abandoned A, is attached right away, while A is still in the memDAG
	_, err := testData.wrk.TxBytesStore().PersistTxBytes(txBytesProducer)
	require.NoError(t, err)
	_, err = attacher.AttachTransactionFromBytes(txBytesProducer, testData.wrk)
	require.NoError(t, err)

	errB := attachAndWait(t, testData, txBytesB, 40*time.Second)
	require.NoError(t, errB, "B must solidify by reviving its abandoned dependency A")

	vidA := testData.wrk.GetVertex(txidA)
	require.NotNil(t, vidA)
	require.EqualValues(t, vertex.Good, vidA.GetTxStatus())
	require.False(t, testData.wrk.IsShuttingDown())
}

// TestAttacherRevivalOfUnresolvableDependency checks that reviving stays bounded when the dependency can
// never solidify: the producer never arrives. B revives the abandoned A once, A is abandoned again, and B
// is abandoned at its own TTL, instead of restarting A on every pass or waiting forever. The node keeps running.
func TestAttacherRevivalOfUnresolvableDependency(t *testing.T) {
	testData, _, txBytesA, txBytesB, txidA, cleanup := abandonedDependencySetup(t)
	defer cleanup()

	errA := attachAndWait(t, testData, txBytesA, 30*time.Second)
	require.Error(t, errA)
	require.True(t, strings.Contains(errA.Error(), "stale attacher abandoned"), "expected A to be abandoned, got: %v", errA)

	errB := attachAndWait(t, testData, txBytesB, 40*time.Second)
	require.Error(t, errB)
	require.True(t, strings.Contains(errB.Error(), "stale attacher abandoned"), "expected B to be abandoned, got: %v", errB)

	// the revived A runs on its own fresh TTL; it must end abandoned too, leaving no attacher behind
	require.Eventually(t, func() bool { return attacher.NumAttachers() == 0 }, 40*time.Second, 100*time.Millisecond,
		"revived attachers must end")
	if vidA := testData.wrk.GetVertex(txidA); vidA != nil {
		require.EqualValues(t, vertex.Undefined, vidA.GetTxStatus())
	}
	require.False(t, testData.wrk.IsShuttingDown())
}
