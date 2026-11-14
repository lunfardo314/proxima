package attacher

import (
	"testing"

	"github.com/lunfardo314/proxima/core/core_modules/branches"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// The floor handed down by the attacher that needs a milestone may serve as the milestone's baseline
// even when the floor is a branch of a later slot, as long as the floor does not contain the milestone:
// validating a transaction against a branch that already holds it would find its own inputs spent.
// A restarted node otherwise validated each such dependency against its own, older baseline and walked
// back slot by slot through history the floor already commits (kb/restart_rebuild.md).
func TestFloorCanBeBaselineOf(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStore)
	_, genesisKey := ledger.GetTestingLedgerParams(31415926535)
	distrib, _, _ := inittest.GenesisParamsWithPreDistribution(1_000_000_000)
	txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, genesisKey, distrib)
	require.NoError(t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	floor := tx.ID() // the distribution branch: committed, of a later slot than genesis
	require.True(t, floor.IsBranchTransaction())
	require.Greater(t, floor.Slot(), uint32(0))

	a := &attacher{Environment: &knownBadEnv{
		branches: branches.New(&knownBadBranchesEnv{Global: global.NewDefault(), store: stateStore}),
	}}

	// the floor itself, and anything of its slot or later: an ordinary baseline
	require.True(t, a.floorCanBeBaselineOf(floor, floor))
	require.True(t, a.floorCanBeBaselineOf(floor, base.RandomTransactionID(true, 0, base.T(floor.Slot()+1, 10))))

	// a milestone of an earlier slot the floor does not contain: the floor may serve
	notContained := base.RandomTransactionID(true, 0, base.T(floor.Slot()-1, 10))
	require.False(t, a.Branches().BranchKnowsTransaction(floor, notContained))
	require.True(t, a.floorCanBeBaselineOf(floor, notContained))

	// a transaction of an earlier slot the floor contains: refused, its own inputs are spent in the floor
	genesis := base.GenesisTransactionID()
	require.True(t, a.Branches().BranchKnowsTransaction(floor, genesis))
	require.False(t, a.floorCanBeBaselineOf(floor, genesis))
}
