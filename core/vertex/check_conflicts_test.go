package vertex

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/set256"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

type checkConflictsBranchesEnv struct {
	*global.Global
	store global.Store
}

func (e *checkConflictsBranchesEnv) StateStore() global.Store     { return e.store }
func (e *checkConflictsBranchesEnv) NotifyBranchCommitted(uint32) {}
func (e *checkConflictsBranchesEnv) RequestPrune()                {}

// spentOutputReader is a baseline state in which one output is already spent; nothing else is read.
type spentOutputReader struct {
	multistate.StateReader
	spent base.OutputID
}

func (r spentOutputReader) HasUTXO(id base.OutputID) bool { return id != r.spent }

// The safety net in _checkVertex raises a consumer flagged not-in-the-state (a stale flag) to in-the-state
// when the baseline has committed it. That changes the verdict on the consumer as a producer: here its
// output is already spent in the baseline state and another vertex of the cone spends it again, a double
// spend. A single pass over the cone finds it only when it checks the consumer after raising it, so with
// the random map order it missed it about every other time; CheckConflicts must find it every time.
func TestCheckConflictsRechecksRaisedConsumer(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStore)
	_, genesis := multistate.FetchEarliestBranchIDList(stateStore)
	require.NotEmpty(t, genesis)
	br := branches.New(&checkConflictsBranchesEnv{Global: global.NewDefault(), store: stateStore})

	baselineID := base.RandomTransactionID(true, 2, base.T(1010, 0))
	producer := WrapTxID(base.RandomTransactionID(false, 2, base.T(1004, 10)))
	raised := WrapTxID(base.RandomTransactionID(false, 2, base.T(1005, 10)))
	spender := WrapTxID(base.RandomTransactionID(false, 2, base.T(1006, 10)))
	producer.consumed = map[byte]set.Set[*WrappedTx]{0: set.New(raised)}
	raised.consumed = map[byte]set.Set[*WrappedTx]{0: set.New(spender)}

	// the baseline commits raised, not spender
	mut := multistate.NewMutations()
	mut.InsertAddTxMutation(raised.ID(), set256.Set256{})
	// only its mutations matter: a stem without a stem lock keeps the branch out of the coverage lookups
	stem := &ledger.OutputWithID{
		ID:     base.MustNewOutputID(baselineID, 0),
		Output: ledger.OutputBasic(0, ledger.SigLockFromED25519PrivateKey(pastConeTestGenesisKey)),
	}
	br.AddPendingBranch(baselineID, &branches.PendingBranchCommit{
		Mutations:        mut,
		RootRecParams:    &multistate.RootRecordParams{},
		PreviousBranchID: genesis[0],
	}, stem, nil)

	reader := spentOutputReader{spent: raised.OutputID(0)}
	for i := 0; i < 40; i++ {
		pc := &PastCone{PastConeBase: NewPastConeBase(&baselineID)}
		pc.SetBranches(br)
		pc.vertices[producer] = FlagPastConeVertexKnown | FlagPastConeVertexDefined
		pc.vertices[raised] = FlagPastConeVertexKnown | FlagPastConeVertexDefined | FlagPastConeVertexCheckedInTheState // stale: not in the state
		pc.vertices[spender] = FlagPastConeVertexKnown | FlagPastConeVertexDefined | FlagPastConeVertexCheckedInTheState

		conflict, err := pc.CheckConflicts(context.Background(), func(base.TransactionID) multistate.StateReader { return reader })
		require.NoError(t, err)
		require.True(t, pc.IsInTheState(raised), "the safety net raises the committed consumer")
		require.NotNil(t, conflict, "run %d: the double spend of the raised vertex's output is missed", i)
		require.Equal(t, raised, conflict.VID)
	}
}
