package node_cmd

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"golang.org/x/crypto/blake2b"
)

// Verification of a mine-chain transit received over the mining stream.
//
// The node relays transits without vouching for them: at the point it streams
// one it has checked the structure and the signature and nothing else, so the
// proof of work is unverified and the shape that marks a transaction as a mine
// transit is attacker-forgeable. Steering on that feed unverified would let
// anyone divert every miner onto a fabricated chain for free.
//
// So the miner re-derives everything mineLock enforces, from the raw bytes plus
// the predecessor it already tracks. That is the whole of the mine-chain rules
// — the same arithmetic buildTemplate performs in the forward direction, run
// here as a check. No ledger context and no trust in the node are required;
// the node is trusted only not to withhold.

// verifyMineTransit checks a candidate transit against a known predecessor and
// returns the mine output it produces, which becomes the tip to build on.
func verifyMineTransit(
	lib *txbuildercore.Library[any],
	consts *txbuildercore.Constants,
	pred *mineTip,
	txBytes []byte,
) (*mineTip, error) {
	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	// shape, as _mineShape requires
	if n := tx.NumInputs(); n != 1 {
		return nil, fmt.Errorf("expected 1 input, got %d", n)
	}
	if n := tx.NumProducedOutputs(); n != 3 {
		return nil, fmt.Errorf("expected 3 produced outputs, got %d", n)
	}

	// It must spend the predecessor we know. This single check rejects every
	// forgery that does not build on the real mine chain, including
	// transactions that merely imitate the mine-transit shape.
	in, err := tx.InputAt(0)
	if err != nil {
		return nil, fmt.Errorf("input 0: %w", err)
	}
	if in != pred.oid {
		return nil, fmt.Errorf("input %s is not the predecessor %s", in.StringShort(), pred.oid.StringShort())
	}
	// ...and it must have been built against the predecessor's actual bytes,
	// not merely reference its ID
	if commitment := txbuildercore.HashOutputBytes(pred.data); !bytes.Equal(tx.InputCommitment(), commitment[:]) {
		return nil, fmt.Errorf("input commitment does not match the predecessor output")
	}

	succBytes := tx.MustOutputDataAt(0)
	succOut, err := txbuildercore.OutputFromBytes(succBytes)
	if err != nil {
		return nil, fmt.Errorf("successor output: %w", err)
	}

	// chain continuation
	cc, err := lib.ParseChainConstraint(succOut.MustConstraintAt(txbuildercore.ConstraintIndexChain))
	if err != nil {
		return nil, fmt.Errorf("successor chain constraint: %w", err)
	}
	if cc.ChainID != base.MineChainID {
		return nil, fmt.Errorf("successor is not on the mine chain")
	}
	if cc.TransitionCounter != pred.cc.TransitionCounter+1 {
		return nil, fmt.Errorf("transition counter %d, expected %d", cc.TransitionCounter, pred.cc.TransitionCounter+1)
	}
	// A is a function of the slot the transit is stamped in, so every amount rule
	// below is checked against the successor's own slot.
	predSlot := pred.oid.Timestamp().Slot
	succSlot := tx.Timestamp().Slot
	a := consts.MineAmountAtSlot(succSlot)
	if cc.CumulativeChainInflation != pred.cc.CumulativeChainInflation+a {
		return nil, fmt.Errorf("cumulative inflation %d, expected %d",
			cc.CumulativeChainInflation, pred.cc.CumulativeChainInflation+a)
	}

	// mineLock state: R decremented, difficulty retargeted, slot ring rolled
	ml, err := lib.ParseMineLock(succOut.MustConstraintAt(txbuildercore.ConstraintIndexLock))
	if err != nil {
		return nil, fmt.Errorf("successor mineLock: %w", err)
	}
	if pred.ml.R < a {
		return nil, fmt.Errorf("mine chain is exhausted")
	}
	if ml.R != pred.ml.R-a {
		return nil, fmt.Errorf("R %d, expected %d", ml.R, pred.ml.R-a)
	}
	if wantB := consts.MineAdjustedB(pred.ml.B, predSlot, succSlot); ml.B != wantB {
		return nil, fmt.Errorf("difficulty %d, expected %d", ml.B, wantB)
	}

	// amounts: balance carried over, exactly A minted as inflation
	amounts, err := txbuildercore.DecodeAmountsVector(succOut.MustConstraintAt(txbuildercore.ConstraintIndexAmounts))
	if err != nil {
		return nil, fmt.Errorf("successor amounts: %w", err)
	}
	if len(amounts) < 2 {
		return nil, fmt.Errorf("successor amounts vector too short: %d", len(amounts))
	}
	if amounts[0] != pred.balance {
		return nil, fmt.Errorf("balance %d, expected %d", amounts[0], pred.balance)
	}
	if amounts[1] != a {
		return nil, fmt.Errorf("inflation %d, expected %d", amounts[1], a)
	}

	// tag-along fee capped at 1% of A. The payout is then pinned by amount
	// conservation, so the fee cap is the only amount rule left to check.
	tagAlongBalance, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(2))
	if err != nil {
		return nil, fmt.Errorf("tag-along output: %w", err)
	}
	if tagAlongBalance*100 > a {
		return nil, fmt.Errorf("tag-along fee %d exceeds 1%% of A", tagAlongBalance)
	}

	// pace floor
	if succSlot < predSlot {
		return nil, fmt.Errorf("successor slot %d is before the predecessor %d", succSlot, predSlot)
	}
	if m := uint64(succSlot - predSlot); m < consts.MineMinPace {
		return nil, fmt.Errorf("pace %d below the minimum %d", m, consts.MineMinPace)
	}

	// proof of work at the required difficulty: B, relieved once the gap exceeds
	// the relief pace. The whole signed transaction must hash to at least K
	// trailing zero bits.
	needK := consts.MineRequiredK(pred.ml.B, uint64(succSlot-predSlot))
	if z := trailingZeroBits(blake2b.Sum256(txBytes)); uint64(z) < needK {
		return nil, fmt.Errorf("insufficient proof of work: %d trailing zero bits, need %d", z, needK)
	}

	// signature. The node checked it before streaming, but the point of this
	// function is to owe the node nothing.
	if err = verifyTxSignature(tx); err != nil {
		return nil, err
	}

	succOID, err := base.NewOutputID(tx.ID(), 0)
	if err != nil {
		return nil, err
	}
	return &mineTip{
		oid:         succOID,
		data:        succBytes,
		ml:          ml,
		cc:          cc,
		balance:     amounts[0],
		tagAlongFee: tagAlongBalance,
		speculative: true,
	}, nil
}

// verifyTxSignature checks the single transaction signature over the tx ID.
func verifyTxSignature(tx *transaction.Transaction) error {
	sig, err := tx.Signature()
	if err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	if sig.SignatureType != base.SignatureTypeED25519 {
		return fmt.Errorf("unsupported signature type %d", sig.SignatureType)
	}
	txid := tx.ID()
	if !ed25519.Verify(sig.MustPubicKeyED25519(), txid[:], sig.MustSignatureDataED25519()) {
		return fmt.Errorf("invalid transaction signature")
	}
	return nil
}

// transitParent is the mine output a candidate transit spends. It is read
// before verification, to find the predecessor to verify against.
func transitParent(txBytes []byte) (base.OutputID, error) {
	tx, err := transaction.ParseLibraryAgnostic(txBytes)
	if err != nil {
		return base.OutputID{}, fmt.Errorf("parse: %w", err)
	}
	if tx.NumInputs() != 1 {
		return base.OutputID{}, fmt.Errorf("expected 1 input, got %d", tx.NumInputs())
	}
	return tx.InputAt(0)
}
