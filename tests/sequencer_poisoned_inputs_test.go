package tests

import (
	"errors"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// poisonSource passes in the produced context and fails whenever the output is consumed in a
// sequencer transaction: its owner can still spend it in an ordinary transaction, while any
// milestone consuming it is invalid.
const poisonSource = "or(selfIsProducedOutput, not(isSequencerTransaction))"

func compilePoison(t *testing.T) []byte {
	_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(poisonSource)
	require.NoError(t, err)
	return code
}

// withGenesisBeforeUpgrade7 runs the test on a genesis folding upgrades 1 to 6 only, upgrade 7 listed at
// its live slot far ahead: from upgrade 7 a delegation has exactly five elements, so one carrying a
// middle constraint exists only if made before it.
func withGenesisBeforeUpgrade7(t *testing.T) {
	extraLedgerOptions = []ledger.ParametersOption{ledger.WithFoldedUpgrades(6)}
	reinitTestLedger()
	t.Cleanup(func() {
		extraLedgerOptions = nil
		reinitTestLedger()
	})
}

// makeDelegationWithMiddle turns a chain origin of the master into a delegation to the sequencer
// carrying the given constraint between the chain constraint and the state, where the ledger
// allows any constraint and obliges the target to keep it.
func (td *testWithUTXODBData) makeDelegationWithMiddle(t *testing.T, amount uint64, middle []byte) base.ChainID {
	origin, err := td.u.CreateChainOrigin(td.masterPrivateKey, td.seqOutput().ID.Timestamp().AddSlots(1), amount)
	require.NoError(t, err)

	txb := exhelp.New()
	_, err = txb.ConsumeOutput(origin.Output, origin.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutConstraint(origin.Output.Amounts().Bytes(), ledger.ConstraintIndexAmounts)
		o.WithLock(ledger.NewDelegateLock(td.seqID, base.HolderID(td.masterAddr), 980))
		o.PutConstraint(ledger.NewChainConstraint(origin.ChainID, 0, origin.OriginSlot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(middle)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	txb.SetTimestamp(origin.ID.Timestamp().AddSlots(1))
	txb.ComputeInputCommitment()
	txb.SignED25519(td.masterPrivateKey)
	txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, txStr)
	require.NoError(t, td.u.AddTransaction(txBytes))
	return origin.ChainID
}

// A delegation carrying a constraint between the chain constraint and the state is not of the
// fixed layout, so the sequencer's pool never takes it. Frozen anyway, the milestone is invalid,
// harmless constraint or not, and the validation error names the delegation's input, which the
// proposer then blacklists.
func TestDelegationWithMiddleConstraint(t *testing.T) {
	withGenesisBeforeUpgrade7(t)
	td, ts := newTestWithUTXODBData(t, 1)
	harmless, poisoned := td.makeDelegationWithMiddle(t, 1_000_000_000, ledger.NewTimelock(1).Bytes()),
		td.makeDelegationWithMiddle(t, 1_000_000_000, compilePoison(t))

	ts = base.MaximumTime(ts, td.delegationChain(0).ID.Timestamp()).AddSlots(5)
	byID := make(map[base.ChainID]ledger.DelegationOutput)
	for _, d := range td.freezableDelegations(ts) {
		byID[d.ChainID] = d
	}
	honest := byID[td.delegationIDs[0]]
	require.True(t, honest.HasStandardLayout())

	for _, id := range []base.ChainID{harmless, poisoned} {
		d, ok := byID[id]
		require.True(t, ok)
		require.False(t, d.HasStandardLayout(), "a delegation with any constraint between chain and state is not of the fixed layout")

		txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
		require.NoError(t, err)
		require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
		_, valid, err := txb.FreezeDelegation(&d)
		require.NoError(t, err)
		require.True(t, valid)
		_, _, _, err = txbtest.BuildAndValidate(txb)
		require.Error(t, err)
		var cf *transaction.ConstraintFailure
		require.True(t, errors.As(err, &cf), "the failure carries the constraint's path")
		idx, ok := cf.FailingInput()
		require.True(t, ok)
		require.EqualValues(t, 1, idx, "input 0 is the sequencer's chain, input 1 the delegation")
	}
}

// An askstop request carrying at element 4 anything but its ensureStopDelegation is refused by
// the request parser for good: consumed in a milestone, such a constraint could refuse every
// sequencer transaction.
func TestAskStopWithForeignElement4Refused(t *testing.T) {
	td, ts := newTestWithUTXODBData(t, 1)
	ts = base.MaximumTime(ts, td.delegationChain(0).ID.Timestamp()).AddSlots(2)

	// the sequencer freezes the delegation
	txb, err := txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
	require.NoError(t, err)
	require.NoError(t, txb.AddEndorsement(base.RandomTransactionID(true, 2, base.T(ts.Slot, 0))))
	freezable := td.freezableDelegations(ts)
	require.Len(t, freezable, 1)
	_, _, err = txb.FreezeDelegation(&freezable[0])
	require.NoError(t, err)
	txBytes, _, txStr, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, txStr)
	require.NoError(t, td.u.AddTransaction(txBytes))

	// the master posts an askstop with the poison at element 4
	par := smallkv.New()
	par.Set(txbuilder_seq.FieldCmdCode, []byte{txbuilder_seq.RequestCodeAskStopDelegation})
	par.Set(txbuilder_seq.FieldRevokeDelegationID, td.delegationIDs[0][:])
	req := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(10_000_000)
		o.WithLock(&ledger.TagAlongLock{TargetSequencerID: td.seqID, SenderID: base.HolderID(td.masterAddr)})
		o.MustPushConstraint(easyfl.InlineDataBytecode(par.Bytes()))
		o.MustPushConstraint(compilePoison(t))
	})
	ts = ts.AddSlots(1)
	require.NoError(t, td.u.SendOutput(td.masterPrivateKey, req, base.T(ts.Slot, 50)))

	ts = ts.AddSlots(1)
	txb, err = txbuilder_seq.NewWithSequencerID(ts, td.seqID, td.targetPrivateKey, td.u.SugaredStateReader())
	require.NoError(t, err)
	backlog := td.tagAlongBacklog()
	require.Len(t, backlog, 1)
	_, valid, err := txb.AddTagAlongInput(backlog[0])
	require.ErrorContains(t, err, "guarantor")
	require.False(t, valid, "refused for good")
}

// End to end on a running v3 sequencer: a delegation to it carrying a constraint between the chain
// constraint and the state (an already expired timelock) used to stop it freezing and taking any
// tag-along, since every milestone with inputs failed. The pool now leaves such a delegation out:
// the honest delegation is frozen and a later transfer is included.
func TestSequencerIgnoresDelegationWithMiddleConstraint(t *testing.T) {
	withGenesisBeforeUpgrade7(t)
	td := initWorkflowTest(t, 1)
	seq, err := newTestSequencer(td.wrk, td.bootstrapChainID, genesisPrivateKey)
	require.NoError(t, err)
	seq.Start()
	defer func() { td.stop(); td.waitStop() }()

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
	half := in.Output.TokenBalance() / 2
	b := exhelp.New()
	_, _, err = b.ConsumeOutputsUnlock(in)
	require.NoError(t, err)
	// the honest delegation
	_, err = b.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(half)
		o.WithLock(ledger.NewDelegateLock(td.bootstrapChainID, holder, 900))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	// the delegation with one harmless constraint between the chain constraint and the state
	_, err = b.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(in.Output.TokenBalance() - half - tagAlongFee)
		o.WithLock(ledger.NewDelegateLock(td.bootstrapChainID, holder, 0))
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.MustPushConstraint(ledger.NewTimelock(1).Bytes())
		o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
	}))
	require.NoError(t, err)
	_, err = b.ProduceOutput(ledger.NewTagAlongOutput(tagAlongFee, td.bootstrapChainID, holder))
	require.NoError(t, err)
	b.FinaliseAndSign(ts, td.privKey)
	_, err = td.wrk.TxBytesInForTests(b.Bytes())
	require.NoError(t, err)

	// wait for the delegations to become freezable, then send a plain transfer with a tag-along
	time.Sleep(3 * ledger.SlotDuration())
	lrb = td.wrk.Branches().FindLatestReliableBranch()
	rdr = multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
	auxOuts, err := rdr.GetOutputsForAccount(td.addrAux.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, auxOuts)
	ts2 := ledger.TimeNow()
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	b2 := exhelp.New()
	_, _, err = b2.ConsumeOutputsUnlock(auxOuts[0])
	require.NoError(t, err)
	_, err = b2.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(auxOuts[0].Output.TokenBalance() - tagAlongFee).WithLock(td.addr)
	}))
	require.NoError(t, err)
	auxHolder := base.HolderIDFromED25519PrivateKey(td.privKeyAux)
	_, err = b2.ProduceOutput(ledger.NewTagAlongOutput(tagAlongFee, td.bootstrapChainID, auxHolder))
	require.NoError(t, err)
	b2.FinaliseAndSign(ts2, td.privKeyAux)
	txid2, err := td.wrk.TxBytesInForTests(b2.Bytes())
	require.NoError(t, err)

	deadline = time.Now().Add(25 * ledger.SlotDuration())
	frozen, included := false, false
	for time.Now().Before(deadline) && !(frozen && included) {
		lrb = td.wrk.Branches().FindLatestReliableBranch()
		if lrb != nil {
			frozen = td.wrk.Branches().FrozenCoverage(lrb.TxID()) > 0
			rdr = multistate.MustNewSugaredReadableState(td.wrk.StateStore(), lrb.Root, 0)
			included = rdr.KnowsCommittedTransaction(txid2)
		}
		time.Sleep(ledger.TickDuration())
	}
	require.True(t, frozen, "the honest delegation is frozen")
	require.True(t, included, "the later transfer is included")
}
