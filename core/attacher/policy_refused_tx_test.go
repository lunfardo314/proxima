package attacher

import (
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/blocklist"
	"github.com/stretchr/testify/require"
)

// policyRefusedEnv implements only what AttachTxID calls on a non-branch transaction; the embedded
// nil interface panics on anything else.
type policyRefusedEnv struct {
	Environment
	vertices map[base.TransactionID]*vertex.WrappedTx
	refused  map[base.TransactionID]error
}

func (e *policyRefusedEnv) WithGlobalWriteLock(fun func())                { fun() }
func (e *policyRefusedEnv) AddVertexNoLock(vid *vertex.WrappedTx)         { e.vertices[vid.ID()] = vid }
func (e *policyRefusedEnv) PolicyRefusedTx(txid base.TransactionID) error { return e.refused[txid] }
func (e *policyRefusedEnv) GetVertexNoLock(txid base.TransactionID) *vertex.WrappedTx {
	return e.vertices[txid]
}

// A transaction ID the input gate refused by the blocklist resolves to BAD when a milestone first
// references it, instead of a virtual vertex its attacher pulls until the solidification deadline
// while holding attacher capacity. A refusal by the blocklist condemns the ID, so this holds for
// every copy. An ID never refused stays a virtual vertex to pull, as before.
func TestPolicyRefusedTxResolvesAtOnce(t *testing.T) {
	env := &policyRefusedEnv{
		vertices: make(map[base.TransactionID]*vertex.WrappedTx),
		refused:  make(map[base.TransactionID]error),
	}
	unknown := base.RandomTransactionID(false, 1, base.T(1001, 0))
	refused := base.RandomTransactionID(false, 1, base.T(1002, 0))
	env.refused[refused] = blocklist.ErrFrozen

	vid := AttachTxID(unknown, env)
	require.Equal(t, vertex.Undefined, vid.GetTxStatus())
	require.False(t, vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted))

	vid = AttachTxID(refused, env)
	require.Equal(t, vertex.Bad, vid.GetTxStatus())
	require.ErrorIs(t, vid.GetError(), blocklist.ErrFrozen)
	require.True(t, vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted|vertex.FlagVertexTxAttachmentFinished))
}
