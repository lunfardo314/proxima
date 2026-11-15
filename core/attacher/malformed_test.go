package attacher

import (
	"crypto/ed25519"
	"math"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// malformedAmountTx builds a signed non-sequencer transaction with outputs of balance and other. A
// balance outside int64 reads as negative; other offsets it so the produced total stays positive
// and the transaction passes parsing and partial-context validation.
func malformedAmountTx(t *testing.T, balance uint64, other int64) *transaction.Transaction {
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	lock := ledger.SigLockFromED25519PrivateKey(key)
	builder := txbuildercore.New(ledger.L(0).UpgradeIndex())
	builder.ConsumeOutput(ledger.OutputBasic(1_000_000, lock).Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
	builder.ProduceOutput(ledger.OutputBasic(int64(balance), lock).Bytes()) // the encoder writes uint64(amount): the raw value
	builder.ProduceOutput(ledger.OutputBasic(other, lock).Bytes())
	builder.PutSignatureUnlock(0)
	builder.SetTimestamp(base.T(2, 10))
	builder.ComputeInputCommitment()
	builder.SignED25519(key)
	tx, err := transaction.Parse(builder.Bytes())
	require.NoError(t, err)
	require.NoError(t, tx.ValidatePartialContext(true))
	return tx
}

// fakeBranch serializes a branch whose stem output carries oracle as its element 3. It parses but
// would never pass validation, which is enough: CheckMalformed runs before validation.
func fakeBranch(t *testing.T, oracle []byte) *transaction.Transaction {
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	seqOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(1_000_000).WithLock(ledger.SigLockFromED25519PrivateKey(key))
		o.MustPushConstraint(ledger.NewChainOrigin(5).Bytes())
		o.MustPushConstraint(ledger.NewSequencerConstraint(0).Bytes())
	})
	stem := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(0).WithLock(&ledger.StemLock{})
		o.PutConstraint(oracle, ledger.ConstraintIndexChain)
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
	require.True(t, tx.IsBranchTransaction())
	require.NoError(t, tx.ValidatePartialContext(false)) // structural scans only: the fake carries no real signature
	return tx
}

// TestCheckMalformed checks that CheckMalformed refuses a token balance outside int64 (offset by another
// output so the transaction still parses) and a branch whose stem oracle element is empty or does not
// decode, and passes their well-formed counterparts.
func TestCheckMalformed(t *testing.T) {
	require.NoError(t, CheckMalformed(malformedAmountTx(t, 1_000, 2)))
	require.Error(t, CheckMalformed(malformedAmountTx(t, math.MaxUint64, 2)))         // reads as -1
	require.Error(t, CheckMalformed(malformedAmountTx(t, math.MaxUint64-999, 1_001))) // reads as -1000

	require.NoError(t, CheckMalformed(fakeBranch(t, (&ledger.OracleData{}).Bytes())))
	require.Error(t, CheckMalformed(fakeBranch(t, []byte{})))
	require.Error(t, CheckMalformed(fakeBranch(t, []byte{0x81, 0x01})))
}

// TestAttachRefusesMalformed checks that AttachTransaction refuses such a transaction on every path,
// not only with the timestamp option: no vertex is created and the attachment callback receives the
// error.
func TestAttachRefusesMalformed(t *testing.T) {
	env := &futureTimestampEnv{global: global.NewDefault()}
	for _, tx := range []*transaction.Transaction{malformedAmountTx(t, math.MaxUint64, 2), fakeBranch(t, []byte{})} {
		var callbackErr error
		vid := AttachTransaction(tx, env, WithAttachmentCallback(func(v *vertex.WrappedTx, err error) {
			require.Nil(t, v)
			callbackErr = err
		}))
		require.Nil(t, vid)
		require.Error(t, callbackErr)
	}
}
