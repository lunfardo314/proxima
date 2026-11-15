package attacher

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// futureTimestampEnv implements only the logger: a refused transaction must not reach anything else,
// and the embedded nil interface panics if it does.
type futureTimestampEnv struct {
	Environment
	global *global.Global
}

func (e *futureTimestampEnv) Log() *zap.SugaredLogger { return e.global.Log() }

// TestAttachRefusesFutureTimestamp checks that a transaction from outside the node, stamped after the
// current ledger time, is refused by AttachTransaction rather than asserted: no panic, no vertex, and
// the attachment callback receives the error. Both a timestamp one slot ahead and one whose clock time
// lies past the int64 nanosecond range are covered.
func TestAttachRefusesFutureTimestamp(t *testing.T) {
	// the ledger is initialized by the package's init; the signature is never checked on this path
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	env := &futureTimestampEnv{global: global.NewDefault()}

	for _, ts := range []base.LedgerTime{ledger.TimeNow().AddSlots(1), base.T(1_400_000_000, 0)} {
		output := ledger.OutputBasic(1_000_000, ledger.SigLockFromED25519PrivateKey(key))
		builder := txbuildercore.New(ledger.L(0).UpgradeIndex())
		builder.ConsumeOutput(output.Bytes(), base.MustNewOutputID(base.RandomTransactionID(false, 0, base.T(0, 0)), 0))
		builder.ProduceOutput(output.Bytes())
		builder.PutSignatureUnlock(0)
		builder.SetTimestamp(ts)
		builder.ComputeInputCommitment()
		builder.SignED25519(key)
		tx, err := transaction.Parse(builder.Bytes())
		require.NoError(t, err)

		var callbackErr error
		vid := AttachTransaction(tx, env, WithEnforceTimestampBeforeRealTime, WithAttachmentCallback(func(v *vertex.WrappedTx, err error) {
			require.Nil(t, v)
			callbackErr = err
		}))
		require.Nil(t, vid, "%s", ts.String())
		require.Error(t, callbackErr, "%s", ts.String())
	}
}
