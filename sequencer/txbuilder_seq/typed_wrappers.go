package txbuilder_seq

// Typed-output wrappers over the embedded *txbuildercore.TxBuilder.
// txbuildercore is bytes-only by design; the sequencer wants
// *ledger.Output ergonomics (typed inspection of amounts, locks, chain
// constraints in the past cone). These thin shadows keep typed slices
// of consumed / produced outputs in sync with the byte-level state the
// core builder maintains.

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SetTimestamp sets the transaction timestamp and the UpgradeIndex
// from the ledger library version at that slot. Shadows the core
// builder's SetTimestamp.
func (txb *SeqTxBuilder) SetTimestamp(ts base.LedgerTime) {
	txb.TxBuilder.SetTimestamp(ts)
	txb.TxData.UpgradeIndex = ledger.L(ts.Slot).UpgradeIndex()
}

// ConsumeOutput appends a typed consumed output and forwards its raw
// bytes to the embedded core builder. Returns the assigned input index.
func (txb *SeqTxBuilder) ConsumeOutput(out *ledger.Output, oid base.OutputID) (byte, error) {
	if txb.NumInputs() >= 256 {
		return 0, fmt.Errorf("SeqTxBuilder.ConsumeOutput: too many consumed outputs")
	}
	txb.ConsumedOutputs = append(txb.ConsumedOutputs, out)
	return txb.TxBuilder.ConsumeOutput(out.Bytes(), oid), nil
}

// ProduceOutput adds a typed produced output (forwarding bytes to the
// embedded core) after enforcing storage-deposit minimum and tuple
// validity. Returns the assigned output index.
func (txb *SeqTxBuilder) ProduceOutput(o *ledger.Output) (byte, error) {
	if err := o.EnoughAmountForStorageDeposit(); err != nil {
		return 0, fmt.Errorf("SeqTxBuilder.ProduceOutput: %v", err)
	}
	o.MustValidOutput()
	if txb.NumOutputs() >= 256 {
		return 0, fmt.Errorf("SeqTxBuilder.ProduceOutput: too many produced outputs")
	}
	txb.ProducedOutputs = append(txb.ProducedOutputs, o)
	return txb.TxBuilder.ProduceOutput(o.Bytes()), nil
}

// ReplaceProducedOutput overwrites the produced output at idx in both
// the typed mirror and the wire-format byte slice. Used by the
// sequencer's chain-output post-processing.
func (txb *SeqTxBuilder) ReplaceProducedOutput(idx byte, o *ledger.Output) {
	txb.ProducedOutputs[idx] = o
	txb.TxData.OutputBytes[idx] = o.Bytes()
}

// LoadInputBytes returns the raw bytes of the i-th consumed output —
// the loader shape expected by transaction.ParseAndValidate.
func (txb *SeqTxBuilder) LoadInputBytes(i byte) ([]byte, error) {
	if int(i) >= len(txb.ConsumedOutputs) {
		return nil, fmt.Errorf("SeqTxBuilder.LoadInputBytes: can't load input #%d", i)
	}
	return txb.ConsumedOutputs[i].Bytes(), nil
}
