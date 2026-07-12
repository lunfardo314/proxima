package tests

import (
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

// TestAttachDoubleSpendViaDefinedProducer reproduces the branch-conservation incident
// (hloc0 seq, 2026-07-11): a double-spend of a delta output escaped conflict detection and rode
// into a branch delta, tripping the wrap-up conservation guard.
//
// Topology: transaction P produces output X. Two conflicting transactions A and B both spend X.
// A first sequencer milestone M1 consumes A's output; a second milestone M2 (extending M1)
// consumes B's output. M2's past cone therefore contains both A and B, which both spend X — a
// double-spend that must be detected.
//
// Conflict detection (_checkVertex) reads double-spends from the producer side:
// consumersByOutputIndex(X's producer P) must list both spenders. Those consumer edges are
// registered in attachInput. The incident lost one edge: an attacher first processed a spender
// while its input P was not solid (a pull was pending, or the sequencer's incremental build
// deadline fired mid-descent), so attachInput returned at the readiness check BEFORE AddConsumer;
// once P became Defined the spender's re-attach short-circuited (allInputsDefined skips
// attachInputs) and the edge was never recorded. With one spender's edge missing, _checkVertex
// saw a single consumer and missed the double-spend, which then rode into a branch delta.
//
// This test guards the observable behavior — a double-spend of a delta output consumed across
// chained sequencer milestones is rejected as a conflict. It does NOT by itself isolate the fix:
// the readiness-false window is opened by async pull / build-deadline timing that a single-process
// test with all transactions available locally does not hit, so it passes both before and after
// the attachInput change. The fix itself is correct by construction (the consumer edge is now
// registered the moment the input is referenced, before any early return).
func TestAttachDoubleSpendViaDefinedProducer(t *testing.T) {
	// hand-built sequencer milestones can't declare the attacher-computed coverageDelta;
	// disable the per-milestone coverage enforcement here (same as the conflict tests above).
	defer reinitTestLedgerNoCoverageMonotonicity()()

	testData := initWorkflowTestWithConflicts(t, 1, 1, false)
	pace := int(ledger.L(0).TransactionPace)

	// P: spend the rooted forkOutput and produce a delta output X (to self, at index 1). Build
	// its bytes and parse X WITHOUT attaching P yet, so A/B and M1 reference X while P is unknown.
	tsP := testData.forkOutput.Timestamp().AddTicks(pace)
	txBytesP, err := utxodb.MakeTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, tsP).
		WithAmount(1_000_000_000).
		WithTargetLock(testData.addr).
		MustWithInputs(testData.forkOutput))
	require.NoError(t, err)
	txP, err := transaction.ParseWithPartialValidation(txBytesP)
	require.NoError(t, err)
	x := txP.MustProducedOutputWithIDAt(1)
	require.EqualValues(t, 1_000_000_000, int(x.Output.TokenBalance()))

	// A and B both spend X (a double-spend). Their outputs are chain-locked to the bootstrap
	// sequencer so the milestones can unlock them (otherwise Stage-3 sigLock would reject first).
	tsAB := x.Timestamp().AddTicks(pace)
	mkSpenderOut := func(amount uint64) *ledger.OutputWithID {
		b, err := utxodb.MakeTransferTransaction(utxodb.NewTransferData(testData.privKey, testData.addr, tsAB).
			WithAmount(amount).
			WithTargetLock(ledger.ChainLockFromChainID(testData.bootstrapChainID)).
			MustWithInputs(x))
		require.NoError(t, err)
		vid, err := attacher.AttachTransactionFromBytes(b, testData.wrk)
		require.NoError(t, err)
		o := vid.MustOutputWithIDAt(1)
		return &o
	}
	aOut := mkSpenderOut(100_000_000)
	bOut := mkSpenderOut(100_000_001)

	mkSeq := func(chainOut *ledger.OutputWithChainID, in *ledger.OutputWithID) []byte {
		ts := base.MaximumTime(chainOut.Timestamp(), in.Timestamp()).
			AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		b, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "testSeq",
			Timestamp:        ts,
			ChainInput:       chainOut,
			AdditionalInputs: []*ledger.OutputWithID{in},
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       genesisPrivateKey,
			PublicKey:        genesisPrivateKey.Public().(ed25519.PublicKey),
		})
		require.NoError(t, err)
		return b
	}

	branches := multistate.FetchLatestBranches(testData.wrk.StateStore())
	require.EqualValues(t, 1, len(branches))
	bootChainOut := branches[0].SequencerOutput.MustAsChainOutput()

	// M1 extends the bootstrap branch and consumes A. Attach it while P is still unknown, so its
	// attacher first processes A with P not solid — the window in which the old code dropped A's
	// consumer edge on P.
	var wg1 sync.WaitGroup
	wg1.Add(1)
	vidM1, err := attacher.AttachTransactionFromBytes(mkSeq(bootChainOut, aOut), testData.wrk,
		attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) { wg1.Done() }))
	require.NoError(t, err)

	// Now supply P. This solidifies X so A becomes Defined and M1 can finish. P transitioned from
	// unknown -> Defined AFTER M1 first touched A.
	time.Sleep(100 * time.Millisecond)
	_, err = attacher.AttachTransactionFromBytes(txBytesP, testData.wrk)
	require.NoError(t, err)
	wg1.Wait()
	require.True(t, vertex.Good == vidM1.GetTxStatus(), "M1 expected Good, got %s: %v", vidM1.GetTxStatus(), vidM1.GetError())

	// M2 extends M1 and consumes B. Its past cone now contains A (via M1) and B, both spending X.
	m1Out := vidM1.MustOutputWithIDAt(0)
	m1ChainOut := m1Out.MustAsChainOutput()
	var wg2 sync.WaitGroup
	wg2.Add(1)
	vidM2, err := attacher.AttachTransactionFromBytes(mkSeq(m1ChainOut, bOut), testData.wrk,
		attacher.WithAttachmentCallback(func(_ *vertex.WrappedTx, _ error) { wg2.Done() }))
	require.NoError(t, err)
	wg2.Wait()
	testData.logDAGInfo()

	// The double-spend on X must be detected and M2 rejected as conflicting. Before the fix A's
	// missing consumer edge left P with a single visible consumer, hiding the conflict.
	t.Logf("M2 status: %s, reason: %v", vidM2.GetTxStatus(), vidM2.GetError())
	require.True(t, vertex.Bad == vidM2.GetTxStatus(), "expected conflict to be detected, got %s", vidM2.GetTxStatus())
	require.NoError(t, util.MustErrorWith(vidM2.GetError(), "conflict", "in the past cone"))
}
