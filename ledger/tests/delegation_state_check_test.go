package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// compileState compiles a delegateLockState from source, so that its arguments can be encoded
// with lengths the Go parser does not accept while the EasyFL rule, reading them by value, does.
func compileState(t *testing.T, src string) []byte {
	_, _, bin, err := ledger.L(base.MaxSlot).CompileExpression(src)
	require.NoError(t, err)
	return bin
}

// holdWithState builds the target's hold of the test delegation in its own milestone, the
// produced on-hold delegation carrying the given state bytes. The sequencer output's frozen
// coverage follows from the produced delegation's cells (pred + sum = 2 succ). It returns the
// transaction validated by the ledger, without adding it to the state.
func (td *testData) holdWithState(ts base.LedgerTime, state []byte) (*transaction.Transaction, []byte, error) {
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

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		td.Logf("failing tx:\n%s", txString)
		return nil, nil, err
	}
	tx, err := transaction.ParseAndValidate(txBytes, txb.LoadInputBytes)
	require.NoError(td, err)
	return tx, txBytes, nil
}

// A target puts its credited frozen delegation on hold in its own milestone with a zero last
// frozen epoch encoded in 5 bytes. The EasyFL rule accepts the hold, the frozen-coverage builtin
// reads the state as undef and does not debit the target, so the target keeps the coverage while
// the master takes the tokens back. CheckDelegationStates refuses the transaction; the same hold
// with the canonical state passes it and debits the target.
func TestCheckDelegationStatesRefusesNonCanonicalHold(t *testing.T) {
	saved, savedBound := transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot
	transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = 0, 0
	defer func() {
		transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = saved, savedBound
	}()

	td := &testData{T: t, seqBalance: 20_000_000_000}
	td.init()
	withUpgrade6(t, td.u)

	at := uint32(upgrade6Slot + 10)
	_, _, err := td.initDelegationUTXOMake(base.T(at, 10), 4, 0)
	require.NoError(t, err)
	ts := base.T(at+1, 10)
	require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts: ts, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), inflate: true, disableConsistencyChecks: true,
	}))
	require.True(t, td.delegatedOutput.IsMarkedFrozen())
	credited := int64(td.delegatedOutput.Output.TokenBalance())
	require.EqualValues(t, credited, td.seqChainOrigin.Output.FrozenCoverage(0), "the freeze credits the target")

	// the attack: the ledger accepts it and the target is not debited; the node check refuses it
	tx, _, err := td.holdWithState(base.T(at+2, 10), compileState(t, "delegateLockState(0x0000000000, 2, 0x0000)"))
	require.NoError(t, err, "the ledger accepts the non-canonical on-hold state")
	require.True(t, tx.SequencerOutput().Output.FrozenCoverage(0) > 0, "the hold does not debit the target")
	require.ErrorContains(t, tx.CheckDelegationStates(), "non-canonical state")
	// the frozen-coverage bound, independent of the state check, refuses it too
	require.ErrorContains(t, tx.CheckFrozenCoverageBound(), "exceeds the bound")

	// below the activation slot the check stays silent
	transaction.DelegationStateCheckFromSlot = at + 3
	require.NoError(t, tx.CheckDelegationStates())
	transaction.DelegationStateCheckFromSlot = 0

	// a share in 3 bytes: the EasyFL rule refuses a share above 1000 by value, so only an
	// overlong zero or small share reaches the parser, and the check refuses it too
	if tx3, _, err3 := td.holdWithState(base.T(at+2, 10), compileState(t, "delegateLockState(0x00000000, 2, 0x000000)")); err3 == nil {
		require.ErrorContains(t, tx3.CheckDelegationStates(), "non-canonical state")
	}

	// the honest hold with the canonical state passes the check and debits the target
	tx, _, err = td.holdWithState(base.T(at+2, 10), nil)
	require.NoError(t, err)
	require.NoError(t, tx.CheckDelegationStates())
	require.NoError(t, tx.CheckStemAndTargetUnlocks())
	require.NoError(t, tx.CheckFrozenCoverageBound())
	require.EqualValues(t, 0, tx.SequencerOutput().Output.FrozenCoverage(0), "the canonical hold debits the target")
}

// A delegation is of a fixed layout: amounts, index values, lock, chain constraint and state.
// CheckDelegationStates refuses a transaction producing one with anything between the chain
// constraint and the state, which the ledger itself still accepts.
func TestCheckDelegationStatesRefusesExtraElements(t *testing.T) {
	saved := transaction.DelegationStateCheckFromSlot
	transaction.DelegationStateCheckFromSlot = 0
	defer func() { transaction.DelegationStateCheckFromSlot = saved }()

	td := &testData{T: t, seqBalance: 20_000_000_000}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))

	makeTx := func(extra []byte) *transaction.Transaction {
		par, err := td.u.MakeTransferInputData(td.masterPrivateKey, nil, ts)
		require.NoError(t, err)
		par = par.WithAmount(1_000_000_000).
			WithTargetLock(ledger.NewDelegateLock(td.seqChainOrigin.ChainID, base.HolderID(td.masterAddr), 0)).
			WithConstraint(ledger.NewChainOrigin(ts.Slot))
		if extra != nil {
			par = par.WithConstraint(ledger.NewGeneralScript(extra))
		}
		txBytes, err := utxodb.MakeSimpleTransferTransaction(par.WithConstraint(ledger.DelegateLockState{}))
		require.NoError(t, err)
		tx, err := transaction.ParseAndValidate(txBytes, nil)
		require.NoError(t, err)
		return tx
	}
	require.NoError(t, makeTx(nil).CheckDelegationStates(), "the fixed layout passes")
	require.ErrorContains(t, makeTx(ledger.NewTimelock(1).Bytes()).CheckDelegationStates(), "has 6 elements")
}
