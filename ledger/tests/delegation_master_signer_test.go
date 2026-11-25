package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// A delegation origin may name a master other than the transaction's signer: the delegate
// lock checks the master only when the delegation is consumed. This is what lets a hot key
// create a delegation mastered by a cold key that never signs (kb/cold_wallet.md).
func TestDelegationOriginMasterNotSigner(t *testing.T) {
	for _, c := range []struct {
		name     string
		upgrade7 bool
	}{
		{"current library", false},
		{"with upgrade 7", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			td := &testData{T: t}
			td.init()
			if c.upgrade7 {
				withUpgrade7(t, td.u)
			}
			// the payer signs and funds the origin; td.masterAddr is only named as master
			payerKey, _, payer := td.u.GenerateAddress(7)
			require.NoError(t, td.u.TokensFromFaucet(payer, 10*delegatedTokens))
			require.NotEqual(t, payer, td.masterAddr)

			ts := td.seqChainOrigin.Timestamp().AddSlots(1)
			par, err := td.u.MakeTransferInputData(payerKey, nil, ts)
			require.NoError(t, err)
			txBytes, err := utxodb.MakeSimpleTransferTransaction(
				par.WithAmount(delegatedTokens).
					WithTargetLock(ledger.NewDelegateLock(td.target, base.HolderID(td.masterAddr), 0)).
					WithConstraint(ledger.NewChainOrigin(ts.Slot)).
					WithConstraint(ledger.DelegateLockState{State: ledger.DelegateLockStateUndef}),
			)
			require.NoError(t, err)
			require.NoError(t, td.u.AddTransaction(txBytes))

			outs, err := td.u.SugaredStateReader().GetOutputsDelegatedToAccount2(td.target[:])
			require.NoError(t, err)
			require.EqualValues(t, 1, len(outs))
			d, ok := ledger.DelegationOutputFromOutputWithChainID(outs[0])
			require.True(t, ok)
			require.EqualValues(t, base.HolderID(td.masterAddr), d.MasterID, "the master is the named key, not the signer")
		})
	}
}
