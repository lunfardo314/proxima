package tests

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/stretchr/testify/require"
)

// A sequencer starting on a reliable branch loads, from the slots in which they can still be taken,
// the outputs its chain can unlock, reading them out of the branch's state. Here the state holds a
// delegation to the sequencer frozen a few slots before: an output whose lock names the chain, in
// the slots the start reads. The sequencer is stopped and a new one started on the same chain, which
// must load it and go on branching.
func TestSequencerRestartLoadsRecentOutputs(t *testing.T) {
	td := initWorkflowTest(t, 1)
	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey)
	require.NoError(t, err)
	seq.Start()

	// waitBranchAfter waits for a reliable branch past the slot
	waitBranchAfter := func(slot uint32, what string) *multistate.BranchData {
		deadline := time.Now().Add(30 * ledger.SlotDuration())
		for {
			if lrb := td.wrk.Branches().FindLatestReliableBranch(); lrb != nil && lrb.Stem.ID.Slot() > slot {
				return lrb
			}
			require.True(t, time.Now().Before(deadline), what)
			time.Sleep(ledger.TickDuration())
		}
	}
	lrb := waitBranchAfter(td.distributionBranchTxID.Slot(), "the sequencer did not branch")

	// the wallet delegates to the sequencer, which freezes the delegation
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

	deadline := time.Now().Add(30 * ledger.SlotDuration())
	for {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil && td.wrk.Branches().FrozenCoverage(lrb.TxID()) > 0 {
			break
		}
		require.True(t, time.Now().Before(deadline), "the delegation was never frozen")
		time.Sleep(ledger.TickDuration())
	}

	// the frozen delegation is in the state under the chain, in a slot the start reads
	cid := ledger.ChainLockFromChainID(td.bootstrapChainID).ControllerID()
	rdr = multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
	recent := 0
	for slot := lrb.Stem.ID.Slot() - min(lrb.Stem.ID.Slot(), ledger.L(lrb.Stem.ID.Slot()).TagAlongSlots); slot <= lrb.Stem.ID.Slot(); slot++ {
		require.NoError(t, rdr.IterateUTXOsForControllerInSlot(cid, slot, func(base.OutputID, []byte) bool {
			recent++
			return true
		}))
	}
	require.Positive(t, recent, "an output under the chain in the recent slots of the state")

	// restart: a new sequencer on the same chain loads its start tips from that state and branches
	seq.Stop()
	time.Sleep(2 * ledger.SlotDuration())
	stoppedAt := td.wrk.Branches().FindLatestReliableBranch().Stem.ID.Slot()
	// another name: the stopped instance keeps its background job names registered
	seq2, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey, sequencer.WithName("restarted"))
	require.NoError(t, err)
	seq2.Start()
	waitBranchAfter(stoppedAt, "the restarted sequencer did not branch")

	seq2.Stop()
	td.stop()
	td.waitStop()
}
