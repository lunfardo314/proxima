package tests

import (
	"bytes"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// From library upgrade 7 an output may carry no index value longer than ledger.MaxIndexValueLength
// (255 bytes): the account key stores an index value's length in one byte, so a longer one wraps and
// lands under another account's prefix. A transfer whose output carries, beside its holder, an index
// value of 256 bytes is valid before the slot and refused from it; one of 255 bytes stays valid.
func TestIndexValueLength(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	keys, _, addrs := u.GenerateAddresses(0, 1)
	key, addr := keys[0], addrs[0]
	require.NoError(t, u.TokensFromFaucet(addr, 1_000*base.PROX))
	withUpgrade7(t, u)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outs)
	src := outs[0]

	// transfer validates a transfer of src to the same address, the output also indexed under extra
	transfer := func(ts base.LedgerTime, extra []byte) error {
		txb := exhelp.New()
		_, err := txb.ConsumeOutput(src.Output, src.ID)
		require.NoError(t, err)
		txb.PutSignatureUnlock(0)
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(src.Output.TokenBalance()).WithLock(addr)
			o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{addr[:], extra}), ledger.ConstraintIndexIndexValues)
		}))
		require.NoError(t, err)
		txb.SetTimestamp(ts)
		txb.ComputeInputCommitment()
		txb.SignED25519(key)
		_, err = transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
		return err
	}
	long := bytes.Repeat([]byte{7}, ledger.MaxIndexValueLength+1)
	limit := bytes.Repeat([]byte{7}, ledger.MaxIndexValueLength)

	require.NoError(t, transfer(base.T(u7Slot-5, 40), long), "before the slot, a 256-byte index value is valid")
	err = transfer(base.T(u7Slot+5, 40), long)
	require.Error(t, err)
	require.Contains(t, err.Error(), "index value of 256 bytes")
	require.NoError(t, transfer(base.T(u7Slot+5, 40), limit), "255 bytes stay valid")
}
