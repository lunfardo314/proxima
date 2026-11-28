package txinput_queue

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// snapshotTestEnvironment is the attacher cap environment with a snapshot in progress.
type snapshotTestEnvironment struct {
	capTestEnvironment
}

func (snapshotTestEnvironment) IsSnapshotting() bool { return true }

// makeNonSequencerTx builds a parseable non-sequencer transaction with the given timestamp.
func makeNonSequencerTx(t *testing.T, ts base.LedgerTime) *transaction.Transaction {
	txb := txbuildercore.New(0)
	txb.SetTimestamp(ts)
	out := txbuildercore.NewOutputBuilder()
	out.MustPushConstraint([]byte{0x80})
	out.MustPushConstraint(nil)
	out.MustPushConstraint([]byte{0x80})
	txb.ProduceOutput(out.Bytes())
	tx, err := transaction.ParseLibraryAgnostic(txb.Bytes())
	require.NoError(t, err)
	return tx
}

// A snapshot write takes time linear in the state, so during it only unsolicited non-sequencer
// transactions are shed: a branch still attaches and the node stays in step with the network, while
// a pulled non-sequencer transaction is needed by a past cone and passes too.
func TestSnapshotShedsOnlyNonSequencer(t *testing.T) {
	q := &TxInputQueue{environment: snapshotTestEnvironment{}}

	nonSeq := makeNonSequencerTx(t, base.T(79300, 10))
	require.False(t, nonSeq.IsSequencerTransaction())
	require.False(t, q.shouldAttach(nonSeq, false), "an unsolicited non-sequencer transaction is shed during a snapshot")
	require.True(t, q.shouldAttach(nonSeq, true), "a pulled one is not")

	branch := makeSequencerTxForCapTest(t, 79300, 0)
	require.True(t, branch.IsBranchTransaction())
	require.True(t, q.shouldAttach(branch, false), "a branch attaches during a snapshot")
}
