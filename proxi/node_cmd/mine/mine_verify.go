package mine

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util/vrf"
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
	// shape, as _mineShape requires: one input, two or three outputs; counted
	// without a library and without panicking on hostile bytes
	if n, err := tx.NumElementsAtPath(ledger.PathToInputIDs); err != nil || n != 1 {
		return nil, fmt.Errorf("expected 1 input, got %d", n)
	}
	if n, err := tx.NumElementsAtPath(ledger.PathToProducedOutputs); err != nil || n < 2 || n > 3 {
		return nil, fmt.Errorf("expected 2 or 3 produced outputs, got %d", n)
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
	// mineLock state: the payout option, R decremented by what the option
	// mints, difficulty and full-slot count retargeted. A is a function of the
	// slot the transit is stamped in, so every amount rule is checked against
	// the successor's own slot.
	predSlot := pred.oid.Timestamp().Slot
	succSlot := tx.Timestamp().Slot
	ml, err := lib.ParseMineLock(succOut.MustConstraintAt(txbuildercore.ConstraintIndexLock))
	if err != nil {
		return nil, fmt.Errorf("successor mineLock: %w", err)
	}
	if ml.O < txbuildercore.MinePayoutOpening || ml.O > txbuildercore.MinePayoutFull {
		return nil, fmt.Errorf("payout option %d, expected 1, 2 or 3", ml.O)
	}
	a := consts.MineMintedAmount(ml.O, succSlot)
	if cc.CumulativeChainInflation != pred.cc.CumulativeChainInflation+a {
		return nil, fmt.Errorf("cumulative inflation %d, expected %d",
			cc.CumulativeChainInflation, pred.cc.CumulativeChainInflation+a)
	}
	if pred.ml.R < a {
		return nil, fmt.Errorf("mine chain is exhausted")
	}
	if ml.R != pred.ml.R-a {
		return nil, fmt.Errorf("R %d, expected %d", ml.R, pred.ml.R-a)
	}
	if ml.V != uint64(consts.MinerVersion) {
		return nil, fmt.Errorf("miner version %d, the ledger requires %d", ml.V, consts.MinerVersion)
	}
	if wantB, wantC := consts.MineRetarget(pred.ml.B, pred.ml.C, predSlot, succSlot); ml.B != wantB || ml.C != wantC {
		return nil, fmt.Errorf("difficulty %d / full slots %d, expected %d / %d", ml.B, ml.C, wantB, wantC)
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

	// the reward layout of the option: the full amount as one tag-along, or the
	// option's amount and the fixed fee; the lock kinds and the beneficiary are
	// the covenant's business, the amounts are what the tree must agree on
	rewardIdx, feeIdx := 1, 2
	if ml.O == txbuildercore.MinePayoutFull {
		feeIdx = 0
	}
	if n := tx.NumProducedOutputs(); n != feeIdx+1 && !(feeIdx == 0 && n == 2) {
		return nil, fmt.Errorf("%d outputs for payout option %d", n, ml.O)
	}
	reward, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(byte(rewardIdx)))
	if err != nil {
		return nil, fmt.Errorf("reward output: %w", err)
	}
	wantReward := a
	if feeIdx != 0 {
		wantReward = a - consts.MineTagAlongFee
		fee, err := txbuildercore.DecodeTokenBalance(tx.MustOutputDataAt(byte(feeIdx)))
		if err != nil {
			return nil, fmt.Errorf("tag-along output: %w", err)
		}
		if fee != consts.MineTagAlongFee {
			return nil, fmt.Errorf("tag-along fee %d, expected %d", fee, consts.MineTagAlongFee)
		}
	}
	if reward != wantReward {
		return nil, fmt.Errorf("reward %d, expected %d for payout option %d", reward, wantReward, ml.O)
	}

	// pace floor
	if succSlot < predSlot {
		return nil, fmt.Errorf("successor slot %d is before the predecessor %d", succSlot, predSlot)
	}
	if m := uint64(succSlot - predSlot); m < consts.MineMinPace {
		return nil, fmt.Errorf("pace %d below the minimum %d", m, consts.MineMinPace)
	}

	// signature. The node checked it before streaming, but the point of this
	// function is to owe the node nothing. The signer's key is also the VRF key.
	sig, err := verifyTxSignature(tx)
	if err != nil {
		return nil, err
	}

	// proof of work at the required difficulty: B, relieved once the gap exceeds
	// the relief pace. The VRF output under the signer's key over
	// predecessor || slot || nonce must have at least K trailing zero bits.
	unlock, err := tx.UnlockParameters(0, txbuildercore.ConstraintIndexLock)
	if err != nil || len(unlock) != txbuildercore.MineUnlockParamsLen {
		return nil, fmt.Errorf("mine lock unlock parameters must be proof || nonce (%d bytes)", txbuildercore.MineUnlockParamsLen)
	}
	var nonce [txbuildercore.MineNonceLen]byte
	copy(nonce[:], unlock[txbuildercore.MineVRFProofLen:])
	beta, err := vrf.Verify(sig.MustPubicKeyED25519(), txbuildercore.MineVRFMessage(pred.oid, succSlot, nonce), unlock[:txbuildercore.MineVRFProofLen])
	if err != nil {
		return nil, fmt.Errorf("VRF proof: %w", err)
	}
	needK := consts.MineRequiredK(pred.ml.B, uint64(succSlot-predSlot))
	if z := trailingZeroBits(beta); uint64(z) < needK {
		return nil, fmt.Errorf("insufficient proof of work: %d trailing zero bits, need %d", z, needK)
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
		vrfOutput:   beta,
		speculative: true,
	}, nil
}

// verifyTxSignature checks the single transaction signature over the tx ID and
// returns it, for the signer's public key.
func verifyTxSignature(tx *transaction.Transaction) (*base.Signature, error) {
	sig, err := tx.Signature()
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	if sig.SignatureType != base.SignatureTypeED25519 {
		return nil, fmt.Errorf("unsupported signature type %d", sig.SignatureType)
	}
	txid := tx.ID()
	if !ed25519.Verify(sig.MustPubicKeyED25519(), txid[:], sig.MustSignatureDataED25519()) {
		return nil, fmt.Errorf("invalid transaction signature")
	}
	return sig, nil
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
