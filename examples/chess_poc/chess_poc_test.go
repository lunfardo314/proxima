// UTXODB tests for the chess covenant PoC — Phase 1 (no networking, no CLI).
//
// Coverage maps to chess_poc.md §4 / §8 Phase 1 — per-branch happy paths,
// per-branch negatives, state-transition invariants, and a few end-to-end
// scenarios. Tests use the in-memory ledger/utxodb harness for determinism.
package chess_poc

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test environment + chess board helpers
// =============================================================================

// chessEnv is the shared scaffold for chess_poc tests.
type chessEnv struct {
	u           *utxodb.UTXODB
	whitePriv   ed25519.PrivateKey
	whiteLock   ledger.SigLock
	blackPriv   ed25519.PrivateKey
	blackLock   ledger.SigLock
	thirdPriv   ed25519.PrivateKey
	thirdLock   ledger.SigLock
}

// newChessEnv creates a utxodb with three funded addresses (white, black,
// third party). Wide initial funding so storage deposit / stake math is easy.
func newChessEnv(t *testing.T) *chessEnv {
	t.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	wPriv, _, wLock := u.GenerateAddress(1)
	bPriv, _, bLock := u.GenerateAddress(2)
	tPriv, _, tLock := u.GenerateAddress(3)
	require.NoError(t, u.TokensFromFaucet(wLock, 1_000_000_000))
	require.NoError(t, u.TokensFromFaucet(bLock, 1_000_000_000))
	require.NoError(t, u.TokensFromFaucet(tLock, 1_000_000_000))
	return &chessEnv{
		u:         u,
		whitePriv: wPriv, whiteLock: wLock,
		blackPriv: bPriv, blackLock: bLock,
		thirdPriv: tPriv, thirdLock: tLock,
	}
}

// outputsOf returns sigLock-controlled outputs of the given address.
func (e *chessEnv) outputsOf(t *testing.T, lock ledger.SigLock) []*ledger.OutputWithID {
	t.Helper()
	outsData, err := e.u.StateReader().GetUTXOsForController(lock.ControllerID())
	require.NoError(t, err)
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	require.NoError(t, err)
	return outs
}

// loadChainOutput fetches a chain output by ID and parses it as a chain
// output (origin ChainID resolved via blake2b(outputID)).
func loadChainOutput(t *testing.T, u *utxodb.UTXODB, chainID base.ChainID) *ledger.OutputWithChainID {
	t.Helper()
	chs, err := u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)
	parsed, err := chs.ParseAsChainOutput()
	require.NoError(t, err)
	return parsed
}

// submit submits txBytes to the utxodb, returning the parsed *Transaction
// (so tests can introspect IsScriptRedeemed etc.) plus the validation error.
func (e *chessEnv) submit(txBytes []byte) (*transaction.Transaction, error) {
	var captured *transaction.Transaction
	err := e.u.AddTransaction(txBytes, func(tx *transaction.Transaction, e error) error {
		captured = tx
		return e
	})
	return captured, err
}

// nextTxTs picks a transaction timestamp that respects the transaction pace
// and the slot boundary. Builds on top of the latest output timestamp.
func (e *chessEnv) nextTxTs(t *testing.T, after base.LedgerTime) base.LedgerTime {
	t.Helper()
	lib := ledger.L(after.Slot)
	return after.AddTicks(int(lib.TransactionPace))
}

// =============================================================================
// Chess board helpers — minimal moves needed for tests
// =============================================================================

const (
	pEMPTY = 0x00
	pWP    = 0x11
	pBP    = 0x21
)

// canonStart returns a fresh copy of CanonicalStartBoard.
func canonStart() []byte {
	out := make([]byte, len(CanonicalStartBoard))
	copy(out, CanonicalStartBoard)
	return out
}

// applyPawnPush2 applies a 2-square pawn push to the board.
// from/to are square indices 0..63; piece is WP (white) or BP (black).
// Returns the new board (length 69) — caller passes a copy.
func applyPawnPush2(start []byte, from, to int, piece byte) []byte {
	out := make([]byte, 69)
	copy(out, start)
	out[from] = pEMPTY
	out[to] = piece
	// EP target = midpoint between from and to (the square the pawn passed over).
	out[67] = byte((from + to) / 2)
	// Side flips
	if start[68] == SideWhite {
		out[68] = SideBlack
	} else {
		out[68] = SideWhite
	}
	return out
}

// movePawnPush2Spec returns the 5-byte move spec for a 2-square pawn push.
func movePawnPush2Spec(from, to int, piece byte) []byte {
	return []byte{byte(from), byte(to), piece, 0x00, 0x00}
}

// =============================================================================
// TestOriginHappyPath: white opens a game with e2-e4. Tx is accepted,
// the chess UTXO appears in state under the chain ID with the correct shape.
// =============================================================================

func TestOriginHappyPath(t *testing.T) {
	e := newChessEnv(t)

	// White's first move: e2-e4 (sq 12 → 28).
	const sqE2, sqE4 = 12, 28
	spec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfter := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	// Pick a timestamp ≥ funding output ts + pace.
	fundingOuts := e.outputsOf(t, e.whiteLock)
	require.NotEmpty(t, fundingOuts)
	txTs := e.nextTxTs(t, fundingOuts[0].ID.Timestamp())
	if txTs.IsSlotBoundary() {
		txTs = txTs.AddTicks(1)
	}

	txb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: fundingOuts,
		Stake:         200_000_000,
		TSlots:        50,
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   txTs,
	})
	require.NoError(t, err)

	tx, err := e.submit(txb.Bytes())
	require.NoError(t, err, "origin tx must validate")
	require.NotNil(t, tx)
	require.True(t, tx.IsScriptRedeemed(GetBins().ValidatorHash))
	require.True(t, tx.IsScriptRedeemed(GetBins().GameHash))

	// The chess UTXO is the chain origin; find it via chain ID.
	chainID := base.MakeOriginChainID(base.MustNewOutputID(tx.ID(), 0))
	t.Logf("origin chain ID = %s", chainID.StringShort())

	parsed := loadChainOutput(t, e.u, chainID)
	parsedState, err := readChessStateFromOutput(parsed.Output)
	require.NoError(t, err)
	require.EqualValues(t, HolderIDOf(e.whitePriv), parsedState.WhiteHolder)
	require.Empty(t, parsedState.BlackHolder)
	require.EqualValues(t, uint32(50), parsedState.TSlots)
	require.Equal(t, byte(0), parsedState.Flags)
	require.Equal(t, SideBlack, parsedState.SideToMove())
}

// =============================================================================
// TestAcceptanceHappyPath: black accepts the origin with e7-e5; new chess
// UTXO has 32-byte black holder, doubled amount, board after black's move.
// =============================================================================

func TestAcceptanceHappyPath(t *testing.T) {
	e := newChessEnv(t)

	// --- Step 1: white opens with e2-e4 ---
	const sqE2, sqE4 = 12, 28
	whiteSpec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfterWhite := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs1 := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if txTs1.IsSlotBoundary() {
		txTs1 = txTs1.AddTicks(1)
	}
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        50,
		FirstMoveSpec: whiteSpec,
		BoardAfter:    boardAfterWhite,
		TxTimestamp:   txTs1,
	})
	require.NoError(t, err)
	originTx, err := e.submit(originTxb.Bytes())
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))

	// --- Step 2: black accepts with e7-e5 ---
	const sqE7, sqE5 = 52, 36
	blackSpec := movePawnPush2Spec(sqE7, sqE5, pBP)
	boardAfterBlack := applyPawnPush2(boardAfterWhite, sqE7, sqE5, pBP)

	originParsed := loadChainOutput(t, e.u, chainID)
	bOuts := e.outputsOf(t, e.blackLock)
	txTs2 := e.nextTxTs(t, originParsed.ID.Timestamp())
	if txTs2.IsSlotBoundary() {
		txTs2 = txTs2.AddTicks(1)
	}
	acceptTxb, err := BuildAcceptance(BuildAcceptanceParams{
		BlackPrivKey:  e.blackPriv,
		BlackSigLock:  e.blackLock,
		OriginUTXO:    originParsed,
		FundingInputs: bOuts,
		NewAmount:     400_000_000, // 2 × origin
		FirstMoveSpec: blackSpec,
		BoardAfter:    boardAfterBlack,
		TxTimestamp:   txTs2,
	})
	require.NoError(t, err)
	_, err = e.submit(acceptTxb.Bytes())
	require.NoError(t, err, "acceptance tx must validate")

	// Verify successor state.
	succParsed := loadChainOutput(t, e.u, chainID)
	succState, err := readChessStateFromOutput(succParsed.Output)
	require.NoError(t, err)
	require.EqualValues(t, HolderIDOf(e.whitePriv), succState.WhiteHolder)
	require.Len(t, succState.BlackHolder, 32)
	blackID := HolderIDOf(e.blackPriv)
	require.True(t, Equal(succState.BlackHolder, blackID[:]))
	require.EqualValues(t, 400_000_000, succParsed.Output.TokenBalance())
	require.Equal(t, SideWhite, succState.SideToMove(), "after black's first move, white moves next")
}

// =============================================================================
// TestOrdinaryMoveHappyPath: after acceptance, white plays a follow-up move
// =============================================================================

// playGame opens an origin (white e2-e4), accepts (black e7-e5) and returns
// the resulting OutputWithChainID + state plus the chain ID.
func playGame(t *testing.T, e *chessEnv, stake, accept uint64) (*ledger.OutputWithChainID, *ChessState, base.ChainID) {
	t.Helper()
	const sqE2, sqE4, sqE7, sqE5 = 12, 28, 52, 36
	whiteSpec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfterWhite := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs1 := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if txTs1.IsSlotBoundary() {
		txTs1 = txTs1.AddTicks(1)
	}
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         stake,
		TSlots:        50,
		FirstMoveSpec: whiteSpec,
		BoardAfter:    boardAfterWhite,
		TxTimestamp:   txTs1,
	})
	require.NoError(t, err)
	originTx, err := e.submit(originTxb.Bytes())
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))

	blackSpec := movePawnPush2Spec(sqE7, sqE5, pBP)
	boardAfterBlack := applyPawnPush2(boardAfterWhite, sqE7, sqE5, pBP)

	origin := loadChainOutput(t, e.u, chainID)
	bOuts := e.outputsOf(t, e.blackLock)
	txTs2 := e.nextTxTs(t, origin.ID.Timestamp())
	if txTs2.IsSlotBoundary() {
		txTs2 = txTs2.AddTicks(1)
	}
	acceptTxb, err := BuildAcceptance(BuildAcceptanceParams{
		BlackPrivKey:  e.blackPriv,
		BlackSigLock:  e.blackLock,
		OriginUTXO:    origin,
		FundingInputs: bOuts,
		NewAmount:     accept,
		FirstMoveSpec: blackSpec,
		BoardAfter:    boardAfterBlack,
		TxTimestamp:   txTs2,
	})
	require.NoError(t, err)
	_, err = e.submit(acceptTxb.Bytes())
	require.NoError(t, err)

	successor := loadChainOutput(t, e.u, chainID)
	succState, err := readChessStateFromOutput(successor.Output)
	require.NoError(t, err)
	return successor, succState, chainID
}

func TestOrdinaryMoveHappyPath(t *testing.T) {
	e := newChessEnv(t)
	prev, prevState, chainID := playGame(t, e, 200_000_000, 400_000_000)
	require.Equal(t, SideWhite, prevState.SideToMove())

	// White plays d2-d4 (sq 11 → 27).
	const sqD2, sqD4 = 11, 27
	spec := movePawnPush2Spec(sqD2, sqD4, pWP)
	boardAfter := applyPawnPush2(prevState.Board, sqD2, sqD4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs := e.nextTxTs(t, prev.ID.Timestamp())
	if txTs.IsSlotBoundary() {
		txTs = txTs.AddTicks(1)
	}
	txb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.whitePriv,
		MoverSigLock:  e.whiteLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(), // no top-up
		FundingInputs: wOuts,                       // for signing tx
		MoveSpec:      spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   txTs,
	})
	require.NoError(t, err)
	_, err = e.submit(txb.Bytes())
	require.NoError(t, err, "ordinary move tx must validate")

	updated := loadChainOutput(t, e.u, chainID)
	updatedState, err := readChessStateFromOutput(updated.Output)
	require.NoError(t, err)
	require.Equal(t, SideBlack, updatedState.SideToMove(), "after white's move, black moves next")

	// Dump the full move transaction so the shape of a chess covenant tx is
	// visible in the test log: inputs *with consumed UTXOs decoded*,
	// unlock data per input, produced outputs (chess UTXO with chain
	// constraint at 3 and chessState tuple-literal at 4, plus change),
	// tx-level constraints (the two redeemScript commitments decompiled),
	// and the signature.
	parsedTx, err := transaction.ParseAndValidate(txb.Bytes(), txb.LoadInputBytes)
	require.NoError(t, err)
	// Inputs of an already-applied move tx aren't in state anymore (the
	// chess UTXO has been spent). Use the builder's snapshot of consumed
	// outputs as the loader source instead.
	consumed := txb.ConsumedOutputs
	loader := func(i byte) ([]byte, error) {
		if int(i) >= len(consumed) {
			return nil, fmt.Errorf("input %d: out of range (%d consumed)", i, len(consumed))
		}
		return consumed[i].Bytes(), nil
	}
	t.Logf("chess move tx:\n%s", parsedTx.Lines(loader, "  ").String())
}

// =============================================================================
// TestTxSizeGate (chess_poc.md §8 Phase 1 step 5):
// A representative chess move tx — both bins committed via redeemScript, one
// input, one output, plus tag-along — must fit under the 65,531-byte network
// limit. If this fails, Phase 2 is moot.
// =============================================================================

const networkMaxTxBytes = 65_531

func TestTxSizeGate(t *testing.T) {
	e := newChessEnv(t)
	prev, prevState, _ := playGame(t, e, 200_000_000, 400_000_000)
	require.Equal(t, SideWhite, prevState.SideToMove())

	const sqD2, sqD4 = 11, 27
	spec := movePawnPush2Spec(sqD2, sqD4, pWP)
	boardAfter := applyPawnPush2(prevState.Board, sqD2, sqD4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs := e.nextTxTs(t, prev.ID.Timestamp())
	if txTs.IsSlotBoundary() {
		txTs = txTs.AddTicks(1)
	}
	txb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.whitePriv,
		MoverSigLock:  e.whiteLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(),
		FundingInputs: wOuts,
		MoveSpec:      spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   txTs,
	})
	require.NoError(t, err)

	txBytes := txb.Bytes()
	t.Logf("ordinary-move tx size = %d bytes (limit = %d)", len(txBytes), networkMaxTxBytes)
	require.Less(t, len(txBytes), networkMaxTxBytes,
		"Phase-1 gate: chess move tx must fit under network max")
}

// =============================================================================
// TestResignHappyPath: side-to-move resigns; opponent receives full bounty.
// =============================================================================

func TestResignHappyPath(t *testing.T) {
	e := newChessEnv(t)
	prev, prevState, _ := playGame(t, e, 200_000_000, 400_000_000)
	require.Equal(t, SideWhite, prevState.SideToMove())

	txTs := e.nextTxTs(t, prev.ID.Timestamp())
	if txTs.IsSlotBoundary() {
		txTs = txTs.AddTicks(1)
	}
	txb, err := BuildResign(BuildResignParams{
		ResignerPrivKey: e.whitePriv,   // white is side-to-move
		OpponentLock:    e.blackLock,
		PrevUTXO:        prev,
		TxTimestamp:     txTs,
	})
	require.NoError(t, err)
	_, err = e.submit(txb.Bytes())
	require.NoError(t, err, "resign tx must validate")

	// Black should hold a sigLock output with the full bounty.
	blackOuts := e.outputsOf(t, e.blackLock)
	require.NotEmpty(t, blackOuts)
	var got uint64
	for _, o := range blackOuts {
		if o.Output.TokenBalance() == 400_000_000 {
			got = o.Output.TokenBalance()
			break
		}
	}
	require.EqualValues(t, 400_000_000, got, "black must receive full bounty")
}

// =============================================================================
// TestTieAcceptHappyPath: white proposes tie, black accepts; bounty split 50/50.
// =============================================================================

func TestTieAcceptHappyPath(t *testing.T) {
	e := newChessEnv(t)
	prev, prevState, chainID := playGame(t, e, 200_000_000, 400_000_001) // odd so we test rounding
	require.Equal(t, SideWhite, prevState.SideToMove())

	// White plays a move with proposeTie=true.
	const sqD2, sqD4 = 11, 27
	spec := movePawnPush2Spec(sqD2, sqD4, pWP)
	boardAfter := applyPawnPush2(prevState.Board, sqD2, sqD4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs1 := e.nextTxTs(t, prev.ID.Timestamp())
	if txTs1.IsSlotBoundary() {
		txTs1 = txTs1.AddTicks(1)
	}
	moveTxb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.whitePriv,
		MoverSigLock:  e.whiteLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(),
		FundingInputs: wOuts,
		MoveSpec:      spec,
		BoardAfter:    boardAfter,
		ProposeTie:    true,
		TxTimestamp:   txTs1,
	})
	require.NoError(t, err)
	_, err = e.submit(moveTxb.Bytes())
	require.NoError(t, err)

	// Now black accepts the tie.
	withTieProposal := loadChainOutput(t, e.u, chainID)
	stateNow, err := readChessStateFromOutput(withTieProposal.Output)
	require.NoError(t, err)
	require.Equal(t, FlagTieProposed, stateNow.Flags&FlagTieProposed, "tie offer should be flagged")

	txTs2 := e.nextTxTs(t, withTieProposal.ID.Timestamp())
	if txTs2.IsSlotBoundary() {
		txTs2 = txTs2.AddTicks(1)
	}
	tieTxb, err := BuildTieAccept(BuildTieAcceptParams{
		OpponentPrivKey: e.blackPriv,
		WhiteLock:       e.whiteLock,
		BlackLock:       e.blackLock,
		PrevUTXO:        withTieProposal,
		TxTimestamp:     txTs2,
	})
	require.NoError(t, err)
	_, err = e.submit(tieTxb.Bytes())
	require.NoError(t, err, "tie-accept tx must validate")

	// Bounty = 400_000_001; white = ceil(/2) = 200_000_001; black = floor(/2) = 200_000_000.
	// (Pre-existing balances of each player are also non-zero, so we look for outputs
	//  with those specific amounts.)
	found := func(lock ledger.SigLock, want uint64) bool {
		for _, o := range e.outputsOf(t, lock) {
			if o.Output.TokenBalance() == want {
				return true
			}
		}
		return false
	}
	require.True(t, found(e.whiteLock, 200_000_001), "white must receive ceil(bounty/2) = 200_000_001")
	require.True(t, found(e.blackLock, 200_000_000), "black must receive floor(bounty/2) = 200_000_000")
}

// =============================================================================
// TestPreAcceptanceTimeoutClaim: white opens, no one accepts, deadline passes,
// white reclaims via timeout-claim.
// =============================================================================

func TestPreAcceptanceTimeoutClaim(t *testing.T) {
	e := newChessEnv(t)

	// Origin with a short deadline (T = 1 slot).
	const sqE2, sqE4 = 12, 28
	whiteSpec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfterWhite := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs1 := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if txTs1.IsSlotBoundary() {
		txTs1 = txTs1.AddTicks(1)
	}
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        2, // very short — deadline = txTs1.slot + 2
		FirstMoveSpec: whiteSpec,
		BoardAfter:    boardAfterWhite,
		TxTimestamp:   txTs1,
	})
	require.NoError(t, err)
	originTx, err := e.submit(originTxb.Bytes())
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))

	// Fast-forward time past the deadline by picking a tx timestamp after it.
	origin := loadChainOutput(t, e.u, chainID)
	originState, err := readChessStateFromOutput(origin.Output)
	require.NoError(t, err)
	// Deadline = txTs1.Slot + 2. Pick tx slot strictly past that.
	claimTs := base.T(originState.Deadline.Slot+1, 1)

	txb, err := BuildTimeoutClaim(BuildTimeoutClaimParams{
		ClaimantPrivKey: e.whitePriv, // pre-acceptance: white reclaims
		ClaimantLock:    e.whiteLock,
		PrevUTXO:        origin,
		TxTimestamp:     claimTs,
	})
	require.NoError(t, err)
	_, err = e.submit(txb.Bytes())
	require.NoError(t, err, "pre-acceptance timeout-claim must validate")

	// White should hold a sigLock output with the recovered stake.
	got := false
	for _, o := range e.outputsOf(t, e.whiteLock) {
		if o.Output.TokenBalance() == 200_000_000 {
			got = true
			break
		}
	}
	require.True(t, got, "white must recover the full stake")
}

// =============================================================================
// TestMove_WrongSignerRejected: white tries to move when it's black's turn.
// =============================================================================

func TestMove_WrongSignerRejected(t *testing.T) {
	e := newChessEnv(t)

	// Open origin only — side-to-move after origin is black (not white).
	const sqE2, sqE4 = 12, 28
	whiteSpec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfterWhite := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := e.outputsOf(t, e.whiteLock)
	txTs1 := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if txTs1.IsSlotBoundary() {
		txTs1 = txTs1.AddTicks(1)
	}
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        50,
		FirstMoveSpec: whiteSpec,
		BoardAfter:    boardAfterWhite,
		TxTimestamp:   txTs1,
	})
	require.NoError(t, err)
	originTx, err := e.submit(originTxb.Bytes())
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))

	// Pre-acceptance state: black is empty. White tries to "accept" their own game.
	origin := loadChainOutput(t, e.u, chainID)
	const sqE7, sqE5 = 52, 36
	blackSpec := movePawnPush2Spec(sqE7, sqE5, pBP)
	boardAfter := applyPawnPush2(boardAfterWhite, sqE7, sqE5, pBP)

	moreWhite := e.outputsOf(t, e.whiteLock)
	txTs2 := e.nextTxTs(t, origin.ID.Timestamp())
	if txTs2.IsSlotBoundary() {
		txTs2 = txTs2.AddTicks(1)
	}
	txb, err := BuildAcceptance(BuildAcceptanceParams{
		BlackPrivKey:  e.whitePriv, // attempts to use white's key as black
		BlackSigLock:  e.whiteLock,
		OriginUTXO:    origin,
		FundingInputs: moreWhite,
		NewAmount:     400_000_000,
		FirstMoveSpec: blackSpec,
		BoardAfter:    boardAfter,
		TxTimestamp:   txTs2,
	})
	require.NoError(t, err)
	_, err = e.submit(txb.Bytes())
	require.Error(t, err, "white cannot accept their own game")
	// The exact !!!err message is not surfaced by the callRedeemer wrapper, but
	// the path-to-failure must be the chess() lock at the consumed UTXO.
	require.Contains(t, err.Error(), "callRedeemer")
}

// =============================================================================
// TestCallRedeemer_PrivateFnRejected: chessGame's internal helpers all start
// with '_' and are private. Even if an outsider mints a fresh tx with a
// `callRedeemer(<gHash>, <private idx>)` extra-constraint, evaluation must
// reject it before running the body — the easyfl privacy gate runs in
// LocalScript.Eval. Picks a stable private fn we know exists (_stBoard).
// =============================================================================

func TestCallRedeemer_PrivateFnRejected(t *testing.T) {
	e := newChessEnv(t)
	bins := GetBins()

	// Find a private fn idx by decoding and scanning.
	lib := ledger.L(base.MaxSlot)
	gScript, err := lib.LocalScriptFromBytes(bins.GameBin)
	require.NoError(t, err)
	privateIdx := -1
	for i := 0; i < gScript.NumFunctions(); i++ {
		if gScript.IsPrivate(i) {
			privateIdx = i
			break
		}
	}
	require.GreaterOrEqual(t, privateIdx, 0, "chessGame must have at least one private helper")

	// Build a transfer tx that redeems chessGame and tries to invoke the
	// private fn directly. We use the existing redeem-test plumbing in
	// ledger/tests for this pattern, but inline it here to avoid an
	// internal dependency.
	wOuts := e.outputsOf(t, e.whiteLock)
	require.NotEmpty(t, wOuts)

	redeemSrc := fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bins.GameBin))
	_, _, redeemBC, err := lib.CompileExpression(redeemSrc)
	require.NoError(t, err)

	// callRedeemer(<gHash>, <privateIdx>) — no args; whatever it does inside
	// it can't run because Eval refuses dispatch.
	callSrc := fmt.Sprintf("callRedeemer(0x%s, 0x%02x)",
		hex.EncodeToString(bins.GameHash[:]), privateIdx)
	_, _, callBC, err := lib.CompileExpression(callSrc)
	require.NoError(t, err)

	txb := exhelp.New()
	total, _, err := txb.ConsumeOutputsUnlock(wOuts...)
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, uint64(100_000_000))

	target := ledger.OutputBasic(int64(100_000_000), e.thirdLock)
	_, err = txb.ProduceOutput(target)
	require.NoError(t, err)

	if total > 100_000_000 {
		// Change output, carrying the offending extra constraint at slot 4.
		change := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(total - 100_000_000)).WithLock(e.whiteLock)
			o.MustPushConstraint(callBC)
		})
		_, err = txb.ProduceOutput(change)
		require.NoError(t, err)
	}

	txb.PushTxConstraint(redeemBC)

	ts := e.nextTxTs(t, wOuts[0].ID.Timestamp())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.whitePriv)

	_, err = e.submit(txb.Bytes())
	require.Error(t, err, "callRedeemer of a private helper must be rejected")
	require.Contains(t, err.Error(), "private")
}
