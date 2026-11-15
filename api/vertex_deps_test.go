package api

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestVertexDepsWithoutPredecessorInputs checks the dependency view of a stored transaction that is
// parsed but not validated: a branch whose sequencer output is a chain origin (no chain predecessor
// input) and whose stem names a predecessor it does not consume. The view leaves both indices unset
// instead of failing, since the DAG stream builds it for any transaction in the txstore.
func TestVertexDepsWithoutPredecessorInputs(t *testing.T) {
	ledger.InitWithTestingLedgerData()
	t.Cleanup(ledger.ResetForTesting)

	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	seqOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(1_000_000).WithLock(ledger.SigLockFromED25519PrivateKey(key))
		o.MustPushConstraint(ledger.NewChainOrigin(5).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
	})
	stem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(0).WithLock(&ledger.StemLock{})
		o.PutConstraint((&ledger.OracleData{}).Bytes(), ledger.ConstraintIndexChain)
	})
	tx, err := transaction.Parse(txbuildercore.SerializeRawTxBytes(&txbuildercore.TxRawData{
		UpgradeIndex:         ledger.L(0).UpgradeIndex(),
		Timestamp:            base.LedgerTime{Slot: 5}, // tick 0: a branch
		SignatureData:        append([]byte{base.SignatureTypeED25519}, make([]byte, 96)...),
		SequencerOutputIndex: 0,
		SequencerData:        []byte{0, 1}, // sequencer output 0, stem output 1
		OutputBytes:          [][]byte{seqOut.Bytes(), stem.Bytes()},
	}))
	require.NoError(t, err)
	require.NoError(t, tx.ValidatePartialContext(false)) // structural scans only: the fake carries no real signature
	require.True(t, tx.IsBranchTransaction())

	v := VertexWithDependenciesFromTransaction(tx)
	require.Nil(t, v.SequencerInputTxIndex)
	require.Nil(t, v.StemInputTxIndex)
}
