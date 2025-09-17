package tests

import (
	"slices"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

func TestTagAlongSimple(t *testing.T) {
	const (
		initAmount = 1_000_000_000_000
		fee        = 500
	)
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(314, 1, initAmount)
	targetChainID := base.RandomChainID()
	privKey := privKeys[0]
	addr := addrs[0]

	txb := txbuilder.New()
	ts := base.T(100, 20)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.AccountID())
	require.NoError(t, err)
	require.True(t, len(outs) > 0)

	require.True(t, outs[0].Output.TokenBalance() == initAmount)

	_, err = txb.ConsumeOutput(outs[0].Output, outs[0].ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	o := ledger.NewTagAlongOutput(fee, targetChainID, privKey)
	t.Logf("\ntarget ID: %s\naddr: %s, tag-along output:\n%s", targetChainID.String(), addr.String(), o.String())
	_, err = txb.ProduceOutput(o)
	require.NoError(t, err)

	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(initAmount - fee).WithLock(addr)
	}))
	require.NoError(t, err)

	txb.TransactionData.Timestamp = ts.AddSlots(1)
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)

	txBytes, txid, txString, err := txb.BytesWithValidation()
	t.Logf("\n%s", txString)
	require.NoError(t, err)

	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	outsMaster, err := u.SugaredStateReader().GetOutputsForAccount(addr.AccountID())
	require.NoError(t, err)
	require.True(t, len(outsMaster) == 2)

	outsTarget, err := u.SugaredStateReader().GetOutputsForAccount(ledger.ChainLockFromChainID(targetChainID).AccountID())
	require.NoError(t, err)
	require.True(t, len(outsTarget) == 1)

	taOid := base.MustNewOutputID(txid, 0)

	idxMaster := slices.IndexFunc(outsMaster, func(o *ledger.OutputWithID) bool {
		return o.ID == taOid
	})
	require.True(t, idxMaster >= 0)
	idxTarget := slices.IndexFunc(outsTarget, func(o *ledger.OutputWithID) bool {
		return o.ID == taOid
	})
	require.True(t, idxTarget >= 0)
}
