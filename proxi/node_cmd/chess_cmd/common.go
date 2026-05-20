package chess_cmd

import (
	"crypto/ed25519"
	"fmt"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
)

const (
	defaultTSlots    = uint32(100)
	defaultPollEvery = time.Second
	maxFundingPick   = 2 // top-2 sigLock outputs by amount; first ≥ stake+fee, second covers tag-along
)

// parseChainID accepts both hex-encoded and short forms understood by
// base.ChainIDFromHexString. Errors out via glb.AssertNoError.
func parseChainID(arg string) base.ChainID {
	cid, err := base.ChainIDFromHexString(arg)
	glb.AssertNoError(err)
	return cid
}

// pickFundingInput selects the largest sigLock output the wallet
// controls and returns it plus the parsed total. Used as a small
// dedicated tag-along funding input.
func pickFundingInput(clnt *client.APIClient, walletLock ledger.SigLock, atLeast uint64) *ledger.OutputWithID {
	outs, _, total, err := clnt.GetTransferableOutputs(walletLock)
	glb.AssertNoError(err)
	glb.Assertf(total >= atLeast, "wallet has %d, need ≥ %d", total, atLeast)
	// Pick the smallest UTXO that still covers the requirement; falling
	// back to the largest if no single one is big enough.
	var best *ledger.OutputWithID
	for _, o := range outs {
		bal := o.Output.TokenBalance()
		if bal >= atLeast && (best == nil || bal < best.Output.TokenBalance()) {
			best = o
		}
	}
	if best == nil {
		// Single output won't cover — pick the biggest; caller is
		// responsible for combining or for tightening the ask.
		best = outs[0]
		for _, o := range outs[1:] {
			if o.Output.TokenBalance() > best.Output.TokenBalance() {
				best = o
			}
		}
	}
	return best
}

// pickFundingInputs returns multiple sigLock outputs whose combined
// balance is ≥ atLeast, picking largest-first. Errors if total balance
// is insufficient.
func pickFundingInputs(clnt *client.APIClient, walletLock ledger.SigLock, atLeast uint64) []*ledger.OutputWithID {
	outs, _, total, err := clnt.GetTransferableOutputs(walletLock)
	glb.AssertNoError(err)
	glb.Assertf(total >= atLeast, "wallet has %d, need ≥ %d", total, atLeast)
	// Sort descending by balance, take until we cover atLeast.
	sorted := make([]*ledger.OutputWithID, len(outs))
	copy(sorted, outs)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Output.TokenBalance() > sorted[i].Output.TokenBalance() {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	picked := make([]*ledger.OutputWithID, 0, 4)
	collected := uint64(0)
	for _, o := range sorted {
		picked = append(picked, o)
		collected += o.Output.TokenBalance()
		if collected >= atLeast {
			return picked
		}
	}
	glb.Assertf(false, "wallet UTXO selection failed: collected %d, need %d", collected, atLeast)
	return nil
}

// tagAlongFee resolves the fee + target sequencer from the proxi
// profile, fetching the sequencer's minimum-fee if needed.
func tagAlongFee() (base.ChainID, uint64) {
	seqIDPtr := glb.GetTagAlongSequencerID()
	glb.Assertf(seqIDPtr != nil, "tag_along.sequencer_id not configured")
	fee, err := glb.GetRequiredTagAlongFee(*seqIDPtr)
	glb.AssertNoError(err)
	glb.Assertf(fee > 0, "tag-along fee resolved to 0; configure tag_along.fee or run against a sequencer with min_fee > 0")
	return *seqIDPtr, fee
}

// fetchChessUTXO retrieves and parses the current chess UTXO for chainID.
// Includes the LRB ID returned by the node, suitable for "LRB:" lines.
func fetchChessUTXO(chainID base.ChainID) (*chess_poc.ChessGameState, base.TransactionID) {
	clnt := glb.GetClient()
	owc, lrb, err := clnt.GetChainOutput(chainID)
	glb.AssertNoError(err)
	gs, err := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
	glb.AssertNoError(err)
	return gs, lrb
}

// fetchAndPrintBoard prints "current chess state" + lines.
func fetchAndPrintBoard(chainID base.ChainID, banner string) *chess_poc.ChessGameState {
	gs, lrb := fetchChessUTXO(chainID)
	glb.Infof("%s (LRB %s):\n%s", banner, lrb.StringShort(), gs.Lines("    ").String())
	return gs
}

// submitAndTrack submits txBytes via glb.SubmitAndDisplay
// (validate_only=false) and (unless --nowait) tracks LRB inclusion.
// consumedBytes is the wire-form of every input in input-index order;
// passing it enables full-context validation server-side.
func submitAndTrack(txBytes []byte, consumedBytes [][]byte, txid base.TransactionID) {
	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		// SubmitAndDisplay already printed the error + failing tx lines.
		return
	}
	glb.Infof("submitted tx %s", txid.StringShort())
	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, defaultPollEvery)
}

// runChessAction is the common pipeline for "build txb, attach tag-along,
// submit + track". Returns the final tx ID.
func runChessAction(label string, txb *txbuilder.TxBuilder, priv ed25519.PrivateKey, walletLock ledger.SigLock, seqID base.ChainID, fee uint64, fundingInput *ledger.OutputWithID) base.TransactionID {
	err := chess_poc.AttachTagAlong(txb, chess_poc.AttachTagAlongParams{
		SignerPrivKey: priv,
		FundingInput:  fundingInput,
		ChangeLock:    walletLock,
		SeqID:         seqID,
		Fee:           fee,
	})
	glb.AssertNoError(err)

	tx, err := txb.Transaction()
	glb.AssertNoError(err)
	glb.Verbosef("---- %s tx ----\n%s\n---------------", label, tx.IDString())
	submitAndTrack(txb.Bytes(), txb.ConsumedOutputBytes(), tx.ID())
	return tx.ID()
}

// parseUint helps the cobra Run handlers.
func parseUint(s, name string) uint64 {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		glb.AssertNoError(fmt.Errorf("invalid %s %q: %v", name, s, err))
	}
	return v
}

// nextTxTimestamp picks a transaction timestamp respecting the
// transaction pace, derived from the LRB / a reference output's
// timestamp.
func nextTxTimestamp(after base.LedgerTime) base.LedgerTime {
	lib := ledger.L(after.Slot)
	ts := after.AddTicks(int(lib.TransactionPace))
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}
