package attacher

import (
	"errors"
	"testing"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

type knownBadBranchesEnv struct {
	*global.Global
	store global.Store
}

func (e *knownBadBranchesEnv) StateStore() global.Store     { return e.store }
func (e *knownBadBranchesEnv) NotifyBranchCommitted(uint32) {}
func (e *knownBadBranchesEnv) RequestPrune()                {}

// knownBadEnv implements only what AttachTxID calls on a branch that is not in the state; the
// embedded nil interface panics on anything else.
type knownBadEnv struct {
	Environment
	branches *branches.Branches
	vertices map[base.TransactionID]*vertex.WrappedTx
	bad      map[base.TransactionID]error
}

func (e *knownBadEnv) WithGlobalWriteLock(fun func())                     { fun() }
func (e *knownBadEnv) Branches() *branches.Branches                       { return e.branches }
func (e *knownBadEnv) AddVertexNoLock(vid *vertex.WrappedTx)              { e.vertices[vid.ID()] = vid }
func (e *knownBadEnv) KnownBadBranchNoLock(txid base.TransactionID) error { return e.bad[txid] }
func (e *knownBadEnv) GetVertexNoLock(txid base.TransactionID) *vertex.WrappedTx {
	return e.vertices[txid]
}

// A branch rejected by a stem-value mismatch and then pruned from the memDAG resolves to BAD at once
// when a milestone references it again, instead of a virtual vertex to pull and validate again. On the
// network after library upgrade 6, diverged nodes kept building on their rejected branch, and every node
// revalidated it and reloaded the history under it about every 13 slots. The vertex is marked attached,
// so a gossiped copy of the branch starts no attacher either.
func TestKnownBadBranchResolvesAtOnce(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStore)
	env := &knownBadEnv{
		branches: branches.New(&knownBadBranchesEnv{Global: global.NewDefault(), store: stateStore}),
		vertices: make(map[base.TransactionID]*vertex.WrappedTx),
		bad:      make(map[base.TransactionID]error),
	}
	unknown := base.RandomTransactionID(true, 2, base.T(1001, 0))
	rejected := base.RandomTransactionID(true, 2, base.T(1002, 0))
	rejection := errors.New("branch rejected: stem-value mismatch [BaselineRoot]")
	env.bad[rejected] = rejection

	// a branch never rejected becomes a virtual vertex to pull, as before
	vid := AttachTxID(unknown, env)
	require.Equal(t, vertex.Undefined, vid.GetTxStatus())
	require.False(t, vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted))

	// a remembered one is BAD with its original reason, and counts as attached
	vid = AttachTxID(rejected, env)
	require.Equal(t, vertex.Bad, vid.GetTxStatus())
	require.Equal(t, rejection, vid.GetError())
	require.True(t, vid.FlagsUp(vertex.FlagVertexTxAttachmentStarted|vertex.FlagVertexTxAttachmentFinished))
}
