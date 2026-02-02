package ledger

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

/*
The file constraints_serde.go contains definitions related to serialization/deserialization of the UTXO constraints.
This is helper, wrapper code of the underlying ledger definitions that are immutable in the ledger.

Serialization means compiling EasyFL source code to bytecode.
Deserialization means decompiling EasyFL source code from the bytecode

The 'constraint' is a EasyFL bytecode, a part of the UTXO.
The Constraint interface wraps Go structure that is parsed from the bytecode.

Usually constraints are known to the node's code via registering it. Registering of constraint places it in the Library structure.
That allows pretty printing of UTXO constraints.

Constraint interface provides functions for the EasyFL name of the constraint, decompiled EasyFL source, human-readable source and serialization to the bytecode.

IMPORTANT: serialization/deserialization of constraints often takes Library as an argument. Libraries can be upgraded by adding and modifying function definitions.
However, serialization/deserialization of a specific formula never changes because it depends on the name <-> op-code relation and
'numArgs' parameter. Both are immutable upon upgrades.

This means, SERIALIZATION/DESERIALIZATION OF CONSTRAINTS IS UPGRADE AGNOSTIC, I.E. IT IS THE SAME AND BACKWARD COMPATIBLE FOR ANY FUTURE LIBRARY UPGRADES.
Being registered as 'constraint' does not impose additional validity rules to the transaction.

Special type of the constraint is 'lock'. Certain constraints are registered as 'locks'. They implement Lock interface.

It is enforced that locks (and only locks) can be at the index 1 of the UTXO constraints. Locks provides a list of 'account ID', identifiers (indexable tags) for the UTXO indexer.
Typically, lock has one indexable tag, sometimes 2 or more.
UTXO index is part of the state: every UTXO has a corresponding index entry in the trie of the ledger state.
BEING REGISTERED AS 'LOCK' IMPOSES ADDITIONAL RULES TO THE LEDGER VALIDITY AND THE CONSISTENCY OF THE STATE.

For example SigLock is lock with one account ID. We can find all UTXOs belonging to certain address in the ledger state.
Another example is DelegateLock. It has two account ID: address of the master and address of the target.

*/

type (
	Constraint interface {
		Name() string   // EasyFL function name in the ledger library
		Bytes() []byte  // bytecode, compiled from EasyFL source
		Source() string // EasyFL source
		String() string // human-readable
	}

	AccountID []byte

	Accountable interface {
		Constraint
		AccountID() AccountID
		AsLock() Lock
	}

	Lock interface {
		Constraint
		// Accounts all accounts of the lock
		Accounts() []Accountable
		// Master is account which is always unlockable. For conditional locks it is usually nil (no master)
		Master() Accountable
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

func (acc AccountID) Bytes() []byte {
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

// AccountableFromBytesWithLib parses an Accountable from bytecode using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func AccountableFromBytesWithLib(data []byte, lib *Library) (Accountable, error) {
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
	return nil, fmt.Errorf("not a indexable constraint '%s'", name)
}

// AccountableFromBytesAtSlot parses an Accountable from bytecode using the library for the given slot.
// Use this when parsing bytecode that was created at a specific slot.
func AccountableFromBytesAtSlot(data []byte, slot uint32) (Accountable, error) {
	return AccountableFromBytesWithLib(data, L(slot))
}

func AccountableFromSource(src string) (Accountable, error) {
	data, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
	}
	// Use latest library version for newly compiled bytecode
	return AccountableFromBytesAtSlot(data, base.MaxSlot)
}

func BelongsToAccount(lock Lock, acc Accountable) bool {
	for _, a := range lock.Accounts() {
		if EqualAccountables(acc, a) {
			return true
		}
	}
	return false
}

func EqualAccountables(a1, a2 Accountable) bool {
	return bytes.Equal(a1.AccountID(), a2.AccountID())
}

func NoDuplicatesAccountables(acc []Accountable) []Accountable {
	ret := make([]Accountable, 0, len(acc))
	for _, a := range acc {
		if util.IsNil(a) {
			continue
		}
		if slices.IndexFunc(ret, func(a1 Accountable) bool {
			return EqualAccountables(a, a1)
		}) >= 0 {
			continue
		}
		ret = append(ret, a)
	}
	return ret
}
