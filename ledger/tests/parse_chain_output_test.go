// Focused tests for OutputDataWithID.ParseAsChainOutput. Verifies the
// post-fix contract: for an origin output whose serialised ChainID is
// NilChainID, the resolved ChainID equals blake2b(outputID).

package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// TestParseAsChainOutput_OriginResolvesChainID exercises the fix for the
// previously dropped chainID assignment: on origin outputs the returned
// OutputWithChainID.ChainID must equal blake2b(outputID), not NilChainID.
func TestParseAsChainOutput_OriginResolvesChainID(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)

	// Re-fetch via the state index and parse as a chain output.
	oData, err := e.u.StateReader().GetUTXOForChainID(chainOut.ChainID)
	require.NoError(t, err)
	parsed, err := oData.ParseAsChainOutput()
	require.NoError(t, err)

	// IsOrigin() is unreliable post-resolution because resolving the
	// chainID overwrites the embedded ChainConstraint.ChainID field
	// (same behaviour as the sister helper AsOutputWithChainID). The
	// authoritative "is origin" signal on a parsed output is the
	// predecessor input index sentinel.
	require.EqualValues(t, byte(0xff), parsed.ChainConstraint.PredecessorInputIndex,
		"origin output must report predecessor input index 0xff")

	expected := blake2b.Sum256(oData.ID[:])
	require.EqualValues(t, expected, parsed.ChainID,
		"origin ChainID must resolve to blake2b(outputID)")
	require.NotEqual(t, base.NilChainID, parsed.ChainID,
		"origin ChainID must not be NilChainID after Parse")
}

// TestParseAsChainOutput_PostOriginPassesChainID checks the second branch:
// once a chain has transitioned past origin, the embedded ChainID round-
// trips through ParseAsChainOutput unchanged.
func TestParseAsChainOutput_PostOriginPassesChainID(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)

	// Drive one transition (chainOut → successor).
	txBytes, _ := e.buildChainTransition(t, &ledger.OutputWithID{ID: chainOut.ID, Output: chainOut.Output}, chainOut, nil)
	require.NoError(t, e.u.AddTransaction(txBytes))

	oData, err := e.u.StateReader().GetUTXOForChainID(chainOut.ChainID)
	require.NoError(t, err)
	parsed, err := oData.ParseAsChainOutput()
	require.NoError(t, err)

	require.NotEqual(t, byte(0xff), parsed.ChainConstraint.PredecessorInputIndex,
		"past-origin output must have a real predecessor input index")
	require.EqualValues(t, chainOut.ChainID, parsed.ChainID,
		"post-origin ChainID must round-trip unchanged")
}
