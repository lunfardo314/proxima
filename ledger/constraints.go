package ledger

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

type (
	Constraint interface {
		Name() string
		Bytes() []byte
		String() string
	}

	AccountID []byte

	Accountable interface {
		Constraint
		AccountID() AccountID
		AsLock() Lock
	}

	Lock interface {
		Constraint
		Accounts() []Accountable
		UnlockableWith(acc AccountID, ts ...Time) bool
	}

	Parser func([]byte) (Constraint, error)

	constraintRecord struct {
		name   string
		prefix []byte
		parser Parser
	}

	LockBalance struct {
		Lock    Lock
		Balance uint64
	}
)

func (lib *Library) extendWithConstraint(name, source string, nArgs byte, parser Parser, inlineTests ...func()) {
	lib.MustExtendMany(source)
	prefix, err := lib.FunctionCallPrefixByName(name, nArgs)
	util.AssertNoError(err)
	_, already := lib.constraintNames[name]
	util.Assertf(!already, "repeating constraint name '%s'", name)
	_, already = lib.constraintByPrefix[string(prefix)]
	util.Assertf(!already, "repeating constraint prefix %s with name '%s'", easyfl.Fmt(prefix), name)
	util.Assertf(0 < len(prefix) && len(prefix) <= 2, "wrong constraint prefix %s, name: %s", easyfl.Fmt(prefix), name)
	lib.constraintByPrefix[string(prefix)] = &constraintRecord{
		name:   name,
		prefix: common.Concat(prefix),
		parser: parser,
	}
	lib.constraintNames[name] = struct{}{}
	lib.inlineTests = append(lib.inlineTests, inlineTests...)
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

func parserByPrefix(prefix []byte) (Parser, bool) {
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

	if parser, ok := parserByPrefix(prefix); ok {
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
		return nil, fmt.Errorf("unknown constraint with prefix '%s'", easyfl.Fmt(prefix))
	}
	switch name {
	case AddressED25519Name:
		return AddressED25519FromBytes(data)
	case DeadlineLockName:
		return DeadlineLockFromBytes(data)
	case ChainLockName:
		return ChainLockFromBytes(data)
	case StemLockName:
		return StemLockFromBytes(data)
	default:
		return GeneralLockFromBytes(data)
	}
}

func AccountableFromBytes(data []byte) (Accountable, error) {
	prefix, err := L().ParsePrefixBytecode(data)
	if err != nil {
		return nil, err
	}
	name, ok := NameByPrefix(prefix)
	if !ok {
		return nil, fmt.Errorf("unknown constraint with prefix '%s'", easyfl.Fmt(prefix))
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
