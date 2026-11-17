package txinput_queue

import (
	"testing"

	"go.uber.org/zap"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/blocklist"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// blocklistTestEnvironment adds a memDAG holding one virtual vertex: the ID an attacher waits for.
type blocklistTestEnvironment struct {
	forgedCopyTestEnvironment
	vertices map[base.TransactionID]*vertex.WrappedTx
}

func (e *blocklistTestEnvironment) Log() *zap.SugaredLogger { return zap.NewNop().Sugar() }

func (e *blocklistTestEnvironment) GetVertex(txid base.TransactionID) *vertex.WrappedTx {
	return e.vertices[txid]
}

// TestBlocklistRefusalReleasesWaiters covers a pulled transaction refused by the blocklist while an
// attacher waits for it as a dependency: the waiting virtual vertex turns BAD at once, so the attacher
// fails now instead of at its solidification deadline; the relaying peer is not penalised; and a later
// lookup of the ID reports the refusal, so a dependency created afterwards is BAD at once too.
func TestBlocklistRefusalReleasesWaiters(t *testing.T) {
	genuine, _, txid := makeGenuineAndForgedCopy(t)
	tx, err := transaction.Parse(genuine)
	require.NoError(t, err)
	blocklist.FreezeOutputForTesting(tx.MustInputAt(0))

	waiting := vertex.WrapTxID(txid)
	env := &blocklistTestEnvironment{vertices: map[base.TransactionID]*vertex.WrappedTx{txid: waiting}}
	q := newForgedCopyTestQueue(&env.forgedCopyTestEnvironment)
	q.environment = env
	require.NoError(t, q.PolicyRefusedTx(txid))

	q.AddPulledTransaction(txid)
	q.fromPeer(peerInput(genuine, txid, "peer"))

	require.Equal(t, vertex.Bad, waiting.GetTxStatus())
	require.ErrorIs(t, waiting.GetError(), blocklist.ErrFrozen)
	require.Empty(t, env.persisted)
	require.Empty(t, env.invalidTx, "a policy refusal does not penalise the relaying peer")
	require.ErrorIs(t, q.PolicyRefusedTx(txid), blocklist.ErrFrozen)
}
