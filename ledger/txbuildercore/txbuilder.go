package txbuildercore

import (
	"errors"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
)

// TxBuilder is the wasm-wallet-side transaction composer. It is the
// minimal compose surface — raw-byte oriented, no constraint serdes,
// no validator hooks, no sequencer-specific helpers.
//
// Wallet flow:
//
//	txb := txbuildercore.New(upgradeIndex)
//	for i, oid := range inputs {
//	    txb.ConsumeOutput(consumedOutputBytes[i], oid)
//	}
//	txb.ProduceOutput(producedBytes0)
//	txb.ProduceOutput(producedBytes1)
//	txb.PutSignatureUnlock(0)
//	txb.SetTimestamp(ts)
//	txb.ComputeInputCommitment()
//	// (Phase 3) txb.SignED25519(privKey)
//	raw := txb.Bytes()
//
// Server-side compose uses ledger/txbuilder.TxBuilder (typed); both
// produce byte-identical output because they share SerializeRawTx.
type TxBuilder struct {
	// consumedOutputBytes holds the raw bytes of each consumed UTXO,
	// in input-index order. Used by ComputeInputCommitment.
	consumedOutputBytes [][]byte

	// TxData is the wire-format-ready value the wallet manipulates.
	// Fields are written by builder methods; SerializeRawTx renders.
	TxData *TxRawData
}

// New returns a fresh TxBuilder for a transaction targeting the given
// library upgrade index. The wasm wallet bundles a single library
// version, so the upgrade index is a build-time constant.
func New(upgradeIndex uint16) *TxBuilder {
	return &TxBuilder{
		consumedOutputBytes: make([][]byte, 0),
		TxData: &TxRawData{
			UpgradeIndex:         upgradeIndex,
			Timestamp:            base.NilLedgerTime,
			SequencerOutputIndex: SequencerOutputIndexNone,
			InputIDs:             make([]*base.OutputID, 0),
			UnlockBlocks:         make([]*UnlockParams, 0),
			OutputBytes:          make([][]byte, 0),
			Endorsements:         make([]base.TransactionID, 0),
			TxConstraints:        make([][]byte, 0),
		},
	}
}

// NumInputs returns the number of consumed inputs registered so far.
func (txb *TxBuilder) NumInputs() int { return len(txb.TxData.InputIDs) }

// NumOutputs returns the number of produced outputs registered so far.
func (txb *TxBuilder) NumOutputs() int { return len(txb.TxData.OutputBytes) }

// ConsumeOutput registers a consumed UTXO at the next input index.
// The raw output bytes are kept so ComputeInputCommitment can hash
// them later; the OutputID is written into TxData.InputIDs.
//
// Returns the input index assigned to this consumption.
func (txb *TxBuilder) ConsumeOutput(outputBytes []byte, oid base.OutputID) byte {
	idx := byte(len(txb.consumedOutputBytes))
	txb.consumedOutputBytes = append(txb.consumedOutputBytes, outputBytes)
	txb.TxData.InputIDs = append(txb.TxData.InputIDs, &oid)
	txb.TxData.UnlockBlocks = append(txb.TxData.UnlockBlocks, NewUnlockBlock())
	return idx
}

// ProduceOutput registers a produced UTXO at the next output index.
// Returns the output index assigned.
func (txb *TxBuilder) ProduceOutput(outputBytes []byte) byte {
	idx := byte(len(txb.TxData.OutputBytes))
	txb.TxData.OutputBytes = append(txb.TxData.OutputBytes, outputBytes)
	return idx
}

// PutUnlockParams writes unlock-params bytes at (inputIndex,
// constraintIndex). additionalBytes are concatenated after the
// primary data (used by the signature-unlock pattern to append the
// 0xff marker plus tag-along references).
func (txb *TxBuilder) PutUnlockParams(inputIndex, constraintIndex byte, unlockParamData []byte, additionalBytes ...byte) {
	data := append([]byte(nil), unlockParamData...)
	data = append(data, additionalBytes...)
	txb.TxData.UnlockBlocks[inputIndex].PutAt(constraintIndex, data)
}

// PutSignatureUnlock writes the canonical "this input is unlocked by
// the transaction signature" marker (0xff) at (inputIndex,
// ConstraintIndexLock). additionalBytes (if any) are appended.
func (txb *TxBuilder) PutSignatureUnlock(inputIndex byte, additionalBytes ...byte) {
	txb.PutUnlockParams(inputIndex, ConstraintIndexLock, append([]byte{0xff}, additionalBytes...))
}

// PutUnlockReference points input inputIndex's lock at the unlock-
// params of input referencedInputIndex. The referenced index must be
// strictly less than inputIndex (validator-enforced; we error early).
func (txb *TxBuilder) PutUnlockReference(inputIndex, constraintIndex, referencedInputIndex byte) error {
	if referencedInputIndex >= inputIndex {
		return errors.New("referenced input index must be strongly less than the unlocked output index")
	}
	txb.PutUnlockParams(inputIndex, constraintIndex, []byte{referencedInputIndex})
	return nil
}

// PutStandardInputUnlocks is the most common unlock pattern: input 0
// uses the transaction signature; inputs 1..n-1 reference input 0's
// unlock at the lock slot.
func (txb *TxBuilder) PutStandardInputUnlocks(n int) error {
	easyfl_util.Assertf(n > 0, "n > 0")
	txb.PutSignatureUnlock(0)
	for i := 1; i < n; i++ {
		if err := txb.PutUnlockReference(byte(i), ConstraintIndexLock, 0); err != nil {
			return err
		}
	}
	return nil
}

// PushTxConstraint appends one tx-level constraint bytecode (currently
// used only for redeemScript local-script commitments; the slot is
// generic).
func (txb *TxBuilder) PushTxConstraint(bytecode []byte) {
	txb.TxData.TxConstraints = append(txb.TxData.TxConstraints, bytecode)
}

// PushEndorsements appends one or more endorsed transaction IDs.
func (txb *TxBuilder) PushEndorsements(txid ...base.TransactionID) {
	txb.TxData.Endorsements = append(txb.TxData.Endorsements, txid...)
}

// PutExplicitBaseline sets the explicit-baseline branch txID. Non-nil
// only when the wallet needs to pin the baseline (rare for compose
// flows; the host normally picks the baseline at attach time).
func (txb *TxBuilder) PutExplicitBaseline(txid *base.TransactionID) {
	txb.TxData.ExplicitBaseline = txid
}

// SetTimestamp writes the transaction's ledger timestamp.
func (txb *TxBuilder) SetTimestamp(ts base.LedgerTime) {
	txb.TxData.Timestamp = ts
}

// SetSequencerData sets the 2-byte sequencer-data slot. seqOutIdx
// also becomes the SequencerOutputIndex discriminator; stemOutIdx is
// the stem-output index used by branch transactions (pass
// SequencerOutputIndexNone = 0xff for non-branch).
func (txb *TxBuilder) SetSequencerData(seqOutIdx, stemOutIdx byte) {
	txb.TxData.SequencerOutputIndex = seqOutIdx
	txb.TxData.SequencerData = []byte{seqOutIdx, stemOutIdx}
}

// ComputeInputCommitment hashes the consumed-output bytes into
// TxData.InputCommitment using the same algorithm the validator
// expects (blake2b over the tuple-of-output-bytes).
//
// Call this after all ConsumeOutput / ProduceOutput / PutUnlock* are
// done and before signing.
func (txb *TxBuilder) ComputeInputCommitment() {
	txb.TxData.InputCommitment = HashOutputBytes(txb.consumedOutputBytes...)
}

// ConsumedOutputBytes returns the slice of consumed-output raw bytes
// in input-index order. Used by signing / tests; mutating the returned
// slice is undefined behaviour.
func (txb *TxBuilder) ConsumedOutputBytes() [][]byte {
	return txb.consumedOutputBytes
}

// ToTuple renders the transaction tree.
func (txb *TxBuilder) ToTuple() *tuples.Tuple {
	return SerializeRawTx(txb.TxData)
}

// Bytes renders the transaction bytes (wire form).
func (txb *TxBuilder) Bytes() []byte {
	return SerializeRawTxBytes(txb.TxData)
}
