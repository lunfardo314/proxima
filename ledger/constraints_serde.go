package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

/*
Serialization/deserialization of UTXO constraints.

A *constraint* is EasyFL bytecode stored as one element of a UTXO tuple.
The Constraint interface wraps a parsed Go structure over that bytecode.
Registering a constraint in the Library enables decompilation and
pretty-printing. Serde depends only on the name<->opcode mapping and
numArgs, both immutable across upgrades.

The "lock" of an output is just a constraint that lives at the fixed
output element index 2. There is no separate Lock interface — the
indexable data of the lock (controllers / target / sender hashes) lives
at output element index 1 as a tuple of byte slices, not embedded in
the constraint bytecode. See claude/utxo-indexing.md.

For sig/chain/tag, the bytecode at index 2 is a per-kind constant (a
0-arg public symbol like `sigLock`). For delegate, it carries 2 policy
args (maxFrozenEpochs, inflationShare). For stem, it carries 9 stem
aggregates. The Constraint interface plus name-by-prefix lookup is
sufficient to identify and parse any of those — no Lock-specific
machinery is needed.
*/

type (
	Constraint interface {
		Name() string   // EasyFL function name in the ledger library
		Bytes() []byte  // bytecode, compiled from EasyFL source
		Source() string // EasyFL source
		String() string // human-readable
	}

	// Lock is a typed handle on the two output elements that materialise
	// an output's unlock policy: the index-value tuple at output element
	// index 1 (IndexValues) and the lock bytecode at index 2
	// (LockBytecode). It is a builder-side convenience only — there is
	// no shared registry, no factory, no serde indirection. Each
	// concrete lock kind (SigLock, ChainLock, StemLock, TagAlongLock,
	// DelegateLock) implements this interface directly.
	Lock interface {
		// Name returns the EasyFL function name of the lock symbol
		// (e.g. "sigLock", "chainLock", "stemLock").
		Name() string
		// String returns a human-readable representation including the
		// lock's data values.
		String() string
		// IndexValues returns the index-value tuple to be written at
		// output element index 1. Each non-empty element produces one
		// trie index entry under TriePartitionControllers; empty
		// elements are silently skipped.
		IndexValues() [][]byte
		// LockBytecode returns the EasyFL bytecode of the lock to be
		// written at output element index 2. For sig/chain/tag this is
		// a per-kind constant (0-arg public symbol); for delegate it
		// carries (maxFrozenEpochs, inflationShare); for stem it
		// carries the 9 stem aggregates.
		LockBytecode() []byte
	}

	// lockKindMarker is the trivial Constraint wrapper used for 0-arg
	// public lock symbols (sigLock, chainLock, tagAlong). The data and
	// the constraint name are carried by the lock kind alone — there
	// are no arguments to parse from the bytecode.
	lockKindMarker struct {
		name     string
		bytecode []byte
	}

	// ControllerID is the bytes used as the indexable tag in the
	// trie partition for "outputs unlockable by this controller".
	// Typically a 32-byte holder hash or chain ID, but any []byte
	// works (the indexer just stores it as-is). Length must be <= 255
	// because the trie key encodes len in 1 byte.
	ControllerID []byte

	// Controller is a Lock that exposes a single indexable hash —
	// implemented by SigLock (holderID) and ChainLock (chainID). Used
	// by APIs that need to look up UTXOs by the controller's bytes
	// while still being usable as a lock for output construction.
	Controller interface {
		Lock
		ControllerID() ControllerID
		// Source returns the wallet/CLI mini-syntax `kind/<hex>` used
		// to round-trip the controller through string APIs (URL
		// parameters, request fields). Replaces the previous EasyFL
		// `a(0x..)` / `c(0x..)` form. See ControllerFromSource.
		Source() string
	}

	ConstraintParser func([]byte) (Constraint, error)

	constraintRecord struct {
		name   string
		prefix []byte
		parser ConstraintParser
	}

	// LockBalance is a lock/amount pair in a distribution list. Each
	// entry produces one output.
	LockBalance struct {
		Lock        Lock
		Balance     uint64
		ChainOrigin bool // if true, a chain constraint (origin) is added to this output
	}
)

// mustRegisterConstraint registers a constraint parser keyed by its
// bytecode prefix. Used for amounts, chain, sequencer, and the lock
// constraints that live at output element index 2.
func (lib *Library) mustRegisterConstraint(name string, nArgs byte, parser ConstraintParser) {
	prefix, err := lib.FunctionCallPrefixByName(name, nArgs)
	util.AssertNoError(err)
	rec, already := lib.constraintByPrefix[string(prefix)]
	util.Assertf(!already || rec.name == name, "rec.name == name")
	util.Assertf(0 < len(prefix) && len(prefix) <= 2, "wrong constraint prefix %s, name: %s", easyfl_util.Fmt(prefix), name)
	lib.constraintByPrefix[string(prefix)] = &constraintRecord{
		name:   name,
		prefix: bytes.Clone(prefix),
		parser: parser,
	}
}

// mustRegisterVarargConstraint registers a parser for all arities 0..15
// of the named constraint.
func (lib *Library) mustRegisterVarargConstraint(name string, parser ConstraintParser) {
	for i := 0; i <= 15; i++ {
		prefix, err := lib.FunctionCallPrefixByName(name, byte(i))
		util.AssertNoError(err)

		_, already := lib.constraintByPrefix[string(prefix)]
		util.Assertf(!already, "repeating constraint prefix %s with name '%s'", easyfl_util.Fmt(prefix), name)
		util.Assertf(0 < len(prefix) && len(prefix) <= 2, "wrong constraint prefix %s, name: %s", easyfl_util.Fmt(prefix), name)
		lib.constraintByPrefix[string(prefix)] = &constraintRecord{
			name:   name,
			prefix: bytes.Clone(prefix),
			parser: parser,
		}
	}
}

// NameByPrefixWithLib looks up constraint name from bytecode prefix.
func NameByPrefixWithLib(prefix []byte, lib *Library) (string, bool) {
	if ret, found := lib.constraintByPrefix[string(prefix)]; found {
		return ret.name, true
	}
	return "", false
}

// NameByPrefix looks up constraint name using the latest library.
func NameByPrefix(prefix []byte) (string, bool) {
	return NameByPrefixWithLib(prefix, L(base.MaxSlot))
}

// constraintParserByPrefixWithLib returns the registered parser for the
// given bytecode prefix.
func constraintParserByPrefixWithLib(prefix []byte, lib *Library) (ConstraintParser, bool) {
	if ret, found := lib.constraintByPrefix[string(prefix)]; found {
		return ret.parser, true
	}
	return nil, false
}

// mustBinFromSource compiles EasyFL source to bytecode using the latest
// library. Panics on error.
func mustBinFromSource(src string) []byte {
	ret, err := binFromSource(src)
	util.AssertNoError(err)
	return ret
}

// binFromSource compiles EasyFL source to bytecode using the latest
// library.
func binFromSource(src string) ([]byte, error) {
	_, _, binCode, err := L(base.MaxSlot).CompileExpression(src)
	return binCode, err
}

func (acc ControllerID) Bytes() []byte {
	return acc
}

// EqualControllers compares two controllers by their ID bytes.
func EqualControllers(a, b Controller) bool {
	return bytes.Equal(a.ControllerID(), b.ControllerID())
}

// ControllerFromSource parses a "kind/<hex>" mini-syntax used by the
// wallet/CLI and returns the resulting Controller (SigLock or
// ChainLock). This replaces the previous EasyFL-source compilation
// path (`a(0x..)` / `c(0x..)`).
func ControllerFromSource(src string) (Controller, error) {
	id, kind, err := ControllerIDFromSource(src)
	if err != nil {
		return nil, err
	}
	switch kind {
	case SigLockName:
		var sig SigLock
		copy(sig[:], id)
		return sig, nil
	case ChainLockName:
		return ChainLock(append([]byte(nil), id...)), nil
	}
	return nil, fmt.Errorf("ControllerFromSource: unknown kind %s", kind)
}

// ControllerIDFromSource parses a "kind/<hex>" mini-syntax used by the
// wallet/CLI and returns the resulting ControllerID bytes plus the lock
// kind name. Recognised forms:
//
//	sigLock/<64-hex>         → 32-byte holder
//	chainLock/<64-hex>       → 32-byte chain ID
//
// This replaces the previous EasyFL-source compilation path
// (`a(0x..)` / `c(0x..)`) — the public lock symbols are 0-arg now and
// no longer parse the holder/chainID as an argument.
func ControllerIDFromSource(src string) (ControllerID, string, error) {
	for _, kind := range []string{SigLockName, ChainLockName} {
		prefix := kind + "/"
		if len(src) > len(prefix) && src[:len(prefix)] == prefix {
			hexPart := src[len(prefix):]
			if len(hexPart) != 64 {
				return nil, kind, fmt.Errorf("ControllerIDFromSource: %s expects 32-byte hex (got %d chars)", kind, len(hexPart))
			}
			id, err := decodeHex32(hexPart)
			if err != nil {
				return nil, kind, fmt.Errorf("ControllerIDFromSource: %w", err)
			}
			return id[:], kind, nil
		}
	}
	return nil, "", fmt.Errorf("ControllerIDFromSource: unrecognised lock source '%s'", src)
}

func decodeHex32(s string) (ret [32]byte, err error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return ret, err
	}
	if len(b) != 32 {
		return ret, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(ret[:], b)
	return ret, nil
}

func (m *lockKindMarker) Name() string   { return m.name }
func (m *lockKindMarker) Bytes() []byte  { return m.bytecode }
func (m *lockKindMarker) Source() string { return m.name }
func (m *lockKindMarker) String() string { return m.name }

// registerLockKind registers a 0-arg public lock symbol so that
// `ConstraintFromBytesWithLib` recognises the bytecode at output element
// index 2 and returns a typed marker.
func (lib *Library) registerLockKind(name string) {
	lib.mustRegisterConstraint(name, 0, func(data []byte) (Constraint, error) {
		return &lockKindMarker{name: name, bytecode: bytes.Clone(data)}, nil
	})
}

// EqualConstraints returns true if both constraints have identical bytecode.
func EqualConstraints(l1, l2 Constraint) bool {
	if util.IsNil(l1) != util.IsNil(l2) {
		return false
	}
	if util.IsNil(l1) || util.IsNil(l2) {
		return false
	}
	return bytes.Equal(l1.Bytes(), l2.Bytes())
}

// ConstraintFromBytesWithLib parses a constraint from bytecode. Falls
// back to GeneralScript for unknown prefixes.
func ConstraintFromBytesWithLib(data []byte, lib *Library) (Constraint, error) {
	prefix, err := lib.ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}

	if parser, ok := constraintParserByPrefixWithLib(prefix, lib); ok {
		return parser(data)
	}
	return NewGeneralScript(data), nil
}

// LockFromOutputElementsWithLib reconstructs a Lock from the two output
// elements that materialise it:
//   - indexValuesBytes — bytes at output element index 1 (the
//     index-value tuple).
//   - lockBytecode     — bytes at output element index 2 (the lock's
//     EasyFL bytecode).
//
// Dispatches on the bytecode prefix at output element index 2 to build
// the right concrete lock type.
func LockFromOutputElementsWithLib(indexValuesBytes, lockBytecode []byte, lib *Library) (Lock, error) {
	prefix, err := lib.ParsePrefixBytecode(lockBytecode)
	if err != nil {
		return nil, fmt.Errorf("LockFromOutputElements: cannot parse lock prefix: %w", err)
	}
	name, ok := NameByPrefixWithLib(prefix, lib)
	if !ok {
		return nil, fmt.Errorf("LockFromOutputElements: unknown lock prefix '%s'", easyfl_util.Fmt(prefix))
	}
	values, err := IndexValuesFromBytes(indexValuesBytes)
	if err != nil {
		return nil, fmt.Errorf("LockFromOutputElements: %w", err)
	}
	switch name {
	case SigLockName:
		if len(values) != 1 || len(values[0]) != 32 {
			return nil, fmt.Errorf("LockFromOutputElements: %s expects 1 index value of 32 bytes", name)
		}
		var sig SigLock
		copy(sig[:], values[0])
		return sig, nil
	case ChainLockName:
		if len(values) != 1 || len(values[0]) != 32 {
			return nil, fmt.Errorf("LockFromOutputElements: %s expects 1 index value of 32 bytes", name)
		}
		return ChainLock(append([]byte(nil), values[0]...)), nil
	case StemLockName:
		return StemLockFromBytesWithLib(lockBytecode, lib)
	case TagAlongLockName:
		if len(values) != 2 || len(values[0]) != 32 || len(values[1]) != 32 {
			return nil, fmt.Errorf("LockFromOutputElements: %s expects 2 index values of 32 bytes each", name)
		}
		ret := &TagAlongLock{}
		copy(ret.SenderID[:], values[0])
		copy(ret.TargetSequencerID[:], values[1])
		return ret, nil
	case DelegateLockName:
		return DelegateLockFromOutputElements(indexValuesBytes, lockBytecode, lib)
	}
	return nil, fmt.Errorf("LockFromOutputElements: '%s' is not a known lock kind", name)
}

// LockFromOutputElements parses using the latest library.
func LockFromOutputElements(indexValuesBytes, lockBytecode []byte) (Lock, error) {
	return LockFromOutputElementsWithLib(indexValuesBytes, lockBytecode, L(base.MaxSlot))
}
