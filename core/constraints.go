package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
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
		UnlockableWith(acc AccountID, ts ...LogicalTime) bool
	}

	Parser func([]byte) (Constraint, error)

	constraintRecord struct {
		name   string
		prefix []byte
		parser Parser
	}
)

var (
	constraintByPrefix = make(map[string]*constraintRecord)
	constraintNames    = make(map[string]struct{})
)

func registerConstraint(name string, prefix []byte, parser Parser) {
	_, already := constraintNames[name]
	util.Assertf(!already, "repeating constraint name '%s'", name)
	_, already = constraintByPrefix[string(prefix)]
	util.Assertf(!already, "repeating constraint prefix %s with name '%s'", easyfl.Fmt(prefix), name)
	util.Assertf(0 < len(prefix) && len(prefix) <= 2, "wrong constraint prefix %s, name: %s", easyfl.Fmt(prefix), name)
	constraintByPrefix[string(prefix)] = &constraintRecord{
		name:   name,
		prefix: common.Concat(prefix),
		parser: parser,
	}
	constraintNames[name] = struct{}{}
}

func NameByPrefix(prefix []byte) (string, bool) {
	if ret, found := constraintByPrefix[string(prefix)]; found {
		return ret.name, true
	}
	return "", false
}

func parserByPrefix(prefix []byte) (Parser, bool) {
	if ret, found := constraintByPrefix[string(prefix)]; found {
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
	_, _, binCode, err := easyfl.CompileExpression(src)
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

func EqualAccountIDs(a1, a2 AccountID) bool {
	return bytes.Equal(a1, a2)
}

func FromBytes(data []byte) (Constraint, error) {
	prefix, err := easyfl.ParseBytecodePrefix(data)
	if err != nil {
		return nil, err
	}
	parser, ok := parserByPrefix(prefix)
	if ok {
		return parser(data)
	}
	return NewGeneralScript(data), nil
}

func (acc AccountID) Bytes() []byte {
	return acc
}

func LockFromBytes(data []byte) (Lock, error) {
	prefix, err := easyfl.ParseBytecodePrefix(data)
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
	}
	return nil, fmt.Errorf("not a lock constraint '%s'", name)
}

func LockFromSource(src string) (Lock, error) {
	_, _, bytecode, err := easyfl.CompileExpression(src)
	if err != nil {
		return nil, err
	}
	return LockFromBytes(bytecode)
}

func AccountableFromBytes(data []byte) (Accountable, error) {
	prefix, err := easyfl.ParseBytecodePrefix(data)
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

func AccountableFromHexString(str string) (Accountable, error) {
	data, err := hex.DecodeString(str)
	if err != nil {
		return nil, err
	}
	return AccountableFromBytes(data)
}

func CloneAccountable(a Accountable) Accountable {
	ret, err := AccountableFromBytes(common.CloneBytes(a.Bytes())) // TODO suboptimal copying bytes
	util.AssertNoError(err)
	return ret
}

// inline unit test
func runCommonUnitTests() {
	pub, _, err := ed25519.GenerateKey(nil)
	util.AssertNoError(err)
	addr := AddressED25519FromPublicKey(pub)
	addrBack := CloneAccountable(addr)
	util.Assertf(EqualConstraints(addr, addrBack), "inline unit test failed for AddressED25519")

	chainLock := ChainLockFromChainID(NilChainID)
	chainConstrBack := CloneAccountable(chainLock)
	util.Assertf(EqualConstraints(chainLock, chainConstrBack), "inline unit test failed for ChainLock")
}

func LockIsIndexableByAccount(lock Lock, accountable Accountable) bool {
	for _, account := range lock.Accounts() {
		if EqualConstraints(account, accountable) {
			return true
		}
	}
	return false
}
