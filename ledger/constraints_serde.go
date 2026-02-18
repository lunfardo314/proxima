package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

/*
Serialization/deserialization of UTXO constraints: compiling EasyFL source to bytecode and back.

A constraint is EasyFL bytecode stored as part of a UTXO tuple. The Constraint interface wraps
a parsed Go structure over that bytecode. Registering a constraint in the Library enables
decompilation and pretty-printing.

Serde depends only on the name<->opcode mapping and numArgs, both immutable across upgrades.
Therefore constraint serde is fully backward-compatible with any future Library upgrade.
Registering a constraint alone does not impose additional transaction validity rules.

A lock is a special constraint registered via mustRegisterLockSerde. Locks must occupy index 1
of the UTXO tuple. Each lock provides one or more ControllerIDs used as indexable tags in the
UTXO index (part of the ledger state trie). Registering a lock imposes additional rules on
ledger validity and state consistency.

Examples:
  - SigLock: one ControllerID (the address). Enables lookup of all UTXOs for an address.
  - DelegateLock: two ControllerIDs (master address and target address).
*/

type (
	Constraint interface {
		Name() string   // EasyFL function name in the ledger library
		Bytes() []byte  // bytecode, compiled from EasyFL source
		Source() string // EasyFL source
		String() string // human-readable
	}

	// ControllerID is an indexable tag for the UTXO state index.
	// Each lock provides one or more ControllerIDs. UTXOs sharing a ControllerID are unlockable the same way.
	// Values: sigLock and chainLock use their own bytecode; stemLock uses a single zero byte (placeholder).
	ControllerID []byte // assumed <= 255

	Controller interface {
		Constraint
		ControllerID() ControllerID
		AsLock() Lock
	}

	Lock interface {
		Constraint
		// Controllers returns all controllers of the lock.
		Controllers() []Controller
		// Master returns the unconditionally unlockable controller, or nil for conditional locks.
		Master() Controller
	}

	ConstraintParser func([]byte) (Constraint, error)
	LockParser       func([]byte) (Lock, error)

	constraintRecord struct {
		name   string
		prefix []byte
		parser ConstraintParser
	}

	// LockBalance is a lock/amount pair in a distribution list. Each entry produces one output.
	LockBalance struct {
		Lock        Lock
		Balance     uint64
		ChainOrigin bool // if true, a chain constraint (origin) is added to this output
	}
)

// mustRegisterConstraint registers a constraint parser keyed by its bytecode prefix.
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

// mustRegisterVarargConstraint registers a parser for all arities 0..15 of the named constraint.
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

// mustRegisterLockSerde registers a lock parser by constraint name.
func (lib *Library) mustRegisterLockSerde(name string, parser LockParser) {
	_, already := lib.locksByName[name]
	util.Assertf(!already, "mustRegisterLockSerde: repeating lock '%s'", name)

	lib.locksByName[name] = parser
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

// constraintParserByPrefixWithLib returns the registered parser for the given bytecode prefix.
func constraintParserByPrefixWithLib(prefix []byte, lib *Library) (ConstraintParser, bool) {
	if ret, found := lib.constraintByPrefix[string(prefix)]; found {
		return ret.parser, true
	}
	return nil, false
}

// mustBinFromSource compiles EasyFL source to bytecode using the latest library. Panics on error.
func mustBinFromSource(src string) []byte {
	ret, err := binFromSource(src)
	util.AssertNoError(err)
	return ret
}

// binFromSource compiles EasyFL source to bytecode using the latest library.
func binFromSource(src string) ([]byte, error) {
	_, _, binCode, err := L(base.MaxSlot).CompileExpression(src)
	return binCode, err
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

// ConstraintFromBytesWithLib parses a constraint from bytecode. Falls back to GeneralScript for unknown prefixes.
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

func (acc ControllerID) Bytes() []byte {
	return acc
}

// LockFromBytes parses a lock from bytecode using the latest library.
func LockFromBytes(data []byte) (Lock, error) {
	return LockFromBytesWithLib(data, L(base.MaxSlot))
}

// LockFromBytesWithLib parses a lock from bytecode. Returns error for unknown or non-lock constraints.
func LockFromBytesWithLib(data []byte, lib *Library) (Lock, error) {
	prefix, err := lib.ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}
	name, ok := NameByPrefixWithLib(prefix, lib)
	if !ok {
		return nil, fmt.Errorf("LockFromBytesWithLib: unknown constraint with prefix '%s'", easyfl_util.Fmt(prefix))
	}

	parser, ok := lib.locksByName[name]
	if !ok {
		return nil, fmt.Errorf("LockFromBytesWithLib: unknown lock '%s'", name)
	}
	return parser(data)
}

// ControllerFromBytesWithLib parses a controller lock (sigLock, chainLock, or stemLock) from bytecode.
func ControllerFromBytesWithLib(data []byte, lib *Library) (Controller, error) {
	prefix, err := lib.ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}
	name, ok := NameByPrefixWithLib(prefix, lib)
	if !ok {
		return nil, fmt.Errorf("unknown constraint with prefix '%s'", easyfl_util.Fmt(prefix))
	}
	switch name {
	case SigLockName:
		return SigLockFromBytes(data)
	case ChainLockName:
		return ChainLockFromBytesWithLib(data, lib)
	case StemLockName:
		return StemLockFromBytesWithLib(data, lib)
	}
	return nil, fmt.Errorf("not a controller lock '%s'", name)
}

// ControllerFromSource compiles EasyFL source and parses it as a controller lock.
func ControllerFromSource(src string) (Controller, error) {
	data, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
	}
	return ControllerFromBytesWithLib(data, L(base.MaxSlot))
}

// LockIsControlledBy returns true if acc is among the lock's controllers.
func LockIsControlledBy(lock Lock, acc Controller) bool {
	for _, a := range lock.Controllers() {
		if EqualControllers(acc, a) {
			return true
		}
	}
	return false
}

// EqualControllers returns true if both controllers have the same ControllerID.
func EqualControllers(a1, a2 Controller) bool {
	return bytes.Equal(a1.ControllerID(), a2.ControllerID())
}
