package txbuildercore

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"golang.org/x/crypto/blake2b"
)

// HashEssence hashes the transaction tree skipping the signature slot.
// The result is the input to the transaction-ID derivation (which then
// overlays a 5-byte timestamp prefix and a 1-byte produced-outputs
// count). This is the byte-identical algorithm the validator uses.
func HashEssence(txTree *tuples.Tree) ([32]byte, error) {
	hasher, _ := blake2b.New256(nil) // never errors for nil key
	for i := byte(0); i < TxTreeTupleNumElements; i++ {
		if i == TxSignatureData {
			continue
		}
		d, err := txTree.BytesAtPath([]byte{i})
		if err != nil {
			return [32]byte{}, err
		}
		_, _ = hasher.Write(d)
	}
	var ret [32]byte
	copy(ret[:], hasher.Sum(nil))
	return ret, nil
}

// TxVersionSignerBoundID is the first transaction version whose ID commits to the
// signer and to the boundaries of its elements (kb/library_upgrade6_mining.md).
const TxVersionSignerBoundID = 6

// hashEssenceSignerBound is the essence from TxVersionSignerBoundID on: the signer's
// holder ID, then every element but the signature data, each preceded by its length.
// The lengths fix where one element ends and the next begins, so one ID has one
// encoding; the signature data stays outside, its holder ID inside.
func hashEssenceSignerBound(txTree *tuples.Tree, signer base.HolderID) ([32]byte, error) {
	preimage, err := SignerBoundPreimage(txTree, signer)
	if err != nil {
		return [32]byte{}, err
	}
	return blake2b.Sum256(preimage), nil
}

// SignerBoundPreimage is the byte string hashEssenceSignerBound hashes. A miner keeps
// the hash state of everything before its nonce, which the transaction-level
// constraints, the last element, end with.
func SignerBoundPreimage(txTree *tuples.Tree, signer base.HolderID) ([]byte, error) {
	ret := append([]byte{}, signer[:]...)
	for i := byte(0); i < TxTreeTupleNumElements; i++ {
		if i == TxSignatureData {
			continue
		}
		d, err := txTree.BytesAtPath([]byte{i})
		if err != nil {
			return nil, err
		}
		ret = binary.BigEndian.AppendUint32(ret, uint32(len(d)))
		ret = append(ret, d...)
	}
	return ret, nil
}

// txVersionOf reads the transaction version of the tree.
func txVersionOf(txTree *tuples.Tree) (uint16, error) {
	v, err := txTree.BytesAtPath([]byte{TxVersion})
	if err != nil {
		return 0, err
	}
	if len(v) != 2 {
		return 0, fmt.Errorf("transaction version must be 2 bytes, got %d", len(v))
	}
	return binary.BigEndian.Uint16(v), nil
}

// TxIDFromTree derives the transaction ID from a parsed transaction
// tree. The byte layout is:
//
//	[0:5]  ledger timestamp (5 bytes, with sequencer bit set on tick
//	       byte if the tx is a sequencer tx)
//	[5]    (number of produced outputs - 1)  (so 1..256 outputs fit
//	       in one byte)
//	[6:32] hash-essence bytes 6..31
//
// The function validates the timestamp parses and the produced-outputs
// count is in (0, 256]. It does NOT run any constraint script — this
// is a structural derivation that matches the wasm wallet's view.
//
// From TxVersionSignerBoundID on the essence also commits to the signer,
// whose holder ID is taken from the signature data, which must then be
// in its type's exact encoding.
func TxIDFromTree(txTree *tuples.Tree) (base.TransactionID, error) {
	return txIDFromTree(txTree, nil)
}

// txIDFromTree is TxIDFromTree with the signer given, as when signing: the
// signature data does not exist yet. A nil signer is read from the tree.
func txIDFromTree(txTree *tuples.Tree, signer *base.HolderID) (base.TransactionID, error) {
	var ret base.TransactionID

	tsBin, err := txTree.BytesAtPath([]byte{TxTimestamp})
	if err != nil {
		return ret, err
	}
	if _, err = base.LedgerTimeFromBytes(tsBin); err != nil {
		return ret, err
	}

	// Sequencer-tx flag: non-empty sequencer-data slot means the tx
	// produced at least one sequencer output. We don't need to parse
	// the slot — its presence is the signal.
	seqBin, err := txTree.BytesAtPath([]byte{TxSequencerDataBytes})
	if err != nil {
		return ret, err
	}
	isSeqTx := len(seqBin) > 0

	version, err := txVersionOf(txTree)
	if err != nil {
		return ret, err
	}
	if version < TxVersionSignerBoundID {
		ret, err = HashEssence(txTree)
	} else {
		if signer == nil {
			sigBin, err := txTree.BytesAtPath([]byte{TxSignatureData})
			if err != nil {
				return ret, err
			}
			sig, err := base.SignatureFromBytes(sigBin)
			if err != nil {
				return ret, err
			}
			h := sig.HolderID()
			signer = &h
		}
		ret, err = hashEssenceSignerBound(txTree, *signer)
	}
	if err != nil {
		return ret, err
	}
	copy(ret[:], tsBin)
	if isSeqTx {
		ret[base.TickByteIndex] |= base.SequencerBitMaskInTick
	}

	nUTXO, err := txTree.NumElementsAtPath([]byte{TxOutputs})
	if err != nil {
		return ret, err
	}
	if nUTXO == 0 || nUTXO > 256 {
		return ret, errors.New("wrong number of produced outputs")
	}
	ret[base.LedgerTimeByteLength] = byte(nUTXO - 1)
	return ret, nil
}

// TxIDFromBytes is the byte-input form of TxIDFromTree. The input is
// the raw transaction tuple as emitted by SerializeRawTx / TxBuilder.Bytes
// (NOT the 2-element outer wrapper that combines the tx tuple with
// the consumed-outputs tree on the server side).
func TxIDFromBytes(txBytes []byte) (base.TransactionID, error) {
	tree, err := tuples.TreeFromBytesReadOnly(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	return TxIDFromTree(tree)
}

// SignED25519 derives the tx ID from the current builder state,
// signs it with privKey, and writes the canonical signature-data
// bytes (sigType || sig || pubKey) into TxData.SignatureData.
//
// Call after all ConsumeOutput / ProduceOutput / PutUnlock* are done
// and after ComputeInputCommitment. Modifying the builder after this
// invalidates the signature.
func (txb *TxBuilder) SignED25519(privKey ed25519.PrivateKey) {
	tree := txb.ToTuple().AsTree()
	signer := base.HolderIDFromED25519PrivateKey(privKey)
	txid, err := txIDFromTree(tree, &signer)
	if err != nil {
		panic(err)
	}
	txb.TxData.SignatureData = base.SignatureDataED25519(privKey, txid[:])
}
