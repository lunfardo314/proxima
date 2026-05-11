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
// The chess() lock is a small redeemer dispatcher built at init time and
// placed at the lock element index of every chess UTXO. It branches on
// selfIsProducedOutput:
//
//   - produced: callRedeemer(<gHash>, <producedValidate idx>) — runs the
//     chessGame produced-side handler (origin check / non-origin pass).
//   - consumed: looks up the branch handler's fnIdx in a 4-byte literal
//     table baked into the lock, indexed by the unlock byte selector
//     (0x00 move, 0x01 tie-accept, 0x02 resign, 0x03 timeout-claim), and
//     calls callRedeemer with that fnIdx — direct dispatch, no
//     selectCaseByIndex wrapper inside chessGame.
//
// Public dispatch surface relies on easyfl's underscore-private convention
// (claude/local_script.md §4) — internal helpers in chess_game.easyfl all
// start with `_` and so are unreachable via callRedeemer.
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

	// chessValidator fn indices.
	playerMoveIdx int
	boardOKIdx    int
	sideToMoveIdx int

	// chessGame public fn indices. branchIdx[i] = idx for selector byte i.
	branchIdx           [4]int
	producedValidateIdx int
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

	// 4) Look up chessGame's five public entry points. The four branch
	//    handlers are indexed by unlock selector byte; producedValidate is
	//    the produced-side entry. All five must fit in 1 byte (easyfl caps
	//    fnIdx at 255 by construction).
	branchNames := [4]string{"branchMove", "branchTieAccept", "branchResign", "branchTimeoutClaim"}
	var branchIdx [4]int
	for i, name := range branchNames {
		idx, ok := gIdx[name]
		if !ok {
			return nil, fmt.Errorf("chess_poc: chessGame has no %s entry", name)
		}
		if idx > 0xff {
			return nil, fmt.Errorf("chess_poc: chessGame %s fnIdx > 255: %d", name, idx)
		}
		branchIdx[i] = idx
	}
	producedValidate, ok := gIdx["producedValidate"]
	if !ok {
		return nil, fmt.Errorf("chess_poc: chessGame has no producedValidate entry")
	}
	if producedValidate > 0xff {
		return nil, fmt.Errorf("chess_poc: chessGame producedValidate fnIdx > 255: %d", producedValidate)
	}

	// 5) chess() lock bytecode. The lookup table baked into the lock maps
	//    unlock byte 0 (the branch selector) → branch handler fnIdx. The
	//    runtime computes byte(<table>, selector) to pick the dispatch.
	table := []byte{byte(branchIdx[0]), byte(branchIdx[1]), byte(branchIdx[2]), byte(branchIdx[3])}
	lockSrc := fmt.Sprintf(`if(
		selfIsProducedOutput,
		callRedeemer(0x%s, 0x%02x),
		and(
			require(equal(len(selfUnlockParameters), u64/1), !!!chess_unlock_must_be_1_byte),
			require(lessThan(byte(selfUnlockParameters,0), 0x04), !!!chess_invalid_branch_selector),
			callRedeemer(0x%s, byte(0x%s, byte(selfUnlockParameters,0)))
		)
	)`, hex.EncodeToString(gHash[:]), producedValidate,
		hex.EncodeToString(gHash[:]), hex.EncodeToString(table))
	_, _, lockBC, err := lib.CompileExpression(lockSrc)
	if err != nil {
		return nil, fmt.Errorf("chess_poc: compile chess() lock: %w", err)
	}

	return &Bins{
		ValidatorBin:        vBin,
		ValidatorHash:       vHash,
		GameBin:             gBin,
		GameHash:            gHash,
		LockBytecode:        lockBC,
		playerMoveIdx:       playerMove,
		boardOKIdx:          boardOK,
		sideToMoveIdx:       sideToMove,
		branchIdx:           branchIdx,
		producedValidateIdx: producedValidate,
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
