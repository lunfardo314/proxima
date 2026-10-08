package txbuildercore

// The library commitment proof: everything a wallet needs to check that the
// library it compiles with is the one the ledger commits to, with no trust in
// the node that served it. See kb/library_proof.md.
//
// The chain of custody, every arrow a hash or a byte parse the wallet does
// itself: branch ID -> branch bytes -> baseline root (stem output, oracle
// tuple) -> Merkle proof -> upgrade UTXO bytes -> library hash -> the library
// built from the JSON. The only input it cannot derive is the branch ID.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/models/trie_blake2b"
	"github.com/lunfardo314/unitrie/models/trie_blake2b/trie_blake2b_verify"
)

// TriePartitionLedgerState is the first byte of every ledger-state trie key:
// a UTXO is keyed by this byte followed by its output ID. The wallet needs it
// to name the key its proof must be about.
const TriePartitionLedgerState = byte(0)

// The ledger state is a hexary trie committed with blake2b hashes of 24 bytes;
// the wallet needs the same model to compute the terminal commitment of the
// value a proof is about. ledger.CommitmentModel is this value.
const (
	TrieArity    = common.PathArity16
	TrieHashSize = trie_blake2b.HashSize192
)

var StateCommitmentModel = trie_blake2b.New(TrieArity, TrieHashSize)

// Stem output element 3 holds the oracle tuple; its fifth element is the trie
// root of the branch's baseline, the predecessor branch.
const (
	stemOracleElementIndex   = 3
	oracleBaselineRootIndex  = 4
	upgradeUTXOLibraryHash   = 3
	upgradeUTXOPrevHash      = 4
	upgradeUTXOPrevSlot      = 5
	upgradeUTXOMinNumElement = 6
)

// UpgradeUTXOView is the byte parse of an upgrade commitment UTXO: elements 3,
// 4 and 5 are inline data holding the library hash, the previous library hash
// and the previous upgrade slot.
type UpgradeUTXOView struct {
	LibraryHash     [32]byte
	PrevLibraryHash [32]byte
	PrevUpgradeSlot uint32
}

// ParseUpgradeUTXO parses an output as an upgrade commitment UTXO.
func ParseUpgradeUTXO(o *Output) (*UpgradeUTXOView, error) {
	if o.NumElements() < upgradeUTXOMinNumElement {
		return nil, fmt.Errorf("upgrade UTXO must have at least %d UTXO elements, got %d", upgradeUTXOMinNumElement, o.NumElements())
	}
	amountsBin, err := o.At(0)
	if err != nil {
		return nil, err
	}
	amounts, err := DecodeAmountsVector(amountsBin)
	if err != nil || (len(amounts) > 0 && amounts[0] != 0) {
		return nil, errors.New("upgrade UTXO must have 0 token balance")
	}
	rawHash := easyfl.StripDataPrefix(o.MustAt(upgradeUTXOLibraryHash))
	if len(rawHash) != 32 {
		return nil, fmt.Errorf("upgrade UTXO library hash must be 32 bytes, got %d", len(rawHash))
	}
	rawPrevHash := easyfl.StripDataPrefix(o.MustAt(upgradeUTXOPrevHash))
	if len(rawPrevHash) != 32 {
		return nil, fmt.Errorf("upgrade UTXO previous library hash must be 32 bytes, got %d", len(rawPrevHash))
	}
	rawPrevSlot := easyfl.StripDataPrefix(o.MustAt(upgradeUTXOPrevSlot))
	if len(rawPrevSlot) != 4 {
		return nil, fmt.Errorf("upgrade UTXO previous slot must be 4 bytes, got %d", len(rawPrevSlot))
	}
	ret := &UpgradeUTXOView{PrevUpgradeSlot: binary.BigEndian.Uint32(rawPrevSlot)}
	copy(ret.LibraryHash[:], rawHash)
	copy(ret.PrevLibraryHash[:], rawPrevHash)
	return ret, nil
}

// OracleElementsFromBytes splits the inline-data literal at stem output
// element 3 into the raw elements of the oracle tuple. Elements are raw
// values without inline-data prefix; a short tuple is returned as is, so an
// absent trailing element reads as nil.
func OracleElementsFromBytes(data []byte) ([][]byte, error) {
	payload := easyfl.StripDataPrefix(data)
	if len(payload) == 0 {
		return nil, errors.New("oracle data: empty")
	}
	t, err := tuples.TupleFromBytes(payload, 256)
	if err != nil {
		return nil, fmt.Errorf("oracle data: %w", err)
	}
	ret := make([][]byte, 0, t.NumElements())
	t.ForEach(func(_ int, v []byte) bool {
		ret = append(ret, v)
		return true
	})
	return ret, nil
}

// StemBaselineRoot returns the baseline trie root a branch's stem output
// carries: the state root of the branch's predecessor.
func StemBaselineRoot(stem *Output) ([]byte, error) {
	bin, err := stem.At(stemOracleElementIndex)
	if err != nil {
		return nil, fmt.Errorf("stem output: %w", err)
	}
	elems, err := OracleElementsFromBytes(bin)
	if err != nil {
		return nil, fmt.Errorf("stem output: %w", err)
	}
	if len(elems) <= oracleBaselineRootIndex || len(elems[oracleBaselineRootIndex]) == 0 {
		return nil, errors.New("stem output: no baseline root in the oracle data")
	}
	return elems[oracleBaselineRootIndex], nil
}

// LibraryCommitment is what the node returns with a library so the wallet can
// prove the library is committed by the baseline state of a branch.
type LibraryCommitment struct {
	BranchID         base.TransactionID
	BranchTxBytes    []byte
	UpgradeUTXOBytes []byte
	Proof            []byte
}

// LibraryCommitmentJSON is the wire form of LibraryCommitment: hex strings, or
// Error when the node could not produce the proof.
type LibraryCommitmentJSON struct {
	BranchID         string `json:"branch_id,omitempty"`
	BranchTxBytes    string `json:"branch_tx_bytes,omitempty"`
	UpgradeUTXOBytes string `json:"upgrade_utxo_bytes,omitempty"`
	Proof            string `json:"proof,omitempty"`
	Error            string `json:"error,omitempty"`
}

// LedgerDefinitionJSON is the body of the node's ledger definition response:
// one library of the upgrade chain with its commitment proof. The API server
// embeds it; the wasm wallet parses it.
type LedgerDefinitionJSON struct {
	UpgradeSlot     uint32                 `json:"upgrade_slot"`
	LibraryJSON     string                 `json:"library_json"`
	LibraryHash     string                 `json:"library_hash"`
	PrevLibraryHash string                 `json:"prev_library_hash"`
	PrevUpgradeSlot uint32                 `json:"prev_upgrade_slot"`
	Commitment      *LibraryCommitmentJSON `json:"commitment,omitempty"`
}

func (c *LibraryCommitment) JSONAble() *LibraryCommitmentJSON {
	return &LibraryCommitmentJSON{
		BranchID:         c.BranchID.StringHex(),
		BranchTxBytes:    hex.EncodeToString(c.BranchTxBytes),
		UpgradeUTXOBytes: hex.EncodeToString(c.UpgradeUTXOBytes),
		Proof:            hex.EncodeToString(c.Proof),
	}
}

func (j *LibraryCommitmentJSON) Parse() (*LibraryCommitment, error) {
	if j.Error != "" {
		return nil, fmt.Errorf("node could not prove the library commitment: %s", j.Error)
	}
	ret := &LibraryCommitment{}
	var err error
	if ret.BranchID, err = base.TransactionIDFromHexString(j.BranchID); err != nil {
		return nil, fmt.Errorf("library commitment: branch_id: %w", err)
	}
	if ret.BranchTxBytes, err = hex.DecodeString(j.BranchTxBytes); err != nil {
		return nil, fmt.Errorf("library commitment: branch_tx_bytes: %w", err)
	}
	if ret.UpgradeUTXOBytes, err = hex.DecodeString(j.UpgradeUTXOBytes); err != nil {
		return nil, fmt.Errorf("library commitment: upgrade_utxo_bytes: %w", err)
	}
	if ret.Proof, err = hex.DecodeString(j.Proof); err != nil {
		return nil, fmt.Errorf("library commitment: proof: %w", err)
	}
	return ret, nil
}

// VerifyLibraryCommitment checks that the baseline state of the branch named
// by the commitment holds an upgrade UTXO for upgradeSlot with exactly the
// expected content. The caller fills expected.LibraryHash with the hash it
// computed from the library it was handed, never with a hash the node named.
func VerifyLibraryCommitment(c *LibraryCommitment, upgradeSlot uint32, expected UpgradeUTXOView) error {
	// 1. the branch ID binds the branch bytes
	tree, err := tuples.TreeFromBytesReadOnly(c.BranchTxBytes)
	if err != nil {
		return fmt.Errorf("library commitment: branch bytes: %w", err)
	}
	txid, err := TxIDFromTree(tree)
	if err != nil {
		return fmt.Errorf("library commitment: branch bytes: %w", err)
	}
	if txid != c.BranchID {
		return fmt.Errorf("library commitment: branch bytes hash to %s, not to the named branch %s", txid.StringShort(), c.BranchID.StringShort())
	}
	if !txid.IsBranchTransaction() {
		return fmt.Errorf("library commitment: %s is not a branch transaction", txid.StringShort())
	}
	// 2. the branch binds the root of its baseline state
	seqData, err := tree.BytesAtPath([]byte{TxSequencerDataBytes})
	if err != nil || len(seqData) != SequencerDataLen {
		return errors.New("library commitment: branch has no sequencer data")
	}
	stemBytes, err := tree.BytesAtPath([]byte{TxOutputs, seqData[1]})
	if err != nil {
		return fmt.Errorf("library commitment: stem output: %w", err)
	}
	stem, err := OutputFromBytes(stemBytes)
	if err != nil {
		return fmt.Errorf("library commitment: stem output: %w", err)
	}
	root, err := StemBaselineRoot(stem)
	if err != nil {
		return fmt.Errorf("library commitment: %w", err)
	}
	// 3. the root binds the upgrade UTXO bytes under the expected key
	proof, err := trie_blake2b.ProofFromBytes(c.Proof)
	if err != nil {
		return fmt.Errorf("library commitment: proof: %w", err)
	}
	if proof.PathArity != TrieArity || proof.HashSize != TrieHashSize {
		return errors.New("library commitment: proof is not over the ledger state's commitment model")
	}
	oid := base.UpgradeOutputID(upgradeSlot)
	key := common.UnpackBytes(append([]byte{TriePartitionLedgerState}, oid[:]...), TrieArity)
	if !bytes.Equal(proof.Key, key) {
		return fmt.Errorf("library commitment: proof is about another key than the upgrade UTXO of slot %d", upgradeSlot)
	}
	// the verifier compares against the terminal commitment of the value, not the value
	terminal := StateCommitmentModel.CommitToData(c.UpgradeUTXOBytes)
	if terminal == nil {
		return errors.New("library commitment: empty upgrade UTXO bytes")
	}
	if err = trie_blake2b_verify.ValidateWithTerminal(proof, root, terminal.Bytes()); err != nil {
		return fmt.Errorf("library commitment: proof does not bind the upgrade UTXO to the baseline root: %w", err)
	}
	// 4. the upgrade UTXO binds the library hash and its predecessor
	utxo, err := OutputFromBytes(c.UpgradeUTXOBytes)
	if err != nil {
		return fmt.Errorf("library commitment: upgrade UTXO: %w", err)
	}
	view, err := ParseUpgradeUTXO(utxo)
	if err != nil {
		return fmt.Errorf("library commitment: upgrade UTXO: %w", err)
	}
	if *view != expected {
		return fmt.Errorf("library commitment: the ledger commits to library %s at slot %d (previous %s at slot %d), the node served %s (previous %s at slot %d)",
			hex.EncodeToString(view.LibraryHash[:]), upgradeSlot, hex.EncodeToString(view.PrevLibraryHash[:]), view.PrevUpgradeSlot,
			hex.EncodeToString(expected.LibraryHash[:]), hex.EncodeToString(expected.PrevLibraryHash[:]), expected.PrevUpgradeSlot)
	}
	return nil
}

// LibraryFromLedgerDefinition builds the wallet library from a ledger
// definition and verifies it against the commitment proof the definition
// carries. The hash compared is computed from the library built here; the
// hash fields of the response are never trusted. A definition without a
// commitment is refused.
func LibraryFromLedgerDefinition(def *LedgerDefinitionJSON) (*Library[any], *LibraryCommitment, error) {
	desc, err := easyfl.ReadLibraryFromJSON([]byte(def.LibraryJSON))
	if err != nil {
		return nil, nil, fmt.Errorf("parse library JSON: %w", err)
	}
	lib, err := NewLibrary(desc)
	if err != nil {
		return nil, nil, fmt.Errorf("build library: %w", err)
	}
	if def.Commitment == nil {
		return nil, nil, errors.New("the node does not provide the library commitment proof")
	}
	c, err := def.Commitment.Parse()
	if err != nil {
		return nil, nil, err
	}
	expected := UpgradeUTXOView{LibraryHash: lib.LibraryHash(), PrevUpgradeSlot: def.PrevUpgradeSlot}
	prev, err := hex.DecodeString(def.PrevLibraryHash)
	if err != nil || len(prev) != 32 {
		return nil, nil, errors.New("ledger definition: malformed prev_library_hash")
	}
	copy(expected.PrevLibraryHash[:], prev)
	if err = VerifyLibraryCommitment(c, def.UpgradeSlot, expected); err != nil {
		return nil, nil, err
	}
	return lib, c, nil
}
