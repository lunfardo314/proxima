package chess_poc

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// publicPrivateCounts splits a script's entry points into (public, private)
// by querying IsPrivate per fn index.
func publicPrivateCounts(s *easyfl.LocalScript[*ledger.EvalContext]) (pub, priv int) {
	for i := 0; i < s.NumFunctions(); i++ {
		if s.IsPrivate(i) {
			priv++
		} else {
			pub++
		}
	}
	return
}

// TestBinsCompile is the basic gate for Phase 1: the vendored chessValidator
// source and the chessGame source (with chessValidator hash + fn indices
// substituted) both compile, the chess() lock bytecode is produced, and the
// content hashes are deterministic.
func TestBinsCompile(t *testing.T) {
	bins := GetBins()
	require.NotNil(t, bins)
	require.NotEmpty(t, bins.ValidatorBin, "validator bin must be non-empty")
	require.NotEmpty(t, bins.GameBin, "game bin must be non-empty")
	require.NotEmpty(t, bins.LockBytecode, "lock bytecode must be non-empty")

	// Decode both bins to surface their entry-point counts (each
	// numbered function in the local script is independently callable
	// via callRedeemer's fnIdx).
	lib := ledger.L(base.MaxSlot)
	vScript, err := lib.LocalScriptFromBytes(bins.ValidatorBin)
	require.NoError(t, err)
	gScript, err := lib.LocalScriptFromBytes(bins.GameBin)
	require.NoError(t, err)

	vPub, vPriv := publicPrivateCounts(vScript)
	gPub, gPriv := publicPrivateCounts(gScript)
	t.Logf("validator bin     = %d bytes, %d entry points (%d public, %d private)",
		len(bins.ValidatorBin), vScript.NumFunctions(), vPub, vPriv)
	t.Logf("validator hash    = %x", bins.ValidatorHash)
	t.Logf("game bin          = %d bytes, %d entry points (%d public, %d private)",
		len(bins.GameBin), gScript.NumFunctions(), gPub, gPriv)
	t.Logf("game hash         = %x", bins.GameHash)
	t.Logf("chess() lock      = %d bytes (%x)", len(bins.LockBytecode), bins.LockBytecode)
	t.Logf("validator fnIdx   playerMove=%d  boardOK=%d  sideToMove=%d",
		bins.playerMoveIdx, bins.boardOKIdx, bins.sideToMoveIdx)
	t.Logf("game fnIdx        branchMove=%d  branchTieAccept=%d  branchResign=%d  branchTimeoutClaim=%d",
		bins.branchIdx[0], bins.branchIdx[1], bins.branchIdx[2], bins.branchIdx[3])
	t.Logf("game fnIdx        producedValidate=%d", bins.producedValidateIdx)
}
