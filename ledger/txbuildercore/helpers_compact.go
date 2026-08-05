package txbuildercore

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// CompactInput is one UTXO to sweep: raw output bytes plus the ID they were
// read under. Bytes-only so the helper stays wasm-clean; callers holding
// typed outputs pass o.Output.Bytes() and o.ID.
type CompactInput struct {
	OutputBytes []byte
	ID          base.OutputID
}

// CompactParams describes one compacting transaction.
//
// Inputs must already be filtered to what the wallet can claim with a plain
// signature unlock — ClassifySpendable == SpendSimple for the signer's holder
// ID at TargetSlot. MakeCompactTransaction does not re-check claimability; it
// composes what it is given.
type CompactParams struct {
	Inputs           []CompactInput
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    base.ChainID
	TagAlongFee      uint64
	// TargetSlot is the slot the caller classified the inputs at. The
	// transaction lands at or after it — never earlier, or a sendWithDeadline
	// or tagAlong window checked at TargetSlot could still be closed on chain.
	TargetSlot uint32
}

// compactTimestampTick is where in TargetSlot a compacting transaction aims.
// Only a starting point: the timestamp is pushed past the newest input by the
// transaction pace below.
const compactTimestampTick = 10

// MakeCompactTransaction sweeps the given UTXOs into a single sigLock output
// back to the signer, minus a tag-along fee output. It is the shared compose
// step behind `proxi node compact` and the miner's payout consolidation.
//
// Unlock pattern: PutSignatureUnlock(0) on input 0 (which carries the tx
// signature) and a reference unlock to input 0 on the rest. The reference path
// is what makes a homogeneous sigLock sweep cheap — same lock bytecode, same
// holder, so the ledger skips a holder-hash derivation per input. On inputs
// whose lock is not the plain sigLock (sendWithDeadline, tagAlong) the ledger
// refuses the reference and falls back to the signer check, which the wallet
// satisfies anyway as master/sender; so mixing kinds is safe and costs nothing
// beyond the derivation the reference would have saved.
func MakeCompactTransaction(lib *Library[any], consts *Constants, par CompactParams) (txBytes []byte, txid base.TransactionID, consumed [][]byte, err error) {
	if len(par.Inputs) == 0 {
		return nil, base.TransactionID{}, nil, errors.New("compact: no inputs")
	}
	if len(par.Inputs) > 256 {
		return nil, base.TransactionID{}, nil, fmt.Errorf("compact: %d inputs exceeds the 256 maximum", len(par.Inputs))
	}

	txb := New(0)
	inTotal := uint64(0)
	newestInput := base.NilLedgerTime
	consumed = make([][]byte, 0, len(par.Inputs))
	for i, in := range par.Inputs {
		balance, err := DecodeTokenBalance(in.OutputBytes)
		if err != nil {
			return nil, base.TransactionID{}, nil, fmt.Errorf("compact: input %d: %w", i, err)
		}
		txb.ConsumeOutput(in.OutputBytes, in.ID)
		consumed = append(consumed, in.OutputBytes)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else if err = txb.PutUnlockReference(byte(i), ConstraintIndexLock, 0); err != nil {
			return nil, base.TransactionID{}, nil, err
		}
		inTotal += balance
		newestInput = base.MaximumTime(newestInput, in.ID.Timestamp())
	}
	if inTotal < par.TagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("compact: balance %d is short of the tag-along fee %d", inTotal, par.TagAlongFee)
	}

	walletHolderID := base.HolderIDFromED25519PrivateKey(par.WalletPrivateKey)
	mainOut, err := NewSigLockOutput(lib, inTotal-par.TagAlongFee, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(mainOut.Bytes())

	taOut, err := NewTagAlongOutput(lib, par.TagAlongFee, par.TagAlongSeqID, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(taOut.Bytes())

	txb.SetTimestamp(compactTimestamp(consts, par.TargetSlot, newestInput))
	txb.ComputeInputCommitment()
	txb.SignED25519(par.WalletPrivateKey)

	txBytes = txb.Bytes()
	if txid, err = TxIDFromBytes(txBytes); err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	return txBytes, txid, consumed, nil
}

// compactTimestamp picks a timestamp inside targetSlot that also clears the
// newest consumed input by the transaction pace. Sweeping a just-received
// output would otherwise produce a transaction that is invalid on arrival:
// aiming at a fixed tick says nothing about where in the slot the inputs
// landed. Slot boundaries are reserved for branch transactions.
func compactTimestamp(consts *Constants, targetSlot uint32, newestInput base.LedgerTime) base.LedgerTime {
	ts := base.MaximumTime(
		base.T(targetSlot, compactTimestampTick),
		newestInput.AddTicks(int(consts.TransactionPace)),
	)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}
