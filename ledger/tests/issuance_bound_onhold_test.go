package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// An on-hold delegation carries the hold's debit as a negative frozen-coverage cell. Read as
// unsigned it would saturate the issuance bound, which would then accept any declared inflation
// in a transaction consuming such an output. The master spends an on-hold delegation declaring
// 600,000 PROX of inflation (the honest bound is about 2 PROX): the ledger refuses it, and so must
// CheckIssuanceBound.
func TestIssuanceBoundOnHoldDelegationInput(t *testing.T) {
	saved := transaction.IssuanceBoundFromSlot
	transaction.IssuanceBoundFromSlot = 0
	t.Cleanup(func() { transaction.IssuanceBoundFromSlot = saved })

	td := &testData{T: t}
	td.init()
	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	_, _, err := td.initDelegationUTXOMake(ts, 4, 0)
	require.NoError(t, err)

	ts = td.timestampSlotsForward(1)
	err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts: ts, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), disableConsistencyChecks: true,
	})
	require.NoError(t, err)
	// the honest hold by the target in its own milestone leaves a negative cell 0
	ts = base.T(td.delegatedOutput.UnfreezeSlot()-10, 5)
	require.NoError(t, td.revokeDelegation(ts, false, false))
	require.True(t, td.delegatedOutput.IsMarkedOnHold())
	require.Less(t, td.delegatedOutput.Output.Amounts().FrozenCoverageAt(0), int64(0))

	const fabricated = uint64(600 * base.KPROX)
	d := td.delegatedOutput
	txb := exhelp.New()
	_, _, err = txb.ConsumeOutputsNoUnlock(&d.OutputWithID)
	require.NoError(t, err)
	txb.PutUnlockParams(0, ledger.ConstraintIndexLock, []byte{0xff, 0xff})
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))
	cc := ledger.NewChainConstraint(d.ChainID, 0, d.OriginSlot, d.CumulativeChainInflation+fabricated, d.CumulativeBranchBonus, d.TransitionCounter+1, d.BranchCounter)
	_, err = txb.ProduceOutput(d.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(d.Output.TokenBalance()+fabricated), int64(fabricated))
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(t, err)
	txb.ComputeInputCommitment()
	txb.SetTimestamp(d.Timestamp().AddSlots(5))
	txb.SignED25519(td.masterPrivateKey)
	tx, ledgerErr := transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
	require.NotNil(t, tx)
	require.Error(t, ledgerErr, "the ledger refuses the fabricated inflation")
	require.ErrorContains(t, tx.CheckIssuanceBound(), "exceeds the issuance bound")
}
