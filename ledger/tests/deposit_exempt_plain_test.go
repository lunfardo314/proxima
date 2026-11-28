package tests

import (
	"crypto/rand"
	"testing"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// DepositExemptOutputsArePlain is the ingress policy on outputs that pay no storage deposit: a
// tag-along or sendWithDeadline output may carry exactly its lock's index values and no chain
// constraint. Each case is a ledger-valid transaction with one such output; the policy accepts the
// shapes wallets and sequencer requests produce and refuses extra index values and a chain on a
// tag-along. An extra index value on a sigLock output is not its business: that output pays for it.
func TestDepositExemptOutputsArePlain(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	keys, _, addrs := u.GenerateAddresses(0, 1)
	key, addr := keys[0], addrs[0]
	require.NoError(t, u.TokensFromFaucet(addr, 1_000*base.PROX))
	withUpgrade7(t, u)
	_, _, other := u.GenerateAddress(2)
	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	src := outs[0]

	var target base.ChainID
	_, _ = rand.Read(target[:])
	tagAlong := &ledger.TagAlongLock{SenderID: base.HolderID(addr), TargetSequencerID: target}

	// validTx builds and validates a transfer of src producing one output built by build and the remainder
	validTx := func(amount uint64, build func(o *ledger.OutputBuilder)) *transaction.Transaction {
		txb := exhelp.New()
		_, err := txb.ConsumeOutput(src.Output, src.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(amount)
			build(o)
		}))
		require.NoError(t, err)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(src.Output.TokenBalance() - amount).WithLock(addr)
		}))
		require.NoError(t, err)
		txb.SetTimestamp(base.T(u7Slot+5, 40))
		txb.ComputeInputCommitment()
		txb.SignED25519(key)
		tx, err := transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
		require.NoError(t, err, "every case is valid in the ledger")
		return tx
	}

	plain := validTx(1, func(o *ledger.OutputBuilder) { o.WithLock(tagAlong) })
	require.True(t, plain.DepositExemptOutputsArePlain(), "a plain tag-along")

	request := validTx(1, func(o *ledger.OutputBuilder) {
		o.WithLock(tagAlong)
		o.MustPushConstraint(engine.InlineDataBytecode([]byte{1, 2, 3}))
	})
	require.True(t, request.DepositExemptOutputsArePlain(), "a tag-along carrying a request payload")

	extraValue := validTx(1, func(o *ledger.OutputBuilder) {
		o.WithLock(tagAlong)
		o.PutConstraint(ledger.IndexValuesTupleBytes(append(tagAlong.IndexValues(), other.ControllerID())), ledger.ConstraintIndexIndexValues)
	})
	require.False(t, extraValue.DepositExemptOutputsArePlain(), "a tag-along with an index value its lock does not read")

	chain := validTx(1, func(o *ledger.OutputBuilder) {
		o.WithLock(tagAlong)
		o.PutConstraint(ledger.NewChainOrigin(u7Slot+5).Bytes(), ledger.ConstraintIndexChain)
	})
	require.False(t, chain.DepositExemptOutputsArePlain(), "a tag-along chain origin")

	sigLockExtra := validTx(100*base.PROX, func(o *ledger.OutputBuilder) {
		o.WithLock(addr)
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{addr.ControllerID(), other.ControllerID()}), ledger.ConstraintIndexIndexValues)
	})
	require.True(t, sigLockExtra.DepositExemptOutputsArePlain(), "an output paying its deposit is not concerned")
}
