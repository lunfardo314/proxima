package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	// 9 args: predOutputID, vrfProof, totalSupply, totalCoverage, coverageDelta,
	// frozenCoverage, slotInflation, numTransactions, baselineRoot.
	stemTemplate = StemLockName + "(0x%s,0x%s,z64/%d,z64/%d,z64/%d,z64/%d,z64/%d,z32/%d,0x%s)"

	// StemLockNumArgs is the on-bytecode arity of the stemLock constraint.
	StemLockNumArgs = 9
)

type (
	// StemLock is the lock constraint of the branch stem output. It carries the
	// global ledger state aggregates that are part of the trie-committed UTXO
	// state (see metadata-refactor plan §3).
	StemLock struct {
		PredecessorOutputID base.OutputID
		VRFProof            []byte
		// Aggregates over the branch's past cone. Verified on-chain via the
		// stemLock constraint recurrences (see lock_stem.easyfl).
		TotalSupply     uint64
		TotalCoverage   uint64
		CoverageDelta   uint64
		FrozenCoverage  uint64
		SlotInflation   uint64
		NumConfirmedTransactions uint32
		// Predecessor branch's trie root (int(TrieHashSize) bytes). All-zero at genesis.
		BaselineRoot []byte
	}
)

//go:embed def/lock_stem.easyfl
var stemLockSource string

// StemAccountID is the placeholder index value (single zero byte) used
// for stem outputs in the controllers index. The stem index-value tuple
// is ([0x00]).
var StemAccountID = []byte{0}

func (st *StemLock) Name() string {
	return StemLockName
}

// Source returns the EasyFL source representation for the stemLock
// constraint with all 9 args inlined — used for compilation to bytecode.
func (st *StemLock) Source() string {
	baselineRoot := st.BaselineRoot
	if len(baselineRoot) == 0 {
		// Genesis stems may leave BaselineRoot unset — encode it as int(TrieHashSize) zero bytes.
		baselineRoot = make([]byte, int(TrieHashSize))
	}
	return fmt.Sprintf(stemTemplate,
		hex.EncodeToString(st.PredecessorOutputID[:]),
		hex.EncodeToString(st.VRFProof),
		st.TotalSupply,
		st.TotalCoverage,
		st.CoverageDelta,
		st.FrozenCoverage,
		st.SlotInflation,
		st.NumConfirmedTransactions,
		hex.EncodeToString(baselineRoot),
	)
}

// Bytes returns the compiled bytecode of the stemLock constraint,
// suitable for placement at output element index 2 of a stem output.
// Stem is the only lock kind whose bytecode at index 2 carries data
// (the 9 args); for sig/chain/tag the index-2 bytecode is a per-kind
// constant (see SigLockBytecode / ChainLockBytecode / TagAlongBytecode).
func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.Source())
}

func (st *StemLock) String() string {
	return st.Source()
}

// IndexValues returns the placeholder ([0x00]) — stem outputs are not
// indexable by controller; the single byte mirrors the historical
// StemAccountID marker for trie partition lookup.
func (st *StemLock) IndexValues() [][]byte {
	return [][]byte{{0}}
}

// LockBytecode returns the compiled stemLock bytecode with all 9 args
// inlined. Stem is the only lock kind whose bytecode at output index 2
// carries data.
func (st *StemLock) LockBytecode() []byte {
	return st.Bytes()
}

func registerStemLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(StemLockName, StemLockNumArgs, func(data []byte) (Constraint, error) {
		return StemLockFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		txid := base.RandomTransactionID(true, 1)
		predID := base.MustNewOutputID(txid, byte(txid.NumProducedOutputs()-1))
		example := StemLock{
			PredecessorOutputID: predID,
			VRFProof:            []byte{0x01, 0x02, 0x03},
			TotalSupply:         1_000_000,
			TotalCoverage:       500_000,
			CoverageDelta:       100_000,
			FrozenCoverage:      10_000,
			SlotInflation:       1_000,
			NumConfirmedTransactions:     42,
			BaselineRoot:        bytes.Repeat([]byte{0x55}, int(TrieHashSize)),
		}
		exampleBack, err := StemLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(example.Bytes(), exampleBack.Bytes()), "bytes.Equal(example.Bytes(), exampleBack.Bytes())")
		util.Assertf(example.TotalSupply == exampleBack.TotalSupply, "TotalSupply roundtrip")
		util.Assertf(example.NumConfirmedTransactions == exampleBack.NumConfirmedTransactions, "NumConfirmedTransactions roundtrip")
		util.Assertf(bytes.Equal(example.BaselineRoot, exampleBack.BaselineRoot), "BaselineRoot roundtrip")
		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}

// StemLockFromBytesWithLib parses a StemLock using the provided library.
func StemLockFromBytesWithLib(data []byte, lib *Library) (*StemLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, StemLockNumArgs)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	oid, err := base.OutputIDFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}
	ret := &StemLock{
		PredecessorOutputID: oid,
		VRFProof:            easyfl.StripDataPrefix(args[1]),
	}
	// $2..$6 — z64-encoded uint64; empty bytes mean zero.
	if ret.TotalSupply, err = decodeOptionalUint64(args[2]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: TotalSupply: %w", err)
	}
	if ret.TotalCoverage, err = decodeOptionalUint64(args[3]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: TotalCoverage: %w", err)
	}
	if ret.CoverageDelta, err = decodeOptionalUint64(args[4]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: CoverageDelta: %w", err)
	}
	if ret.FrozenCoverage, err = decodeOptionalUint64(args[5]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: FrozenCoverage: %w", err)
	}
	if ret.SlotInflation, err = decodeOptionalUint64(args[6]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: SlotInflation: %w", err)
	}
	// $7 — z32-encoded uint32; empty bytes mean zero.
	if ret.NumConfirmedTransactions, err = decodeOptionalUint32(args[7]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: NumConfirmedTransactions: %w", err)
	}
	// $8 — fixed-width 24-byte trie root.
	baselineRoot := easyfl.StripDataPrefix(args[8])
	if len(baselineRoot) != int(TrieHashSize) {
		return nil, fmt.Errorf("StemLockFromBytes: BaselineRoot must be %d bytes, got %d", int(TrieHashSize), len(baselineRoot))
	}
	ret.BaselineRoot = append([]byte(nil), baselineRoot...)
	return ret, nil
}

// decodeOptionalUint64 decodes a z64-encoded uint64; empty bytes ⇒ 0.
func decodeOptionalUint64(arg []byte) (uint64, error) {
	b := easyfl.StripDataPrefix(arg)
	if len(b) == 0 {
		return 0, nil
	}
	return easyfl_util.Uint64FromBytes(b)
}

// decodeOptionalUint32 decodes a z32-encoded uint32; empty bytes ⇒ 0.
func decodeOptionalUint32(arg []byte) (uint32, error) {
	b := easyfl.StripDataPrefix(arg)
	if len(b) == 0 {
		return 0, nil
	}
	return easyfl_util.Uint32FromBytes(b)
}
