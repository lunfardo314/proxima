package chess_poc

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
)

// AttachTagAlongParams collects everything AttachTagAlong needs to extend
// a chess covenant tx with a tag-along output payable to a sequencer.
type AttachTagAlongParams struct {
	// SignerPrivKey re-signs the tx after the tag-along input/outputs are
	// appended. MUST match the key passed to the prior Build* call —
	// otherwise sigLock unlocks (signature marker 0xff) won't validate.
	SignerPrivKey ed25519.PrivateKey

	// FundingInput is a sigLock-controlled wallet output whose holder
	// matches SignerPrivKey. Its token balance must be ≥ Fee.
	FundingInput *ledger.OutputWithID

	// ChangeLock receives any (FundingInput.amount - Fee) remainder.
	// Typically the signer's sigLock.
	ChangeLock ledger.SigLock

	// SeqID is the target sequencer chain that will pick the tx up.
	SeqID base.ChainID

	// Fee is the tag-along amount paid to SeqID.
	Fee uint64
}

// AttachTagAlong post-processes a chess covenant tx built by one of the
// Build* helpers: consumes a fresh wallet sigLock input, produces a
// `tagAlong(target=SeqID, sender=signer)` output sized to Fee, optionally
// produces a change output, then recomputes the input commitment and
// re-signs.
//
// Keeping this as a post-processor (instead of folding tag-along into
// every Build*) keeps the covenant builders untangled from network
// concerns — UTXODB tests still call Build* directly and skip this step.
func AttachTagAlong(txb *txbuilder.TxBuilder, p AttachTagAlongParams) error {
	if p.FundingInput == nil {
		return fmt.Errorf("AttachTagAlong: FundingInput required")
	}
	if p.FundingInput.Output.TokenBalance() < p.Fee {
		return fmt.Errorf("AttachTagAlong: funding %d < fee %d",
			p.FundingInput.Output.TokenBalance(), p.Fee)
	}

	senderID := HolderIDOf(p.SignerPrivKey)

	fundingIdx, err := txb.ConsumeOutput(p.FundingInput.Output, p.FundingInput.ID)
	if err != nil {
		return fmt.Errorf("AttachTagAlong: consume funding: %w", err)
	}
	// Funding sigLock unlock = signature marker (0xff). Matches the tx
	// signer's holder ID by construction.
	txb.PutSignatureUnlock(fundingIdx)

	// Produce the tag-along output. This is just another produced
	// output appended after whatever chess outputs Build* already
	// added; the chess covenant only constrains the chess slot itself
	// (and the payout slots for terminations), not subsequent outputs.
	tagAlongOut := ledger.NewTagAlongOutput(p.Fee, p.SeqID, senderID)
	if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
		return fmt.Errorf("AttachTagAlong: produce tag-along: %w", err)
	}

	if change := p.FundingInput.Output.TokenBalance() - p.Fee; change > 0 {
		if _, err = txb.ProduceOutput(ledger.OutputBasic(int64(change), p.ChangeLock)); err != nil {
			return fmt.Errorf("AttachTagAlong: produce change: %w", err)
		}
	}

	// Recompute input commitment over the now-extended consumed list,
	// then re-sign. SignED25519 overwrites the previous signature data.
	txb.ComputeInputCommitment()
	txb.SignED25519(p.SignerPrivKey)
	return nil
}
