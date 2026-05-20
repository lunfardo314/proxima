package chess_poc

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
)

// BenchmarkChessTxValidation measures full-context validation cost
// (Parse → SetFullContext → ValidateFullContext) for each chess covenant
// branch. The interesting variation isn't tx *building* time — that's
// negligible — but the redeemed-script evaluation cost: how much the
// chessGame dispatch + chessValidator calls (via callRedeemer) actually
// cost.
//
// Cost shape per branch:
//
//   origin                — _producedValidate → playerMove (one chessValidator call)
//   acceptance            — _branchMove → _bMoveAcceptance → playerMove + shape checks
//   ordinary_move         — _branchMove → _cMoveOrdinary → playerMove + sideToMove + shape checks
//   resign                — _branchResign (no chessValidator call; signer + payout only)
//   tie_accept            — _branchTieAccept (signer + payout split)
//   timeout_preacceptance — _branchTimeoutClaim pre-acceptance flavour (signer + payout)
//
// Each scenario validates against a pre-captured consumed-outputs vector
// (`b.ConsumedOutputs` at build time) so the only work in the timer loop
// is parse + validate.
func BenchmarkChessTxValidation(b *testing.B) {
	scenarios := buildBenchScenarios(b)
	for _, s := range scenarios {
		s := s
		b.Run(s.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(s.txBytes)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx, err := transaction.Parse(s.txBytes)
				if err != nil {
					b.Fatalf("Parse: %v", err)
				}
				if err = tx.SetFullContext(func(idx byte) (*ledger.Output, error) {
					return s.consumed[idx], nil
				}); err != nil {
					b.Fatalf("SetFullContext: %v", err)
				}
				if err = tx.ValidateFullContext(); err != nil {
					b.Fatalf("ValidateFullContext: %v", err)
				}
			}
			b.ReportMetric(float64(len(s.txBytes)), "tx_bytes")
		})
	}
}

// =============================================================================
// Scenario construction
// =============================================================================

type benchScenario struct {
	name     string
	txBytes  []byte
	consumed []*ledger.Output
}

// snapshot freezes a built tx into (bytes, consumed-outputs) so the
// scenario survives further state mutation.
func snapshot(name string, txb *txbuilder.TxBuilder) benchScenario {
	bytes := txb.Bytes()
	consumed := make([]*ledger.Output, len(txb.ConsumedOutputs))
	copy(consumed, txb.ConsumedOutputs)
	return benchScenario{name: name, txBytes: bytes, consumed: consumed}
}

func buildBenchScenarios(b *testing.B) []benchScenario {
	b.Helper()
	e := newBenchEnv(b)

	// ----- origin -----
	originTxb, originChainID, boardAfterWhite := bbBuildOrigin(b, e, 50 /*TSlots*/)
	originScn := snapshot("origin", originTxb)
	if _, err := e.submit(originTxb.Bytes()); err != nil {
		b.Fatalf("submit origin: %v", err)
	}

	// ----- acceptance -----
	acceptTxb, boardAfterBlack := bbBuildAcceptance(b, e, originChainID, boardAfterWhite)
	acceptScn := snapshot("acceptance", acceptTxb)
	if _, err := e.submit(acceptTxb.Bytes()); err != nil {
		b.Fatalf("submit acceptance: %v", err)
	}

	// ----- ordinary_move (no tie offer) -----
	movePlainTxb, _ := bbBuildOrdinaryMove(b, e, originChainID, boardAfterBlack, false)
	movePlainScn := snapshot("ordinary_move", movePlainTxb)

	// ----- propose-tie move (submitted; needed as predecessor for tie-accept) -----
	moveTieTxb, _ := bbBuildOrdinaryMove(b, e, originChainID, boardAfterBlack, true)
	if _, err := e.submit(moveTieTxb.Bytes()); err != nil {
		b.Fatalf("submit tie-propose move: %v", err)
	}

	// ----- tie_accept -----
	tieTxb := bbBuildTieAccept(b, e, originChainID)
	tieScn := snapshot("tie_accept", tieTxb)

	// ----- resign -----
	// resign predecessor is the current head (post tie-propose move). Side-
	// to-move is black, so black signs the resign.
	resignTxb := bbBuildResign(b, e, originChainID)
	resignScn := snapshot("resign", resignTxb)

	// ----- timeout_preacceptance: fresh env, short deadline, fast-forward -----
	timeoutTxb := bbBuildPreacceptanceTimeout(b)
	timeoutScn := snapshot("timeout_preacceptance", timeoutTxb)

	return []benchScenario{
		originScn, acceptScn, movePlainScn, tieScn, resignScn, timeoutScn,
	}
}

// =============================================================================
// Per-scenario builders — minimal helpers that drive the public Build*
// functions in chess_builder.go with realistic but uniform parameters.
// =============================================================================

// newBenchEnv re-creates the same chessEnv shape used in the test files
// but takes *testing.B. Re-funds white/black/third with enough tokens to
// cover all the txs built below.
func newBenchEnv(b *testing.B) *chessEnv {
	b.Helper()
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	wPriv, _, wLock := u.GenerateAddress(1)
	bPriv, _, bLock := u.GenerateAddress(2)
	tPriv, _, tLock := u.GenerateAddress(3)
	for _, p := range [][2]any{{wLock, uint64(1_000_000_000)}, {bLock, uint64(1_000_000_000)}, {tLock, uint64(1_000_000_000)}} {
		if err := u.TokensFromFaucet(p[0].(ledger.SigLock), p[1].(uint64)); err != nil {
			b.Fatalf("faucet: %v", err)
		}
	}
	return &chessEnv{
		u:         u,
		whitePriv: wPriv, whiteLock: wLock,
		blackPriv: bPriv, blackLock: bLock,
		thirdPriv: tPriv, thirdLock: tLock,
	}
}

// Address-list fetch for bench (mirrors chessEnv.outputsOf but uses *testing.B).
func bbOuts(b *testing.B, e *chessEnv, lock ledger.SigLock) []*ledger.OutputWithID {
	b.Helper()
	outsData, err := e.u.StateReader().GetUTXOsForController(lock.ControllerID())
	if err != nil {
		b.Fatalf("GetUTXOsForController: %v", err)
	}
	outs, err := ledger.ParseAndSortOutputData(outsData, func(_ *base.OutputID, o *ledger.Output) bool {
		return o.ChainConstraint() == nil && o.Lock().Name() == ledger.SigLockName
	})
	if err != nil {
		b.Fatalf("ParseAndSortOutputData: %v", err)
	}
	return outs
}

func bbTs(after base.LedgerTime) base.LedgerTime {
	lib := ledger.L(after.Slot)
	ts := after.AddTicks(int(lib.TransactionPace))
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}

func bbBuildOrigin(b *testing.B, e *chessEnv, tslots uint32) (*txbuilder.TxBuilder, base.ChainID, []byte) {
	b.Helper()
	const sqE2, sqE4 = 12, 28
	spec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfter := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := bbOuts(b, e, e.whiteLock)
	ts := bbTs(wOuts[0].ID.Timestamp())
	txb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        tslots,
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   ts,
	})
	if err != nil {
		b.Fatalf("BuildOrigin: %v", err)
	}
	tx, err := txb.Transaction()
	if err != nil {
		b.Fatalf("Transaction: %v", err)
	}
	chainID := base.MakeOriginChainID(base.MustNewOutputID(tx.ID(), 0))
	return txb, chainID, boardAfter
}

func bbBuildAcceptance(b *testing.B, e *chessEnv, chainID base.ChainID, boardAfterWhite []byte) (*txbuilder.TxBuilder, []byte) {
	b.Helper()
	const sqE7, sqE5 = 52, 36
	spec := movePawnPush2Spec(sqE7, sqE5, pBP)
	boardAfter := applyPawnPush2(boardAfterWhite, sqE7, sqE5, pBP)

	origin := loadChainOutputForBench(b, e, chainID)
	bOuts := bbOuts(b, e, e.blackLock)
	ts := bbTs(origin.ID.Timestamp())

	txb, err := BuildAcceptance(BuildAcceptanceParams{
		BlackPrivKey:  e.blackPriv,
		BlackSigLock:  e.blackLock,
		OriginUTXO:    origin,
		FundingInputs: bOuts,
		NewAmount:     400_000_000,
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   ts,
	})
	if err != nil {
		b.Fatalf("BuildAcceptance: %v", err)
	}
	return txb, boardAfter
}

func bbBuildOrdinaryMove(b *testing.B, e *chessEnv, chainID base.ChainID, prevBoard []byte, proposeTie bool) (*txbuilder.TxBuilder, []byte) {
	b.Helper()
	// White plays d2-d4.
	const sqD2, sqD4 = 11, 27
	spec := movePawnPush2Spec(sqD2, sqD4, pWP)
	boardAfter := applyPawnPush2(prevBoard, sqD2, sqD4, pWP)

	prev := loadChainOutputForBench(b, e, chainID)
	wOuts := bbOuts(b, e, e.whiteLock)
	ts := bbTs(prev.ID.Timestamp())

	txb, err := BuildMove(BuildMoveParams{
		MoverPrivKey:  e.whitePriv,
		MoverSigLock:  e.whiteLock,
		PrevUTXO:      prev,
		NewAmount:     prev.Output.TokenBalance(),
		FundingInputs: wOuts,
		MoveSpec:      spec,
		BoardAfter:    boardAfter,
		ProposeTie:    proposeTie,
		TxTimestamp:   ts,
	})
	if err != nil {
		b.Fatalf("BuildMove: %v", err)
	}
	return txb, boardAfter
}

func bbBuildTieAccept(b *testing.B, e *chessEnv, chainID base.ChainID) *txbuilder.TxBuilder {
	b.Helper()
	prev := loadChainOutputForBench(b, e, chainID)
	ts := bbTs(prev.ID.Timestamp())
	txb, err := BuildTieAccept(BuildTieAcceptParams{
		OpponentPrivKey: e.blackPriv, // black accepts (white proposed)
		WhiteLock:       e.whiteLock,
		BlackLock:       e.blackLock,
		PrevUTXO:        prev,
		TxTimestamp:     ts,
	})
	if err != nil {
		b.Fatalf("BuildTieAccept: %v", err)
	}
	return txb
}

func bbBuildResign(b *testing.B, e *chessEnv, chainID base.ChainID) *txbuilder.TxBuilder {
	b.Helper()
	prev := loadChainOutputForBench(b, e, chainID)
	prevState, err := readChessStateFromOutput(prev.Output)
	if err != nil {
		b.Fatalf("read predecessor state: %v", err)
	}
	// Side-to-move resigns.
	var signer ed25519.PrivateKey
	var opponent ledger.SigLock
	if prevState.SideToMove() == SideWhite {
		signer, opponent = e.whitePriv, e.blackLock
	} else {
		signer, opponent = e.blackPriv, e.whiteLock
	}
	ts := bbTs(prev.ID.Timestamp())
	txb, err := BuildResign(BuildResignParams{
		ResignerPrivKey: signer,
		OpponentLock:    opponent,
		PrevUTXO:        prev,
		TxTimestamp:     ts,
	})
	if err != nil {
		b.Fatalf("BuildResign: %v", err)
	}
	return txb
}

func bbBuildPreacceptanceTimeout(b *testing.B) *txbuilder.TxBuilder {
	b.Helper()
	e := newBenchEnv(b)

	const sqE2, sqE4 = 12, 28
	spec := movePawnPush2Spec(sqE2, sqE4, pWP)
	boardAfter := applyPawnPush2(canonStart(), sqE2, sqE4, pWP)

	wOuts := bbOuts(b, e, e.whiteLock)
	originTs := bbTs(wOuts[0].ID.Timestamp())
	originTxb, err := BuildOrigin(BuildOriginParams{
		WhitePrivKey:  e.whitePriv,
		WhiteSigLock:  e.whiteLock,
		FundingInputs: wOuts,
		Stake:         200_000_000,
		TSlots:        2, // tight deadline
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   originTs,
	})
	if err != nil {
		b.Fatalf("BuildOrigin (timeout env): %v", err)
	}
	if _, err := e.submit(originTxb.Bytes()); err != nil {
		b.Fatalf("submit origin (timeout env): %v", err)
	}
	originTx, _ := originTxb.Transaction()
	chainID := base.MakeOriginChainID(base.MustNewOutputID(originTx.ID(), 0))

	origin := loadChainOutputForBench(b, e, chainID)
	originState, _ := readChessStateFromOutput(origin.Output)
	claimTs := base.T(originState.Deadline.Slot+1, 1)

	txb, err := BuildTimeoutClaim(BuildTimeoutClaimParams{
		ClaimantPrivKey: e.whitePriv, // pre-acceptance reclaim
		ClaimantLock:    e.whiteLock,
		PrevUTXO:        origin,
		TxTimestamp:     claimTs,
	})
	if err != nil {
		b.Fatalf("BuildTimeoutClaim: %v", err)
	}
	return txb
}

// loadChainOutputForBench mirrors loadChainOutput (chess_poc_test.go) but
// takes *testing.B and bails with b.Fatal.
func loadChainOutputForBench(b *testing.B, e *chessEnv, chainID base.ChainID) *ledger.OutputWithChainID {
	b.Helper()
	chs, err := e.u.StateReader().GetUTXOForChainID(chainID)
	if err != nil {
		b.Fatalf("GetUTXOForChainID: %v", err)
	}
	parsed, err := chs.ParseAsChainOutput()
	if err != nil {
		b.Fatalf("ParseAsChainOutput: %v", err)
	}
	return parsed
}

// newChessEnvFromHelper is referenced by chess_bench_test.go's earlier
// scaffold; kept as a no-op alias so compilation stays clean if anyone
// re-introduces it. Unused at runtime.
var _ = newChessEnvFromHelper

func newChessEnvFromHelper(b *testing.B) *chessEnv {
	return newBenchEnv(b)
}
