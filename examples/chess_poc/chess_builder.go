package chess_poc

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"golang.org/x/crypto/blake2b"
)

// =============================================================================
// TxBuilder helpers — produce the chess covenant transactions sketched in
// chess_poc.md §5.
//
// Convention: each Build* function below takes the funding inputs explicitly
// so callers control coin selection, and returns the *txbuilder.TxBuilder
// ready to sign + serialise.
// =============================================================================

// Holder ID derived from an ed25519 keypair (mirrors the standard Proxima
// idiom hash(sigType ‖ pubkey)).
func HolderIDOf(priv ed25519.PrivateKey) base.HolderID {
	pub := priv.Public().(ed25519.PublicKey)
	body := append([]byte{base.SignatureTypeED25519}, pub...)
	return blake2b.Sum256(body)
}

// pushRedeemConstraints adds the two redeemScript constraints (chessValidator
// + chessGame) to the tx. Must be called on every chess covenant tx.
func pushRedeemConstraints(txb *txbuilder.TxBuilder) error {
	bins := GetBins()
	lib := ledger.L(base.MaxSlot)

	for _, bin := range [][]byte{bins.ValidatorBin, bins.GameBin} {
		src := fmt.Sprintf("redeemScript(0x%s)", hex.EncodeToString(bin))
		_, _, bc, err := lib.CompileExpression(src)
		if err != nil {
			return fmt.Errorf("pushRedeemConstraints: %w", err)
		}
		txb.PushTxConstraint(bc)
	}
	return nil
}

// buildChessOutput assembles the standard chess UTXO: amounts at 0,
// index-values [whiteHolderID, blackHolderID] at 1, chess() lock at 2,
// chain constraint at 3, chessState at 4.
func buildChessOutput(amount uint64, state *ChessState, cc *ledger.ChainConstraint) *ledger.Output {
	bins := GetBins()
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount))

		// index-values: [white, black]. Black may be empty pre-acceptance.
		ivs := [][]byte{state.WhiteHolder[:], append([]byte(nil), state.BlackHolder...)}
		o.PutConstraint(ledger.IndexValuesTupleBytes(ivs), ledger.ConstraintIndexIndexValues)

		// chess() lock at slot 2 (raw EasyFL bytecode; lock-kind not registered).
		o.PutConstraint(bins.LockBytecode, ledger.ConstraintIndexLock)

		// chain constraint at slot 3.
		o.PutConstraint(cc.Bytes(), ledger.ConstraintIndexChain)

		// chessState at slot 4.
		o.PutConstraint(state.Marshal(), ChessStateConstraintIndex)
	})
}

// =============================================================================
// Build helpers shared by all branches
// =============================================================================

// finaliseAndSign sets timestamp, computes input commitment, signs ed25519.
// txTs MUST be ≥ max(input timestamps) + TransactionPace.
func finaliseAndSign(txb *txbuilder.TxBuilder, txTs base.LedgerTime, priv ed25519.PrivateKey) {
	txb.TransactionData.Timestamp = txTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(priv)
}

// =============================================================================
// BuildOrigin (§5.1): create a fresh chess chain (white opens the game)
// =============================================================================

// BuildOriginParams collects inputs for BuildOrigin.
type BuildOriginParams struct {
	WhitePrivKey  ed25519.PrivateKey   // signer = white
	WhiteSigLock  ledger.SigLock       // white's funding lock (for change)
	FundingInputs []*ledger.OutputWithID
	Stake         uint64               // tokens locked into the chess UTXO
	TSlots        uint32               // per-game move budget
	FirstMoveSpec []byte               // 5-byte chessValidator move spec
	BoardAfter    []byte               // 69-byte board after white's first move
	TxTimestamp   base.LedgerTime      // tx timestamp; deadline = TxTimestamp.Slot + TSlots
}

func BuildOrigin(p BuildOriginParams) (*txbuilder.TxBuilder, error) {
	if p.Stake == 0 {
		return nil, fmt.Errorf("BuildOrigin: stake must be > 0")
	}
	if p.TSlots == 0 {
		return nil, fmt.Errorf("BuildOrigin: TSlots must be > 0")
	}
	if len(p.FirstMoveSpec) != 5 {
		return nil, fmt.Errorf("BuildOrigin: FirstMoveSpec must be 5 bytes")
	}
	if len(p.BoardAfter) != 69 {
		return nil, fmt.Errorf("BuildOrigin: BoardAfter must be 69 bytes")
	}

	txb := txbuilder.New()
	total, _, err := txb.ConsumeOutputsUnlock(p.FundingInputs...)
	if err != nil {
		return nil, fmt.Errorf("BuildOrigin: consume inputs: %w", err)
	}
	if total < p.Stake {
		return nil, fmt.Errorf("BuildOrigin: insufficient funding: have %d, need %d", total, p.Stake)
	}

	whiteHolder := HolderIDOf(p.WhitePrivKey)
	deadline := base.T(p.TxTimestamp.Slot+p.TSlots, 0)

	state := &ChessState{
		Board:        append([]byte(nil), p.BoardAfter...),
		LastMoveSpec: append([]byte(nil), p.FirstMoveSpec...),
		WhiteHolder:  whiteHolder,
		BlackHolder:  nil, // empty pre-acceptance
		TSlots:       p.TSlots,
		Deadline:     deadline,
		Flags:        0,
	}
	cc := ledger.NewChainOrigin(p.TxTimestamp.Slot)

	chessOut := buildChessOutput(p.Stake, state, cc)
	if _, err := txb.ProduceOutput(chessOut); err != nil {
		return nil, fmt.Errorf("BuildOrigin: produce chess UTXO: %w", err)
	}

	if change := total - p.Stake; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.WhiteSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildOrigin: produce change: %w", err)
		}
	}

	if err := pushRedeemConstraints(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.WhitePrivKey)
	return txb, nil
}

// =============================================================================
// BuildAcceptance (§5.2): black accepts; chess UTXO succeeds origin
// =============================================================================

type BuildAcceptanceParams struct {
	BlackPrivKey  ed25519.PrivateKey
	BlackSigLock  ledger.SigLock
	OriginUTXO    *ledger.OutputWithChainID
	FundingInputs []*ledger.OutputWithID
	NewAmount     uint64           // must be ≥ 2 × origin amount
	FirstMoveSpec []byte           // black's first move (5 bytes)
	BoardAfter    []byte           // 69-byte board after black's first move
	TxTimestamp   base.LedgerTime
}

func BuildAcceptance(p BuildAcceptanceParams) (*txbuilder.TxBuilder, error) {
	if p.OriginUTXO == nil {
		return nil, fmt.Errorf("BuildAcceptance: OriginUTXO required")
	}
	predAmount := p.OriginUTXO.Output.TokenBalance()
	if p.NewAmount < 2*predAmount {
		return nil, fmt.Errorf("BuildAcceptance: NewAmount %d < 2 × predecessor amount %d", p.NewAmount, predAmount)
	}
	predState, err := readChessStateFromOutput(p.OriginUTXO.Output)
	if err != nil {
		return nil, fmt.Errorf("BuildAcceptance: parse predecessor: %w", err)
	}

	txb := txbuilder.New()

	// Consume the chess UTXO first (input index 0).
	chessInIdx, err := txb.ConsumeOutput(p.OriginUTXO.Output, p.OriginUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("BuildAcceptance: consume chess UTXO: %w", err)
	}

	// Consume black's funding inputs (with sigLock unlocks; first sets signature).
	if len(p.FundingInputs) == 0 {
		return nil, fmt.Errorf("BuildAcceptance: at least one funding input required")
	}
	fundingTotal := uint64(0)
	for i, in := range p.FundingInputs {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		if err != nil {
			return nil, fmt.Errorf("BuildAcceptance: consume funding input %d: %w", i, err)
		}
		if i == 0 {
			txb.PutSignatureUnlock(idx)
		} else {
			if err := txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(chessInIdx)+1)); err != nil {
				return nil, fmt.Errorf("BuildAcceptance: put unlock reference: %w", err)
			}
		}
		fundingTotal += in.Output.TokenBalance()
	}

	// Chess UTXO unlock: 1 byte branch selector = move.
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexLock, []byte{BranchMove})

	// Build successor chess state (move 2: black accepts).
	blackHolder := HolderIDOf(p.BlackPrivKey)
	deadline := base.T(p.TxTimestamp.Slot+predState.TSlots, 0)
	succState := &ChessState{
		Board:        append([]byte(nil), p.BoardAfter...),
		LastMoveSpec: append([]byte(nil), p.FirstMoveSpec...),
		WhiteHolder:  predState.WhiteHolder,
		BlackHolder:  blackHolder[:],
		TSlots:       predState.TSlots,
		Deadline:     deadline,
		Flags:        0,
	}

	// Chain constraint: transition from origin.
	succCC := ledger.NewChainConstraint(p.OriginUTXO.ChainID, chessInIdx, p.OriginUTXO.OriginSlot, 0, 0, 1, 0)
	succChess := buildChessOutput(p.NewAmount, succState, succCC)
	succIdx, err := txb.ProduceOutput(succChess)
	if err != nil {
		return nil, fmt.Errorf("BuildAcceptance: produce successor: %w", err)
	}
	// Chain unlock: 1-byte successor index.
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	// Change back to black.
	stakeFromBlack := p.NewAmount - predAmount
	if fundingTotal < stakeFromBlack {
		return nil, fmt.Errorf("BuildAcceptance: insufficient funding: have %d, need %d", fundingTotal, stakeFromBlack)
	}
	if change := fundingTotal - stakeFromBlack; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.BlackSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildAcceptance: produce change: %w", err)
		}
	}

	if err := pushRedeemConstraints(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.BlackPrivKey)
	return txb, nil
}

// =============================================================================
// BuildMove (§5.3): ordinary move (post-acceptance)
// =============================================================================

type BuildMoveParams struct {
	MoverPrivKey ed25519.PrivateKey   // must match side-to-move
	MoverSigLock ledger.SigLock       // for change (optional funding)
	PrevUTXO     *ledger.OutputWithChainID
	NewAmount    uint64               // ≥ predecessor amount
	FundingInputs []*ledger.OutputWithID // optional, for amount top-up
	MoveSpec     []byte               // 5 bytes
	BoardAfter   []byte               // 69 bytes
	ProposeTie   bool
	TxTimestamp  base.LedgerTime
}

func BuildMove(p BuildMoveParams) (*txbuilder.TxBuilder, error) {
	predState, err := readChessStateFromOutput(p.PrevUTXO.Output)
	if err != nil {
		return nil, fmt.Errorf("BuildMove: parse predecessor: %w", err)
	}
	predAmount := p.PrevUTXO.Output.TokenBalance()
	if p.NewAmount < predAmount {
		return nil, fmt.Errorf("BuildMove: NewAmount %d < predecessor %d", p.NewAmount, predAmount)
	}

	txb := txbuilder.New()
	chessInIdx, err := txb.ConsumeOutput(p.PrevUTXO.Output, p.PrevUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("BuildMove: consume chess UTXO: %w", err)
	}
	// Lock unlock = branch selector for move.
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexLock, []byte{BranchMove})

	// Funding inputs (optional).
	fundingTotal := uint64(0)
	hasSig := false
	for i, in := range p.FundingInputs {
		idx, err := txb.ConsumeOutput(in.Output, in.ID)
		if err != nil {
			return nil, fmt.Errorf("BuildMove: consume funding input %d: %w", i, err)
		}
		if !hasSig {
			txb.PutSignatureUnlock(idx)
			hasSig = true
		} else {
			if err := txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, byte(int(chessInIdx)+1)); err != nil {
				return nil, fmt.Errorf("BuildMove: put unlock reference: %w", err)
			}
		}
		fundingTotal += in.Output.TokenBalance()
	}
	if !hasSig {
		// no funding inputs — signature must still be present; usually rare for move because
		// the mover at least needs a tag-along payer. We still need to satisfy tx-signature
		// requirements: build will fail with "no signature" — caller must supply at least 1 input.
		return nil, fmt.Errorf("BuildMove: at least one signed funding input required to sign tx")
	}

	stakeFromMover := p.NewAmount - predAmount
	if fundingTotal < stakeFromMover {
		return nil, fmt.Errorf("BuildMove: insufficient funding: have %d, need %d", fundingTotal, stakeFromMover)
	}

	// Build successor state.
	flags := byte(0)
	if p.ProposeTie {
		flags |= FlagTieProposed
	}
	succState := &ChessState{
		Board:        append([]byte(nil), p.BoardAfter...),
		LastMoveSpec: append([]byte(nil), p.MoveSpec...),
		WhiteHolder:  predState.WhiteHolder,
		BlackHolder:  append([]byte(nil), predState.BlackHolder...),
		TSlots:       predState.TSlots,
		Deadline:     base.T(p.TxTimestamp.Slot+predState.TSlots, 0),
		Flags:        flags,
	}

	predCC := p.PrevUTXO.Output.ChainConstraint()
	succCC := ledger.NewChainConstraint(p.PrevUTXO.ChainID, chessInIdx, p.PrevUTXO.OriginSlot,
		0, 0, predCC.TransitionCounter+1, 0)
	succChess := buildChessOutput(p.NewAmount, succState, succCC)
	succIdx, err := txb.ProduceOutput(succChess)
	if err != nil {
		return nil, fmt.Errorf("BuildMove: produce successor: %w", err)
	}
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))

	// Change back to mover.
	if change := fundingTotal - stakeFromMover; change > 0 {
		ret := ledger.OutputBasic(int64(change), p.MoverSigLock)
		if _, err := txb.ProduceOutput(ret); err != nil {
			return nil, fmt.Errorf("BuildMove: produce change: %w", err)
		}
	}

	if err := pushRedeemConstraints(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, p.TxTimestamp, p.MoverPrivKey)
	return txb, nil
}

// =============================================================================
// Termination branches (§5.4): tie-accept / resign / timeout-claim
// =============================================================================

// buildTermination is the common scaffolding for the three termination
// branches. payouts is the ordered list of (recipient sigLock, amount)
// pairs that become produced outputs 0 and 1 (in that order).
func buildTermination(
	priv ed25519.PrivateKey,
	prevUTXO *ledger.OutputWithChainID,
	branchSelector byte,
	payouts []termPayout,
	txTs base.LedgerTime,
) (*txbuilder.TxBuilder, error) {
	txb := txbuilder.New()
	chessInIdx, err := txb.ConsumeOutput(prevUTXO.Output, prevUTXO.ID)
	if err != nil {
		return nil, fmt.Errorf("buildTermination: consume chess UTXO: %w", err)
	}
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexLock, []byte{branchSelector})
	// Empty chain unlock = discontinue chain (chess_poc.md §4.2/§4.3/§4.4).
	txb.PutUnlockParams(chessInIdx, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	// Tx signature must be from the chain input — but our consumer signs the tx
	// directly. Place the signature unlock on the chess input as well so the
	// signer is recorded; the chain constraint's chain-end path also fires.
	// Actually the chess() lock doesn't require a separate sigLock-style unlock;
	// the signature data is global. We only need to provide chain unlock + lock
	// unlock above. SignED25519 will populate TxSignatureData.

	// Produce payouts.
	for i, po := range payouts {
		out := ledger.OutputBasic(int64(po.Amount), po.Lock)
		idx, err := txb.ProduceOutput(out)
		if err != nil {
			return nil, fmt.Errorf("buildTermination: produce payout %d: %w", i, err)
		}
		if int(idx) != i {
			return nil, fmt.Errorf("buildTermination: payout %d landed at output index %d", i, idx)
		}
	}

	if err := pushRedeemConstraints(txb); err != nil {
		return nil, err
	}
	finaliseAndSign(txb, txTs, priv)
	return txb, nil
}

type termPayout struct {
	Lock   ledger.SigLock
	Amount uint64
}

// BuildTieAccept: opponent of the proposer accepts a pending tie.
type BuildTieAcceptParams struct {
	OpponentPrivKey ed25519.PrivateKey // signer = NOT side-to-move
	WhiteLock       ledger.SigLock     // pays ⌈amount/2⌉
	BlackLock       ledger.SigLock     // pays ⌊amount/2⌋
	PrevUTXO        *ledger.OutputWithChainID
	TxTimestamp     base.LedgerTime
}

func BuildTieAccept(p BuildTieAcceptParams) (*txbuilder.TxBuilder, error) {
	amount := p.PrevUTXO.Output.TokenBalance()
	whiteShare := (amount + 1) / 2
	blackShare := amount / 2
	return buildTermination(p.OpponentPrivKey, p.PrevUTXO, BranchTieAccept,
		[]termPayout{{Lock: p.WhiteLock, Amount: whiteShare}, {Lock: p.BlackLock, Amount: blackShare}},
		p.TxTimestamp)
}

// BuildResign: side-to-move resigns; opponent receives full bounty.
type BuildResignParams struct {
	ResignerPrivKey ed25519.PrivateKey
	OpponentLock    ledger.SigLock
	PrevUTXO        *ledger.OutputWithChainID
	TxTimestamp     base.LedgerTime
}

func BuildResign(p BuildResignParams) (*txbuilder.TxBuilder, error) {
	return buildTermination(p.ResignerPrivKey, p.PrevUTXO, BranchResign,
		[]termPayout{{Lock: p.OpponentLock, Amount: p.PrevUTXO.Output.TokenBalance()}},
		p.TxTimestamp)
}

// BuildTimeoutClaim: deadline passed; claimant takes the chain.
// Claimant identity depends on chess state — see chess_poc.md §4.4.
type BuildTimeoutClaimParams struct {
	ClaimantPrivKey ed25519.PrivateKey
	ClaimantLock    ledger.SigLock
	PrevUTXO        *ledger.OutputWithChainID
	TxTimestamp     base.LedgerTime
}

func BuildTimeoutClaim(p BuildTimeoutClaimParams) (*txbuilder.TxBuilder, error) {
	return buildTermination(p.ClaimantPrivKey, p.PrevUTXO, BranchTimeoutClaim,
		[]termPayout{{Lock: p.ClaimantLock, Amount: p.PrevUTXO.Output.TokenBalance()}},
		p.TxTimestamp)
}

// =============================================================================
// Utilities
// =============================================================================

// readChessStateFromOutput extracts and parses the chessState tuple at
// output element index 4.
func readChessStateFromOutput(o *ledger.Output) (*ChessState, error) {
	bin, err := o.ConstraintAt(ChessStateConstraintIndex)
	if err != nil {
		return nil, fmt.Errorf("readChessStateFromOutput: %w", err)
	}
	return UnmarshalChessState(bin)
}

// chessLockBytecode is exported for tests that want to spot-check the chess()
// lock bytecode handed to outputs.
func ChessLockBytecode() []byte { return GetBins().LockBytecode }

// ChessValidatorBin / ChessGameBin expose the redeemed-script bytecodes.
func ChessValidatorBin() easyfl.LocalScriptBin { return GetBins().ValidatorBin }
func ChessGameBin() easyfl.LocalScriptBin      { return GetBins().GameBin }
