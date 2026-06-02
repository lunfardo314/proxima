package txbuildercore

import (
	"encoding/binary"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SequencerOutputIndexNone is the sentinel that marks a transaction as
// non-sequencer: it tells SerializeRawTx to omit the 4-byte
// sequencer-data slot. Wallet builders should leave the
// SequencerOutputIndex field at this default.
const SequencerOutputIndexNone byte = 0xff

// SequencerDataLen is the wire-format length of the sequencer-data slot
// (TxSequencerDataBytes). Present only when SequencerOutputIndex !=
// SequencerOutputIndexNone. Today: 2 bytes (sequencer-output-index +
// stem-output-index).
const SequencerDataLen = 2

// UnlockParams is a tuple-backed mutable container for unlock-params
// bytes per input slot. The full ledger.txbuilder.TxBuilder keeps one
// per input; wallet code typically holds a slice of these directly.
type UnlockParams struct {
	array *tuples.TupleEditable
}

// NewUnlockBlock returns an empty UnlockParams.
func NewUnlockBlock() *UnlockParams {
	return &UnlockParams{array: tuples.EmptyTupleEditable(MaxNumConstraints)}
}

// Bytes returns the serialised tuple bytes (wire form).
func (u *UnlockParams) Bytes() []byte {
	return u.array.Bytes()
}

// PutAt writes data at the given constraint-index slot, padding empty
// slots before it if needed. additionalBytes are concatenated after the
// primary slot data (used by PutSignatureUnlock to append the 0xff
// signature marker plus tag-along references).
func (u *UnlockParams) PutAt(constraintIndex byte, data []byte) {
	u.array.MustPutAtIdxWithPadding(constraintIndex, data)
}

// TxRawData is the wire-format-ready transaction value: every field is
// either a primitive or raw bytes. Wallets construct it directly;
// server-side typed builders convert their typed transactionData to
// this shape just before serialisation.
//
// Field semantics mirror ToTuple in ledger/txbuilder (see tx layout
// constants above) — this struct is the input contract to
// SerializeRawTx.
type TxRawData struct {
	// UpgradeIndex is the library upgrade index for this tx's slot,
	// written as a 2-byte big-endian uint16 at TxVersion. The wallet
	// hardcodes the value matching its bundled library version; the
	// server reads it from ledger.L(slot).UpgradeIndex().
	UpgradeIndex uint16

	Timestamp base.LedgerTime

	// SequencerOutputIndex == SequencerOutputIndexNone (0xff) means
	// the transaction is non-sequencer; the TxSequencerDataBytes slot
	// is omitted from the serialised tuple. Otherwise SequencerData
	// (exactly SequencerDataLen bytes) is written.
	SequencerOutputIndex byte
	SequencerData        []byte

	SignatureData    []byte
	InputCommitment  [32]byte
	ExplicitBaseline *base.TransactionID

	InputIDs       []*base.OutputID
	UnlockBlocks   []*UnlockParams
	OutputBytes    [][]byte
	Endorsements   []base.TransactionID
	TxConstraints  [][]byte
}

// SerializeRawTx renders the wire-format transaction tuple. The output
// matches the ToTuple logic in ledger/txbuilder exactly so server-side
// parsers and the wasm wallet produce byte-identical results.
func SerializeRawTx(d *TxRawData) *tuples.Tuple {
	unlockParams := tuples.EmptyTupleEditable(MaxNumConstraints)
	inputIDs := tuples.EmptyTupleEditable(MaxNumConstraints)
	outputs := tuples.EmptyTupleEditable(MaxNumConstraints)
	endorsements := tuples.EmptyTupleEditable(MaxNumConstraints)
	var explicitBaseline []byte
	if d.ExplicitBaseline != nil {
		explicitBaseline = d.ExplicitBaseline[:]
	}

	for _, b := range d.UnlockBlocks {
		unlockParams.MustPush(b.Bytes())
	}
	for _, oid := range d.InputIDs {
		inputIDs.MustPush(oid[:])
	}
	for _, b := range d.OutputBytes {
		outputs.MustPush(b)
	}
	for _, e := range d.Endorsements {
		endorsements.MustPush(e.Bytes())
	}

	elems := make([]any, TxTreeTupleNumElements)
	versionBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(versionBytes, d.UpgradeIndex)
	elems[TxVersion] = versionBytes
	if len(d.TxConstraints) == 0 {
		// Backward-compat: empty list serialises as nil, matching the
		// pre-feature encoding.
		elems[TxConstraints] = nil
	} else {
		txc := tuples.EmptyTupleEditable(MaxNumConstraints)
		for _, b := range d.TxConstraints {
			txc.MustPush(b)
		}
		elems[TxConstraints] = txc
	}
	elems[TxTimestamp] = d.Timestamp.Bytes()
	if d.SequencerOutputIndex != SequencerOutputIndexNone {
		easyfl_util.Assertf(len(d.SequencerData) == SequencerDataLen,
			"txbuildercore.SerializeRawTx: sequencer data must be %d bytes, got %d",
			SequencerDataLen, len(d.SequencerData))
		elems[TxSequencerDataBytes] = d.SequencerData
	}
	elems[TxSignatureData] = d.SignatureData
	elems[TxInputCommitment] = d.InputCommitment[:]
	elems[TxExplicitBaseline] = explicitBaseline
	elems[TxInputIDs] = inputIDs
	elems[TxUnlockData] = unlockParams
	elems[TxOutputs] = outputs
	elems[TxEndorsements] = endorsements
	return tuples.MakeTupleFromSerializableElements(elems...)
}

// SerializeRawTxBytes is the byte-output form of SerializeRawTx.
func SerializeRawTxBytes(d *TxRawData) []byte {
	return SerializeRawTx(d).Bytes()
}
