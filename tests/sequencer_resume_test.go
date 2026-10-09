package tests

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/stretchr/testify/require"
)

// A sequencer that resumes on its latest reliable branch builds its first branch directly
// on that branch's own sequencer output. The output is in the baseline state and its only
// consumer is the transaction being built, so the frozen coverage delta of the past cone
// must subtract its frozen amount: the stem builder adds the new tip's frozen amount on top
// of the baseline total, and without the subtraction the total grows by the sequencer's
// frozen delegations at every such resume, past the supply (observed 2026-10-09 on a
// sequencer restarting after twelve hours down: 54M + 18M > 60M, builder assertion, crash).
func TestResumeOnOwnBranchKeepsFrozenCoverage(t *testing.T) {
	td := initWorkflowTest(t, 1)
	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey)
	require.NoError(t, err)
	seq.Start()

	// wait for the sequencer to branch
	deadline := time.Now().Add(8 * ledger.SlotDuration())
	var lrb *multistate.BranchData
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && lrb.Stem.ID.Slot() > td.distributionBranchTxID.Slot() {
			break
		}
		require.True(t, time.Now().Before(deadline), "the sequencer did not branch")
		time.Sleep(ledger.TickDuration())
	}

	// the wallet delegates to the bootstrap sequencer, the tag-along gets the transaction taken
	rdr := multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
	outs, err := rdr.GetOutputsForAccount(td.addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outs)
	in := outs[0]
	holder := base.HolderIDFromED25519PrivateKey(td.privKey)
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	b := exhelp.New()
	_, _, err = b.ConsumeOutputsUnlock(in)
	require.NoError(t, err)
	_, err = b.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(in.Output.TokenBalance() - tagAlongFee)
		o.WithLock(ledger.NewDelegateLock(td.bootstrapChainID, holder, 900))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	_, err = b.ProduceOutput(ledger.NewTagAlongOutput(tagAlongFee, td.bootstrapChainID, holder))
	require.NoError(t, err)
	b.FinaliseAndSign(ts, td.privKey)
	_, err = td.wrk.TxBytesInForTests(b.Bytes())
	require.NoError(t, err)

	// the sequencer freezes the delegation: the reliable branch's frozen coverage turns positive
	deadline = time.Now().Add(30 * ledger.SlotDuration())
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && td.wrk.Branches().FrozenCoverage(lrb.TxID()) > 0 {
			break
		}
		require.True(t, time.Now().Before(deadline), "the delegation was never frozen")
		time.Sleep(ledger.TickDuration())
	}
	total := td.wrk.Branches().FrozenCoverage(lrb.TxID())
	lrbID := lrb.TxID()
	t.Logf("frozen coverage at %s: %d", lrbID.StringShort(), total)

	// the sequencer stops; its latest reliable branch carries its own output with the frozen delegation
	seq.Stop()
	time.Sleep(2 * ledger.SlotDuration())
	lrb = td.wrk.Branches().FindLatestReliableBranch()
	require.NotNil(t, lrb)
	branchVid := td.wrk.MustEnsureBranch(lrb.TxID())
	seqOut := branchVid.MustOutputAt(0)
	require.True(t, seqOut.IsSequencerOutput())
	own := seqOut.FrozenCoverage(0)
	require.Positive(t, own)
	total = td.wrk.Branches().FrozenCoverage(lrb.TxID())

	// the resume: a branch at the next slot boundary extending that output, nothing else
	a, err := attacher.NewIncrementalAttacher("resume", td.wrk, base.T(ledger.TimeNow().Slot+1, 0),
		vertex.WrappedOutput{VID: branchVid, Index: 0})
	require.NoError(t, err)
	defer a.Close()
	delta := a.SequencerFrozenCoverageDelta()
	require.EqualValues(t, -own, delta, "the consumed baseline output must be subtracted")
	// what buildStemLock emits: baseline + delta + the new tip, i.e. the total unchanged
	require.EqualValues(t, total, a.BaselineFrozenCoverage()+uint64(delta)+uint64(own))

	td.stop()
	td.waitStop()
}

// A node whose peers gossip branches far past its own latest branch is catching up, not on a
// stalled network: a sequencer started on its old state would double-spend its chain output
// into a lineage of its own (observed 2026-10-09 on a bootstrap sequencer restored from the
// genesis snapshot while the network was 4400 slots on). It must not start.
func TestSequencerWaitsBehindPeers(t *testing.T) {
	td := initWorkflowTest(t, 1)
	td.wrk.RecordBranchSlotFromPeers(ledger.TimeNow().Slot + 100)

	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey)
	require.NoError(t, err)
	var submitted atomic.Int32
	seq.OnMilestoneSubmittedVID(func(*vertex.WrappedTx) { submitted.Add(1) })
	seq.Start()

	time.Sleep(4 * ledger.SlotDuration())
	require.Zero(t, submitted.Load(), "a sequencer behind its peers must not start")

	td.stop()
	td.waitStop()
}
