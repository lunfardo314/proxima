package ledger

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

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

func (lib *Library) mustRegisterConstraint(name string, nArgs byte, parser ConstraintParser, inlineTests ...func()) {
	prefix, err := lib.FunctionCallPrefixByName(name, nArgs)
	util.AssertNoError(err)
	util.Assertf(!lib.constraintNames.Contains(name), "repeating constraint name '%s'", name)
	_, already := lib.constraintByPrefix[string(prefix)]
	util.Assertf(!already, "repeating constraint prefix %s with name '%s'", easyfl_util.Fmt(prefix), name)
	util.Assertf(0 < len(prefix) && len(prefix) <= 2, "wrong constraint prefix %s, name: %s", easyfl_util.Fmt(prefix), name)
	lib.constraintByPrefix[string(prefix)] = &constraintRecord{
		name:   name,
		prefix: common.Concat(prefix),
		parser: parser,
	}
	lib.constraintNames.Insert(name)
	lib.appendInlineTests(inlineTests...)
}

func (lib *Library) mustRegisterLock(name string, parser LockParser) {
	util.Assertf(lib.constraintNames.Contains(name), "mustRegisterLock: unknown constraint '%s'", name)
	_, already := lib.locksByName[name]
	util.Assertf(!already, "mustRegisterLock: repeating lock '%s'", name)

	lib.locksByName[name] = parser
}

func (lib *Library) appendInlineTests(fun ...func()) {
	lib.inlineTests = append(lib.inlineTests, fun...)
}

func (lib *Library) runInlineTests() {
	for _, fun := range lib.inlineTests {
		fun()
	}
}

func NameByPrefix(prefix []byte) (string, bool) {
	if ret, found := L().constraintByPrefix[string(prefix)]; found {
		return ret.name, true
	}
	return "", false
}

func constraintParserByPrefix(prefix []byte) (ConstraintParser, bool) {
	if ret, found := L().constraintByPrefix[string(prefix)]; found {
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
	_, _, binCode, err := L().CompileExpression(src)
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

func ConstraintFromBytes(data []byte) (Constraint, error) {
	prefix, err := L().ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}

	if parser, ok := constraintParserByPrefix(prefix); ok {
		return parser(data)
	}
	return NewGeneralScript(data), nil
}

func (acc AccountID) Bytes() []byte {
	return acc
}

func LockFromBytes(data []byte) (Lock, error) {
	prefix, err := L().ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}
	name, ok := NameByPrefix(prefix)
	if !ok {
		return nil, fmt.Errorf("LockFromBytes: unknown constraint with prefix '%s'", easyfl_util.Fmt(prefix))
	}

	parser, ok := L().locksByName[name]
	if !ok {
		return nil, fmt.Errorf("LockFromBytes: unknown lock '%s'", name)
	}
	return parser(data)
}

func AccountableFromBytes(data []byte) (Accountable, error) {
	prefix, err := L().ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}
	name, ok := NameByPrefix(prefix)
	if !ok {
		return nil, fmt.Errorf("unknown constraint with prefix '%s'", easyfl_util.Fmt(prefix))
	}
	switch name {
	case AddressED25519Name:
		return AddressED25519FromBytes(data)
	case ChainLockName:
		return ChainLockFromBytes(data)
	case StemLockName:
		return StemLockFromBytes(data)
	}
	return nil, fmt.Errorf("not a indexable constraint '%s'", name)
}

func AccountableFromSource(src string) (Accountable, error) {
	data, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
	}
	return AccountableFromBytes(data)
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
