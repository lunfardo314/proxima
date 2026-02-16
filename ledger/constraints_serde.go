package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

/*
The file constraints_serde.go contains definitions related to serialization/deserialization of the UTXO constraints.
This is a helper wrapper code of the underlying ledger definitions that are immutable in the ledger.

Serialization means compiling EasyFL source code to bytecode.
Deserialization means decompiling EasyFL source code from the bytecode

The 'constraint' is an EasyFL bytecode, a part of the UTXO.
The Constraint interface wraps Go structure that is parsed from the bytecode.

Usually constraints are known to the Library via registering it. That allows decompiling and pretty printing of UTXO constraints.

IMPORTANT: serialization/deserialization of constraints often takes Library as an argument. Libraries can be upgraded by adding and modifying function definitions.
However, serialization/deserialization of a specific formula never changes because it depends on the name <-> op-code relation and the 'numArgs' parameter. Both are immutable upon upgrades.

This means, SERIALIZATION/DESERIALIZATION OF CONSTRAINTS IS UPGRADE AGNOSTIC, I.E. IT IS THE SAME AND BACKWARD COMPATIBLE FOR ANY FUTURE LIBRARY UPGRADES.
Being registered as 'constraint' in the Library does not impose additional validity rules to the transaction.

Special type of the constraint is 'lock'. Certain constraints are registered as 'locks'. They implement Lock interface.

It is enforced that locks (and only locks) can be at the index 1 of the UTXO constraints. Locks provides a list of 'ControllerIDs', identifiers (indexable tags) for the UTXO indexer.
Typically, lock has one indexable tag, sometimes 2 or more.
UTXO index is part of the ledger state: every UTXO has at least one corresponding index entry in the trie of the ledger state.
BEING REGISTERED AS 'LOCK' IMPOSES ADDITIONAL RULES TO THE LEDGER VALIDITY AND THE CONSISTENCY OF THE STATE.

For example SigLock is lock with one index value. We can find all UTXOs belonging to certain address in the ledger state.

Another example is DelegateLock. It has two controller IDs: address of the master and address of the target.
*/

type (
	Constraint interface {
		Name() string   // EasyFL function name in the ledger library
		Bytes() []byte  // bytecode, compiled from EasyFL source
		Source() string // EasyFL source
		String() string // human-readable
	}

	// ControllerID is used as a value in the index of UTXOs in the state.
	// UTXOs with the same ControllerID can be unlocked the same way.
	// Each lock constraint of UTXO provides 1 or more controller IDs for the index.
	// There are 3 single-controller locks:
	// - sigLock's controllerID is bytecode of the sigLock Constraint
	// - chainLock's controllerID is bytecode of the chainLock Constraint
	// - stemLock controllerID is 1 byte with 0 value (a placeholder)
	ControllerID []byte // assumed <= 255

	Controller interface {
		Constraint
		ControllerID() ControllerID
		AsLock() Lock
	}

	Lock interface {
		Constraint
		// Controllers all controllers of the lock
		Controllers() []Controller
		// Master is account which is always unlockable. For conditional locks it is usually nil (no master)
		Master() Controller
	}

	ConstraintParser func([]byte) (Constraint, error)
	LockParser       func([]byte) (Lock, error)

	constraintRecord struct {
		name   string
		prefix []byte
		parser ConstraintParser
	}

	// LockBalance is an amount/target pair used in distribution list
	// One LockBalance results in one produced output on the transaction
	LockBalance struct {
		// Lock of the output
		Lock Lock
		// Balance amount of tokens on the output
		Balance uint64
		// ChainOrigin true if start a chain on this output by adding chain constrain (origin)
		//	 false for simple ED25519 account balance (no chain origin added)
		ChainOrigin bool
	}
)

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

// mustRegisterVarargConstraint registers one parser for each possible number of args (0 to 15)
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

func (lib *Library) mustRegisterLockSerde(name string, parser LockParser) {
	_, already := lib.locksByName[name]
	util.Assertf(!already, "mustRegisterLockSerde: repeating lock '%s'", name)

	lib.locksByName[name] = parser
}

// NameByPrefixWithLib looks up constraint name from bytecode prefix using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func NameByPrefixWithLib(prefix []byte, lib *Library) (string, bool) {
	if ret, found := lib.constraintByPrefix[string(prefix)]; found {
		return ret.name, true
	}
	return "", false
}

// NameByPrefixAtSlot looks up constraint name from bytecode prefix using the library for the given slot.
// Use this when parsing bytecode that was created at a specific slot.
func NameByPrefixAtSlot(prefix []byte, slot uint32) (string, bool) {
	return NameByPrefixWithLib(prefix, L(slot))
}

// NameByPrefix looks up constraint name using the latest library version.
func NameByPrefix(prefix []byte) (string, bool) {
	return NameByPrefixAtSlot(prefix, base.MaxSlot)
}

func constraintParserByPrefixWithLib(prefix []byte, lib *Library) (ConstraintParser, bool) {
	if ret, found := lib.constraintByPrefix[string(prefix)]; found {
		return ret.parser, true
	}
	return nil, false
}

func mustBinFromSource(src string) []byte {
	ret, err := binFromSource(src)
	util.AssertNoError(err)
	return ret
}

func binFromSource(src string) ([]byte, error) {
	_, _, binCode, err := L(base.MaxSlot).CompileExpression(src)
	return binCode, err
}

func EqualConstraints(l1, l2 Constraint) bool {
	if util.IsNil(l1) != util.IsNil(l2) {
		return false
	}
	if util.IsNil(l1) || util.IsNil(l2) {
		return false
	}
	return bytes.Equal(l1.Bytes(), l2.Bytes())
}

// ConstraintFromBytesWithLib parses a constraint from bytecode using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
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

// ConstraintFromBytesAtSlot parses a constraint from bytecode using the library for the given slot.
// Use this when parsing bytecode that was created at a specific slot.
func ConstraintFromBytesAtSlot(data []byte, slot uint32) (Constraint, error) {
	return ConstraintFromBytesWithLib(data, L(slot))
}

func (acc ControllerID) Bytes() []byte {
	return acc
}

// LockFromBytes parses a lock from bytecode using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func LockFromBytes(data []byte) (Lock, error) {
	return LockFromBytesWithLib(data, L(base.MaxSlot))
}

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

// ControllerFromBytesWithLib parses a Controller lock from bytecode using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
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

func ControllerFromSource(src string) (Controller, error) {
	data, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
	}
	// Use latest library version for newly compiled bytecode
	return ControllerFromBytesWithLib(data, L(base.MaxSlot))
}

func LockIsControlledBy(lock Lock, acc Controller) bool {
	for _, a := range lock.Controllers() {
		if EqualControllers(acc, a) {
			return true
		}
	}
	return false
}

func EqualControllers(a1, a2 Controller) bool {
	return bytes.Equal(a1.ControllerID(), a2.ControllerID())
}
