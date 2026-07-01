package ledger

import (
	"encoding/hex"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	// 5 args: predOutputID, vrfProof, totalSupply, totalCoverage, slotInflation.
	// All five are constrained by the stemLock recurrences (supply and
	// total-coverage halving) or pinned at genesis. coverageDelta moved off the
	// stem onto the branch's sequencer constraint (the recurrence reads it from
	// there). The remaining, purely informational, deterministic aggregates live
	// in the OracleData tuple at output index 3 (see OracleData below).
	stemTemplate = StemLockName + "(0x%s,0x%s,z64/%d,z64/%d,z64/%d)"

	// StemLockNumArgs is the on-bytecode arity of the stemLock constraint.
	StemLockNumArgs = 5
)

type (
	// StemLock is the lock constraint of the branch stem output. It carries the
	// global ledger-state aggregates that the stemLock EasyFL body verifies via
	// recurrences (supply, total-coverage halving) or pins at genesis.
	StemLock struct {
		PredecessorOutputID base.OutputID
		VRFProof            []byte
		// Aggregates over the branch's past cone, verified on-chain via the
		// stemLock constraint recurrences (see lock_stem.easyfl). coverageDelta
		// is no longer here — it lives on the branch's sequencer constraint.
		TotalSupply   uint64
		TotalCoverage uint64
		SlotInflation uint64
	}

	// OracleData is the unconstrained, deterministic consensus data of the branch
	// stem output. It is stored as a single inline-data literal at output index 3
	// (ConstraintIndexChain — the stem has no chain constraint, so that slot is
	// free) holding a serialized tuple of values. Unlike a registered constraint
	// it carries no EasyFL logic: when evaluated it returns its own (non-empty)
	// payload, which is truthy, so the branch transaction validates. Its values
	// are committed to the trie (they are part of the stem output bytes), so a
	// node computing them differently produces a different branch root — that is
	// how determinism is "verified". They are interpreted only outside the
	// ledger.
	//
	// The tuple is read element-by-index, with absent elements decoded as zero.
	// New deterministic aggregates can therefore be appended in a future ledger
	// version without changing any EasyFL arity: old readers ignore the extras,
	// new readers read them.
	//
	// Tuple layout:
	//   0: frozenCoverage           (z64)
	//   1: numConfirmedTransactions (z64)
	//   2: numSeqTransactions       (z64)
	//   3: numSeq                   (z64)
	//   4: baselineRoot             (TrieHashSize bytes; all-zero at genesis)
	OracleData struct {
		// FrozenCoverage: cumulative total of tokens frozen by delegations across
		// all sequencers (state invariant, <= totalSupply).
		FrozenCoverage uint64
		// NumConfirmedTransactions: new tx count in the branch's past cone.
		NumConfirmedTransactions uint32
		// NumSeqTransactions: new sequencer-transaction count in the branch's slot.
		NumSeqTransactions uint32
		// NumSeq: number of distinct sequencers active in the branch's slot.
		NumSeq uint32
		// BaselineRoot: predecessor branch's trie root (TrieHashSize bytes).
		// All-zero at genesis.
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
// constraint with all 5 args inlined — used for compilation to bytecode.
func (st *StemLock) Source() string {
	return fmt.Sprintf(stemTemplate,
		hex.EncodeToString(st.PredecessorOutputID[:]),
		hex.EncodeToString(st.VRFProof),
		st.TotalSupply,
		st.TotalCoverage,
		st.SlotInflation,
	)
}

// Bytes returns the compiled bytecode of the stemLock constraint,
// suitable for placement at output element index 2 of a stem output.
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

// LockBytecode returns the compiled stemLock bytecode with all 5 args inlined.
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
			SlotInflation:       1_000,
		}
		exampleBack, err := StemLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(example.TotalSupply == exampleBack.TotalSupply, "TotalSupply roundtrip")
		util.Assertf(example.TotalCoverage == exampleBack.TotalCoverage, "TotalCoverage roundtrip")
		util.Assertf(example.SlotInflation == exampleBack.SlotInflation, "SlotInflation roundtrip")
		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)

		// OracleData inline-data tuple round-trip.
		sd := OracleData{
			FrozenCoverage:           10_000,
			NumConfirmedTransactions: 42,
			NumSeqTransactions:       7,
			NumSeq:                   3,
			BaselineRoot:            make([]byte, TrieHashSize),
		}
		for i := range sd.BaselineRoot {
			sd.BaselineRoot[i] = 0x55
		}
		sdBack, err := OracleDataFromBytes(sd.Bytes())
		util.AssertNoError(err)
		util.Assertf(sd.FrozenCoverage == sdBack.FrozenCoverage, "OracleData FrozenCoverage roundtrip")
		util.Assertf(sd.NumConfirmedTransactions == sdBack.NumConfirmedTransactions, "OracleData NumConfirmedTransactions roundtrip")
		util.Assertf(sd.NumSeqTransactions == sdBack.NumSeqTransactions, "OracleData NumSeqTransactions roundtrip")
		util.Assertf(sd.NumSeq == sdBack.NumSeq, "OracleData NumSeq roundtrip")
		util.Assertf(len(sdBack.BaselineRoot) == int(TrieHashSize), "OracleData BaselineRoot roundtrip")
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
	// $2..$4 — z64-encoded uint64; empty bytes mean zero.
	if ret.TotalSupply, err = decodeOptionalUint64(args[2]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: TotalSupply: %w", err)
	}
	if ret.TotalCoverage, err = decodeOptionalUint64(args[3]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: TotalCoverage: %w", err)
	}
	if ret.SlotInflation, err = decodeOptionalUint64(args[4]); err != nil {
		return nil, fmt.Errorf("StemLockFromBytes: SlotInflation: %w", err)
	}
	return ret, nil
}

// Bytes returns the inline-data-literal bytecode of the OracleData tuple,
// suitable for placement at output element index 3 (ConstraintIndexChain)
// of a stem output. When evaluated during validation the literal returns
// its (non-empty) payload, which is truthy.
func (d *OracleData) Bytes() []byte {
	return mustBinFromSource("0x" + hex.EncodeToString(d.tupleBytes()))
}

// tupleBytes serializes the OracleData values into the wire-form tuple.
func (d *OracleData) tupleBytes() []byte {
	baselineRoot := d.BaselineRoot
	if len(baselineRoot) == 0 {
		baselineRoot = make([]byte, int(TrieHashSize))
	}
	t := tuples.EmptyTupleEditable(256)
	t.MustPush(easyfl_util.TrimmedLeadingZeroUint64(d.FrozenCoverage))
	t.MustPush(easyfl_util.TrimmedLeadingZeroUint32(d.NumConfirmedTransactions))
	t.MustPush(easyfl_util.TrimmedLeadingZeroUint32(d.NumSeqTransactions))
	t.MustPush(easyfl_util.TrimmedLeadingZeroUint32(d.NumSeq))
	t.MustPush(baselineRoot)
	return t.Tuple().Bytes()
}

func (d *OracleData) String() string {
	return fmt.Sprintf("oracleData(frozenCoverage=%s, numTx=%d, numSeqTx=%d, numSeq=%d, baselineRoot=0x%s)",
		util.Th(d.FrozenCoverage), d.NumConfirmedTransactions, d.NumSeqTransactions, d.NumSeq,
		hex.EncodeToString(d.BaselineRoot))
}

// OracleDataFromBytes parses the inline-data-literal bytecode at stem output
// index 3 into a OracleData. Absent tuple elements decode as zero so future
// appended aggregates remain backward-readable.
func OracleDataFromBytes(data []byte) (*OracleData, error) {
	payload := easyfl.StripDataPrefix(data)
	if len(payload) == 0 {
		return nil, fmt.Errorf("OracleDataFromBytes: empty data")
	}
	t, err := tuples.TupleFromBytes(payload, 256)
	if err != nil {
		return nil, fmt.Errorf("OracleDataFromBytes: %w", err)
	}
	elems := make([][]byte, 0, t.NumElements())
	t.ForEach(func(_ int, v []byte) bool {
		elems = append(elems, v)
		return true
	})
	at := func(i int) []byte {
		if i < len(elems) {
			return elems[i]
		}
		return nil
	}
	ret := &OracleData{}
	// Tuple elements are raw (no inline-data prefix); easyfl_util.Uint*FromBytes
	// pad short/empty slices to zero, so absent elements decode as zero.
	if ret.FrozenCoverage, err = easyfl_util.Uint64FromBytes(at(0)); err != nil {
		return nil, fmt.Errorf("OracleDataFromBytes: FrozenCoverage: %w", err)
	}
	if ret.NumConfirmedTransactions, err = easyfl_util.Uint32FromBytes(at(1)); err != nil {
		return nil, fmt.Errorf("OracleDataFromBytes: NumConfirmedTransactions: %w", err)
	}
	if ret.NumSeqTransactions, err = easyfl_util.Uint32FromBytes(at(2)); err != nil {
		return nil, fmt.Errorf("OracleDataFromBytes: NumSeqTransactions: %w", err)
	}
	if ret.NumSeq, err = easyfl_util.Uint32FromBytes(at(3)); err != nil {
		return nil, fmt.Errorf("OracleDataFromBytes: NumSeq: %w", err)
	}
	ret.BaselineRoot = append([]byte(nil), at(4)...)
	return ret, nil
}

// decodeOptionalUint64 decodes a z64-encoded stemLock arg (inline-data
// prefixed); empty bytes ⇒ 0.
func decodeOptionalUint64(arg []byte) (uint64, error) {
	b := easyfl.StripDataPrefix(arg)
	if len(b) == 0 {
		return 0, nil
	}
	return easyfl_util.Uint64FromBytes(b)
}
