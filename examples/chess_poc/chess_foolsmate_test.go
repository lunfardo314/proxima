// Fool's Mate end-to-end UTXODB test for chess_poc.md §8 Phase 1
// "mate-by-deadline e2e".
//
// Fool's Mate is the shortest possible chess game ending in checkmate:
//
//     1. f3   e5
//     2. g4   Qh4#
//
// After 2...Qh4 the black queen attacks the white king on e1 along the
// h4-e1 diagonal (g3 and f2 are empty, because white pushed those pawns
// in moves 1 and 3). White has no legal reply:
//   - no king square is unattacked AND vacated by own pieces;
//   - nothing blocks the diagonal (no piece can reach f2 or g3 in one move);
//   - nothing captures the queen (no white piece reaches h4 in one move).
//
// chessValidator's playerMove enforces "not(isCheck(kingColor, result))",
// so any white move-spec sent next would fail validation — there is no
// result board where white's king is safe. White therefore cannot submit
// a `move` branch tx; the deadline lapses; and black claims the bounty
// via the `timeout-claim` branch (chess_poc.md §4.4, post-acceptance
// flavour). Both checkmate and stalemate collapse onto this same rule
// (chess_poc.md §1 / §9.3) — the validator never enumerates moves.
//
// The test drives the full sequence and asserts that:
//   - All four half-moves validate.
//   - After the deadline, black's timeout-claim is accepted.
//   - Black receives the full bounty in a sigLock output.
package chess_poc

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Black queen piece-byte (chessValidator encoding: color<<4 | type).
const pBQ = 0x25

// applyMove is a generic board-update helper covering both pawn pushes
// (1- and 2-square) and any other piece-to-empty-square move that
// doesn't change castling rights or king-pos bytes. The EP target byte
// (board[67]) is set to the midpoint only for 2-square pawn pushes; any
// other move clears it to 0xff. The side-to-move byte (board[68])
// flips on every move.
//
// This is enough for Fool's Mate: f3 (1-square pawn push), e5 / g4
// (2-square pawn pushes), Qd8-h4 (diagonal queen move). None of those
// moves touch king-home or rook-home squares, so castling rights stay
// at 0x0f throughout; and neither king moves so the king-pos bytes
// stay at e1 / e8.
func applyMove(start, spec []byte) []byte {
	from, to, piece := int(spec[0]), int(spec[1]), spec[2]
	out := make([]byte, 69)
	copy(out, start)
	out[from] = pEMPTY
	out[to] = piece

	// 2-square pawn push? Set EP target to the passed-over square.
	isPawnPush2 := (piece == pWP || piece == pBP) && (to-from == 16 || from-to == 16)
	if isPawnPush2 {
		out[67] = byte((from + to) / 2)
	} else {
		out[67] = 0xff
	}
	if start[68] == SideWhite {
		out[68] = SideBlack
	} else {
		out[68] = SideWhite
	}
	return out
}

// moveSpec returns the 5-byte chessValidator move spec for a simple
// non-special move (no capture, castle, EP, or promotion).
func moveSpec(from, to int, piece byte) []byte {
	return []byte{byte(from), byte(to), piece, 0x00, 0x00}
}

// =============================================================================
// TestFoolsMate: 1.f3 e5 2.g4 Qh4# — black wins by deadline.
// =============================================================================

func TestFoolsMate(t *testing.T) {
	e := newChessEnv(t)

	// chainID is set right after the origin tx settles; the traceState
	// closure reads it then.
	var chainID base.ChainID

	// traceState prints the parsed chess covenant UTXO under a
	// "after <label>" banner. Called after each half-move so the test
	// log reads as a play-by-play.
	traceState := func(label string) {
		t.Helper()
		owc := loadChainOutput(t, e.u, chainID)
		gs, err := ParseAsChessGameOutput(&owc.OutputWithID)
		require.NoError(t, err, "parse chess UTXO after %s", label)
		t.Logf("after %s:\n%s", label, gs.Lines("    ").String())
	}

	// Square indices (rank*8 + file). a1=0, h1=7, a8=56, h8=63.
	const (
		sqE1 = 4
		sqE8 = 60
		sqE2 = 12
		sqE4 = 28
		sqE5 = 36
		sqE7 = 52
		sqF2 = 13
		sqF3 = 21
		sqG2 = 14
		sqG4 = 30
		sqH4 = 31
		sqD8 = 59
	)

	// We pick a short T_slots so we can fast-forward past the deadline
	// without inflating the test runtime.
	const tslots = uint32(4)

	// ----- Move 1: white plays f2-f3 -----
	move1Spec := moveSpec(sqF2, sqF3, pWP)
	board1 := applyMove(canonStart(), move1Spec)

	wOuts := e.outputsOf(t, e.whiteLock)
	require.NotEmpty(t, wOuts)
	ts1 := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if ts1.IsSlotBoundary() {
		ts1 = ts1.AddTicks(1)
	}
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        tslots,
		FirstMoveSpec: move1Spec,
		BoardAfter:    board1,
		TxTimestamp:   ts1,
	})
	require.NoError(t, err)
	originTx, err := e.submit(originTxb.Bytes())
	require.NoError(t, err, "move 1 (white f3) must validate")
	chainID = base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))
	traceState("1. f3 (origin)")

	// ----- Move 2: black accepts with e7-e5 -----
	move2Spec := moveSpec(sqE7, sqE5, pBP)
	board2 := applyMove(board1, move2Spec)

	prev := loadChainOutput(t, e.u, chainID)
	bOuts := e.outputsOf(t, e.blackLock)
	require.NotEmpty(t, bOuts)
	ts2 := e.nextTxTs(t, prev.ID.Timestamp())
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	acceptTxb, err := BuildAcceptance(BuildAcceptanceParams{
		BlackPrivKey:  e.blackPriv,
		BlackSigLock:  e.blackLock,
		OriginUTXO:    prev,
		FundingInputs: bOuts,
		NewAmount:     400_000_000,
		FirstMoveSpec: move2Spec,
		BoardAfter:    board2,
		TxTimestamp:   ts2,
	})
	require.NoError(t, err)
	_, err = e.submit(acceptTxb.Bytes())
	require.NoError(t, err, "move 2 (black e5) must validate")
	traceState("1... e5 (acceptance)")

	// ----- Move 3: white plays g2-g4 -----
	move3Spec := moveSpec(sqG2, sqG4, pWP)
	board3 := applyMove(board2, move3Spec)

	prev = loadChainOutput(t, e.u, chainID)
	wOuts = e.outputsOf(t, e.whiteLock)
	ts3 := e.nextTxTs(t, prev.ID.Timestamp())
	if ts3.IsSlotBoundary() {
		ts3 = ts3.AddTicks(1)
	}
	move3Txb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.whitePriv,
		MoverSigLock:  e.whiteLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(),
		FundingInputs: wOuts,
		MoveSpec:      move3Spec,
		BoardAfter:    board3,
		TxTimestamp:   ts3,
	})
	require.NoError(t, err)
	_, err = e.submit(move3Txb.Bytes())
	require.NoError(t, err, "move 3 (white g4) must validate")
	traceState("2. g4")

	// ----- Move 4: black plays Qd8-h4# -----
	// Diagonal d8→h4 via e7, f6, g5 — all empty (black just emptied e7
	// in move 2; ranks 5-6 untouched).
	move4Spec := moveSpec(sqD8, sqH4, pBQ)
	board4 := applyMove(board3, move4Spec)

	prev = loadChainOutput(t, e.u, chainID)
	bOuts = e.outputsOf(t, e.blackLock)
	ts4 := e.nextTxTs(t, prev.ID.Timestamp())
	if ts4.IsSlotBoundary() {
		ts4 = ts4.AddTicks(1)
	}
	move4Txb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.blackPriv,
		MoverSigLock:  e.blackLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(),
		FundingInputs: bOuts,
		MoveSpec:      move4Spec,
		BoardAfter:    board4,
		TxTimestamp:   ts4,
	})
	require.NoError(t, err)
	_, err = e.submit(move4Txb.Bytes())
	require.NoError(t, err, "move 4 (black Qh4#) must validate — black's queen takes the h4 diagonal")
	traceState("2... Qh4#  (white in check, no legal reply)")

	// At this point side-to-move is WHITE and the white king on e1 is
	// in check from the black queen on h4 along the h4-e1 diagonal.
	// White has no legal reply — chessValidator's playerMove(WHITE,
	// board4, anySpec, anyResult) cannot be satisfied because no result
	// board lifts the check (king has no vacated unattacked square,
	// nothing blocks at f2 or g3 in one move, nothing captures h4 in
	// one move).
	//
	// White does nothing; the deadline lapses; black claims the chain.
	mated := loadChainOutput(t, e.u, chainID)
	matedState, err := readChessStateFromOutput(mated.Output)
	require.NoError(t, err)
	require.Equal(t, SideWhite, matedState.SideToMove(),
		"after black's mating move side-to-move is white")

	// Pick a tx timestamp strictly past the deadline (tick > 0 to dodge
	// the slot boundary rule).
	claimTs := base.T(matedState.Deadline.Slot+1, 1)

	claimTxb, err := BuildTimeoutClaim(BuildTimeoutClaimParams{
		ClaimantPrivKey: e.blackPriv,  // post-acceptance: opposite of side-to-move
		ClaimantLock:    e.blackLock,
		PrevUTXO:        mated,
		TxTimestamp:     claimTs,
	})
	require.NoError(t, err)
	_, err = e.submit(claimTxb.Bytes())
	require.NoError(t, err, "black's timeout-claim after mating move must validate")

	// Black should now hold a sigLock output with the full bounty.
	bountyFound := false
	for _, o := range e.outputsOf(t, e.blackLock) {
		if o.Output.TokenBalance() == 400_000_000 {
			bountyFound = true
			break
		}
	}
	require.True(t, bountyFound,
		"black must receive the full 400_000_000 bounty after Fool's Mate timeout")

	// Sanity: chain should be terminated — the chess UTXO is no longer
	// in state under chainID.
	_, err = e.u.StateReader().GetUTXOForChainID(chainID)
	require.Error(t, err, "chess chain must be terminated after timeout-claim")
}
