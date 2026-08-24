// Package chess_poc implements the chess covenant PoC sketched in
// chess_poc.md. Phase 1 builds two redeemed local scripts on the Proxima
// ledger:
//
//   - chessValidator (vendored from easyfl/chess): rule-pure half-move
//     validator. Its hash is pinned at chessGame compile time.
//   - chessGame (this package): on-chain protocol layer wrapping
//     chessValidator via callRedeemer. Branches: move / tie-accept /
//     resign / timeout-claim. Source in chess_game.easyfl.
//
// The chess() lock is a tiny one-shot dispatcher placed at every chess
// UTXO's lock element:
//
//     callRedeemer(<gHash>, <chess-entry idx>)
//
// All produced/consumed branching and selector decoding lives inside the
// chessGame script's single public `chess` function. That keeps the
// per-UTXO lock bytecode ≈ 36 bytes — UTXOs persist much longer than the
// tx that creates them, so the bytes saved per UTXO outweigh the slightly
// fatter chessGame bin (committed only once per tx via redeemScript).
//
// Privacy surface relies on easyfl's underscore-private convention
// (claude/archive/shipped/local_script.md §4) — every chessGame function except `chess`
// has a leading `_`, so callRedeemer cannot reach internal helpers or
// branch handlers directly.
package chess_poc

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/chess"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"golang.org/x/crypto/blake2b"
)

//go:embed chess_game.easyfl
var chessGameSourceTemplate string

// Bins holds the compiled binaries and content hashes for the chess covenant.
// All fields are populated lazily by initBins() on first access.
type Bins struct {
	ValidatorBin  easyfl.LocalScriptBin
	ValidatorHash [32]byte
	GameBin       easyfl.LocalScriptBin
	GameHash      [32]byte
	LockBytecode  []byte // chess() lock — see package doc.

	// chessValidator fn indices (consumed via callRedeemer from chessGame).
	playerMoveIdx int
	boardOKIdx    int
	sideToMoveIdx int

	// chessGame's single public entry point. All branching and selector
	// decoding happens inside this function (see chess_game.easyfl).
	chessEntryIdx int
}

var (
	binsOnce sync.Once
	binsVal  *Bins
	binsErr  error
)

// GetBins returns the singleton Bins for the chess covenant against the
// latest library. Panics on compile error (covenant authors hit this at
// init time, not at tx-validation time).
func GetBins() *Bins {
	binsOnce.Do(func() { binsVal, binsErr = buildBins() })
	if binsErr != nil {
		panic(binsErr)
	}
	return binsVal
}

func buildBins() (*Bins, error) {
	lib := ledger.L(base.MaxSlot)

	// 1) compile chessValidator (vendored source from easyfl/chess)
	vBin, vIdx, err := lib.CompileLocalScriptWithIndex(chess.ScriptSource)
	if err != nil {
		return nil, fmt.Errorf("chess_poc: compile chessValidator: %w", err)
	}
	vHash := blake2b.Sum256(vBin)

	playerMove, ok := vIdx["playerMove"]
	if !ok {
		return nil, fmt.Errorf("chess_poc: chessValidator has no playerMove")
	}
	boardOK, ok := vIdx["boardOK"]
	if !ok {
		return nil, fmt.Errorf("chess_poc: chessValidator has no boardOK")
	}
	sideToMove, ok := vIdx["sideToMove"]
	if !ok {
		return nil, fmt.Errorf("chess_poc: chessValidator has no sideToMove")
	}
	if playerMove > 0xff || boardOK > 0xff || sideToMove > 0xff {
		return nil, fmt.Errorf("chess_poc: chessValidator fnIdx > 255 (playerMove=%d boardOK=%d sideToMove=%d)",
			playerMove, boardOK, sideToMove)
	}

	// 2) sigLock bytecode (used by termination branches that emit payouts)
	_, _, sigLockBC, err := lib.CompileExpression("sigLock")
	if err != nil {
		return nil, fmt.Errorf("chess_poc: compile sigLock: %w", err)
	}

	// 3) compile chessGame with placeholders substituted
	gameSource := strings.NewReplacer(
		"{{VHASH}}", hex.EncodeToString(vHash[:]),
		"{{V_PLAYERMOVE}}", fmt.Sprintf("%02x", playerMove),
		"{{V_BOARDOK}}", fmt.Sprintf("%02x", boardOK),
		"{{V_SIDETOMOVE}}", fmt.Sprintf("%02x", sideToMove),
		"{{SIGLOCK_BC}}", hex.EncodeToString(sigLockBC),
	).Replace(chessGameSourceTemplate)

	gBin, gIdx, err := lib.CompileLocalScriptWithIndex(gameSource)
	if err != nil {
		return nil, fmt.Errorf("chess_poc: compile chessGame: %w\nsource:\n%s", err, gameSource)
	}
	gHash := blake2b.Sum256(gBin)

	// 4) Look up chessGame's single public entry point.
	chessEntry, ok := gIdx["chess"]
	if !ok {
		return nil, fmt.Errorf("chess_poc: chessGame has no public `chess` entry")
	}
	if chessEntry > 0xff {
		return nil, fmt.Errorf("chess_poc: chessGame `chess` fnIdx > 255: %d", chessEntry)
	}

	// 5) chess() lock bytecode: a one-shot redeemer dispatch.
	lockSrc := fmt.Sprintf("callRedeemer(0x%s, 0x%02x)",
		hex.EncodeToString(gHash[:]), chessEntry)
	_, _, lockBC, err := lib.CompileExpression(lockSrc)
	if err != nil {
		return nil, fmt.Errorf("chess_poc: compile chess() lock: %w", err)
	}

	return &Bins{
		ValidatorBin:  vBin,
		ValidatorHash: vHash,
		GameBin:       gBin,
		GameHash:      gHash,
		LockBytecode:  lockBC,
		playerMoveIdx: playerMove,
		boardOKIdx:    boardOK,
		sideToMoveIdx: sideToMove,
		chessEntryIdx: chessEntry,
	}, nil
}

// SourceForDebug returns the substituted chessGame source. Useful when the
// compile error is opaque and you need to grep the expanded text.
func SourceForDebug() string {
	lib := ledger.L(base.MaxSlot)
	vBin, vIdx, err := lib.CompileLocalScriptWithIndex(chess.ScriptSource)
	if err != nil {
		return fmt.Sprintf("// compile chessValidator failed: %v", err)
	}
	vHash := blake2b.Sum256(vBin)
	_, _, sigLockBC, _ := lib.CompileExpression("sigLock")
	return strings.NewReplacer(
		"{{VHASH}}", hex.EncodeToString(vHash[:]),
		"{{V_PLAYERMOVE}}", fmt.Sprintf("%02x", vIdx["playerMove"]),
		"{{V_BOARDOK}}", fmt.Sprintf("%02x", vIdx["boardOK"]),
		"{{V_SIDETOMOVE}}", fmt.Sprintf("%02x", vIdx["sideToMove"]),
		"{{SIGLOCK_BC}}", hex.EncodeToString(sigLockBC),
	).Replace(chessGameSourceTemplate)
}

// Equal compares two byte slices structurally. Convenience for tests.
func Equal(a, b []byte) bool { return bytes.Equal(a, b) }
