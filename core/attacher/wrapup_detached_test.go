package attacher

import (
	"errors"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// TestCoverageDeltaCheckOnForceDetachedVertex: the memDAG size backstop can force-detach a milestone while its
// attacher is in wrap-up. The coverageDelta cross-check then cannot read the declared value from its own vertex.
// That is no verdict on the milestone: it must end as a clean abort (errDetachedInAttacher), never as a rejection,
// which would mark it Bad for good, spread to every milestone built on it and stop the node at its own next
// milestone ("BAD vertex in the past cone"). A full vertex without a readable sequencer constraint is still rejected.
func TestCoverageDeltaCheckOnForceDetachedVertex(t *testing.T) {
	require.True(t, ledger.L(0).EnforceCoverageDeltaMonotonicity, "the cross-check must be on for this test")

	newVid := func(seed int) *vertex.WrappedTx {
		_, genesisPrivateKey := ledger.GetTestingLedgerParams(31415926535)
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey, _, addr := u.GenerateAddress(seed)
		require.NoError(t, u.TokensFromFaucet(addr, 1_000_000_000))
		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		par, err := u.MakeTransferInputData(privKey, nil, outs[0].ID.Timestamp().AddSlots(1))
		require.NoError(t, err)
		_, _, addr2 := u.GenerateAddress(seed + 1)
		txBytes, err := utxodb.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)
		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)
		return vertex.NewVertex(tx).Wrap()
	}

	// own vertex force-detached during wrap-up: clean abort, no verdict
	detached := newVid(80)
	detached.ConvertToDetachedForced()
	err := (&milestoneAttacher{vid: detached}).enforceSeqCoverageDelta(1)
	require.Error(t, err)
	require.True(t, errors.Is(err, errDetachedInAttacher), "got: %v", err)

	// the branch commit reads its stem and sequencer outputs from the same vertex: a clean abort too, never an assert
	_, _, err = (&milestoneAttacher{vid: detached}).extractBranchOutputs(base.OutputID{}, base.ChainID{})
	require.Error(t, err)
	require.True(t, errors.Is(err, errDetachedInAttacher), "got: %v", err)

	// full vertex whose sequencer constraint cannot be read: still a rejection
	full := newVid(82)
	err = (&milestoneAttacher{vid: full}).enforceSeqCoverageDelta(1)
	require.Error(t, err)
	require.False(t, errors.Is(err, errDetachedInAttacher), "got: %v", err)
}
