package txcore

import (
	"crypto"
	"crypto/ed25519"
	"errors"
	"math/rand"
	"sync"
	"time"

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
func TxIDFromTree(txTree *tuples.Tree) (base.TransactionID, error) {
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

	if ret, err = HashEssence(txTree); err != nil {
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

// signRand is a process-global source for ed25519's optional reader.
// ed25519 ignores the reader (signatures are deterministic), but the
// crypto.Signer interface requires one; we share one source across
// SignED25519 calls so we don't allocate per signature.
var (
	signRandOnce sync.Once
	signRand     *rand.Rand
)

func ed25519RandSource() *rand.Rand {
	signRandOnce.Do(func() {
		signRand = rand.New(rand.NewSource(time.Now().UnixNano()))
	})
	return signRand
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
	txid, err := TxIDFromTree(tree)
	if err != nil {
		panic(err)
	}
	sig, err := privKey.Sign(ed25519RandSource(), txid[:], crypto.Hash(0))
	if err != nil {
		panic(err)
	}
	pubKey := privKey.Public().(ed25519.PublicKey)
	// Wire format: <sig type byte> | <signature proper> | <public key>.
	sd := make([]byte, 0, 1+len(sig)+len(pubKey))
	sd = append(sd, base.SignatureTypeED25519)
	sd = append(sd, sig...)
	sd = append(sd, pubKey...)
	txb.TxData.SignatureData = sd
}
