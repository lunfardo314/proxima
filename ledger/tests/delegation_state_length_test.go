package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// compileState compiles a delegateLockState from source, so that its arguments can be encoded
// with lengths the node's parser (DelegateLockStateFromBytesWithLib) does not accept.
func compileState(t *testing.T, src string) []byte {
	_, _, bin, err := ledger.L(base.MaxSlot).CompileExpression(src)
	require.NoError(t, err)
	return bin
}

// holdWithState is the target's hold of its frozen delegation in its own milestone, the produced
// on-hold delegation carrying the given delegateLockState bytes and no frozen-coverage cells (nil
// state: the canonical hold as the builder makes it). The sequencer output's frozen
// coverage is computed from the produced delegation by the node's parser, as a sequencer would.
func (td *testData) holdWithState(ts base.LedgerTime, state []byte) error {
	txb := exhelp.New()
	_, _, err := txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)
	cc := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	seqIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance()))
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	predIdx, err := txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	require.NoError(td, err)
	onHold, err := td.delegatedOutput.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs: ts, PredOutputIndex: predIdx, DisableConsistencyChecks: true,
	})
	require.NoError(td, err)
	if state != nil {
		// keep the balance, drop every frozen-coverage cell, swap in the given state
		onHold = onHold.Clone(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(onHold.TokenBalance()))
			o.PutConstraint(state, byte(onHold.NumElements()-1))
		})
	}
	succIdx, err := txb.ProduceOutput(onHold)
	require.NoError(td, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), ledger.DelegationUnlockedByTarget)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	fc, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)
	txb.MustPutFrozenCoverage(seqIdx, fc, ts)
	txb.SetSequencerData(seqIdx, txbuildercore.SequencerOutputIndexNone)
	txb.PushEndorsements(base.NewTransactionID(ts.AddTicks(-5), base.TransactionIDShort{}, true))
	txb.ComputeInputCommitment()
	txb.SetTimestamp(ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, _, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		return err
	}
	_, err = transaction.ParseAndValidate(txBytes, txb.LoadInputBytes)
	require.NoError(td, err)
	require.NoError(td, td.u.AddTransaction(txBytes))
	td.delegatedOutput, err = td.u.SugaredStateReader().GetDelegatedOutput(td.delegatedOutput.ChainID)
	require.NoError(td, err)
	td.seqChainOrigin, err = td.u.SugaredStateReader().GetChainOutputWithChainID(td.seqChainOrigin.ChainID)
	require.NoError(td, err)
	return nil
}

// From library upgrade 7 the epoch and the share of a produced delegateLockState are bounded by
// length, at most 4 and 2 bytes, so the ledger and the node read the same state. A target's hold
// with a 5-byte epoch or a 3-byte share is refused; the canonical hold is accepted and debits the
// target's frozen coverage.
func TestDelegationStateLength(t *testing.T) {
	td := &testData{T: t, seqBalance: 20_000_000_000, seqExtension: true}
	td.init()
	withUpgrade7(t, td.u)

	at := uint32(u7Slot + 10)
	_, _, err := td.initDelegationUTXOMake(base.T(at, 10), 4, 0)
	require.NoError(t, err)
	ts := base.T(at+1, 10)
	require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts: ts, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), inflate: true, disableConsistencyChecks: true,
	}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())
	credited := int64(td.delegatedOutput.Output.TokenBalance())
	require.EqualValues(t, credited, td.seqChainOrigin.Output.FrozenCoverage(0), "the freeze credits the target")

	hold := base.T(at+2, 10)
	err = td.holdWithState(hold, compileState(t, "delegateLockState(0x0000000000, 2, 0x0000)"))
	require.Error(t, err, "a 5-byte epoch is refused")
	require.Contains(t, err.Error(), "delegateLockState $0 must be at most 4 bytes")

	err = td.holdWithState(hold, compileState(t, "delegateLockState(0x00000000, 2, 0x000000)"))
	require.Error(t, err, "a 3-byte share is refused")
	require.Contains(t, err.Error(), "delegateLockState $2 must be at most 2 bytes")

	require.NoError(t, td.holdWithState(hold, nil))
	require.EqualValues(t, 0, td.seqChainOrigin.Output.FrozenCoverage(0), "the canonical hold debits the target")
}

// From library upgrade 7 a produced delegation has exactly five elements: amounts, index values,
// the lock, the chain and the state. A master delegating a chain that carries one more element is
// accepted before the slot and refused from it; the plain delegation stays valid.
func TestDelegationLayout(t *testing.T) {
	td := &testData{T: t, seqBalance: 20_000_000_000, seqExtension: true}
	td.init()
	withUpgrade7(t, td.u)
	extra := compileState(t, "equal(u64/1, u64/1)")

	// delegate makes a chain origin at slot at-1 and delegates it to the sequencer at slot at
	delegate := func(at uint32, extra []byte) error {
		origin, err := td.u.CreateChainOrigin(td.masterPrivateKey, base.T(at-1, 10), 1000*base.PROX)
		require.NoError(t, err)
		txb := exhelp.New()
		_, err = txb.ConsumeOutput(origin.Output, origin.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.PutConstraint(origin.Output.Amounts().Bytes(), ledger.ConstraintIndexAmounts)
			o.WithLock(ledger.NewDelegateLock(td.seqChainOrigin.ChainID, base.HolderID(td.masterAddr), 0))
			o.PutConstraint(ledger.NewChainConstraint(origin.ChainID, 0, origin.OriginSlot, 0, 0, 1, 0).Bytes(), ledger.ConstraintIndexChain)
			if extra != nil {
				o.MustPushConstraint(extra)
			}
			o.MustPushConstraint(ledger.DelegateLockState{}.Bytes())
		}))
		require.NoError(t, err)
		txb.SetTimestamp(base.T(at, 10))
		txb.ComputeInputCommitment()
		txb.SignED25519(td.masterPrivateKey)
		_, err = transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
		return err
	}
	require.NoError(t, delegate(u7Slot-5, extra), "before the slot an extra element is valid")
	err := delegate(u7Slot+5, extra)
	require.Error(t, err)
	require.Contains(t, err.Error(), "delegation output must have exactly 5 elements")
	require.NoError(t, delegate(u7Slot+10, nil))
}
