package txinput_queue

import (
	"fmt"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// checkCanonicalEncoding refuses a transaction whose elements are not encoded as every
// builder writes them. The transaction ID does not fix every byte of the encoding, so
// another encoding of the same ID must not be persisted or gossiped before validation.
// Node policy: every transaction an honest builder makes passes.
func checkCanonicalEncoding(tx *transaction.Transaction) error {
	exactLen := func(path []byte, name string, lengths ...int) error {
		b, err := tx.BytesAtPath(path)
		if err != nil {
			return err
		}
		for _, l := range lengths {
			if len(b) == l {
				return nil
			}
		}
		return fmt.Errorf("non-canonical %s: %d bytes", name, len(b))
	}
	if err := exactLen(ledger.PathToTxVersion, "version", 2); err != nil {
		return err
	}
	if err := exactLen(ledger.PathToTimestamp, "timestamp", base.LedgerTimeByteLength); err != nil {
		return err
	}
	if err := exactLen(ledger.PathToSequencerDataBytes, "sequencer data", 0, txbuildercore.SequencerDataLen); err != nil {
		return err
	}
	if err := exactLen(ledger.PathToInputCommitment, "input commitment", 32); err != nil {
		return err
	}
	if err := exactLen(ledger.PathToExplicitBaseline, "explicit baseline", 0, base.TransactionIDLength); err != nil {
		return err
	}
	sig, err := tx.BytesAtPath(ledger.PathToSignature)
	if err != nil {
		return err
	}
	if _, err = base.SignatureFromBytes(sig); err != nil {
		return fmt.Errorf("non-canonical signature data: %v", err)
	}
	// these four are always written as tuples, an empty one as its 2-byte header
	for _, e := range []struct {
		path []byte
		name string
	}{
		{ledger.PathToInputIDs, "input IDs"},
		{ledger.PathToUnlockParams, "unlock data"},
		{ledger.PathToProducedOutputs, "outputs"},
		{ledger.PathToEndorsements, "endorsements"},
	} {
		b, err := tx.BytesAtPath(e.path)
		if err != nil {
			return err
		}
		if err = canonicalTuple(b, e.name, false); err != nil {
			return err
		}
	}
	// transaction-level constraints are left out when there are none
	b, err := tx.BytesAtPath(ledger.PathToTxConstraints)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return canonicalTuple(b, "transaction constraints", true)
}

// canonicalTuple requires a tuple element to be written as the encoder writes it: one
// whose length code says all its elements are empty is exactly its 2-byte header.
// nonEmpty requires at least one element.
func canonicalTuple(b []byte, name string, nonEmpty bool) error {
	t, err := tuples.TupleFromBytes(b)
	if err != nil {
		return fmt.Errorf("non-canonical %s: %v", name, err)
	}
	if nonEmpty && t.NumElements() == 0 {
		return fmt.Errorf("non-canonical %s: an empty tuple is written as no bytes", name)
	}
	allEmpty := len(b) >= 2 && b[0]&0xc0 == 0 // the length code in the top 2 bits of the header
	if allEmpty && len(b) != 2 {
		return fmt.Errorf("non-canonical %s: %d bytes after a header with no element data", name, len(b)-2)
	}
	return nil
}
