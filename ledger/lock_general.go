package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
)

const GeneralLockName = "generalLock"

type GeneralLock []byte

func NewGeneralLock(bytecode []byte) GeneralLock {
	return bytecode
}

func NewGeneralLockFromSource(src string) (GeneralLock, error) {
	_, _, bytecode, err := L().CompileExpression(src)
	if err != nil {
		return nil, err
	}
	return bytecode, nil
}

func (u GeneralLock) Name() string {
	return GeneralLockName
}

func (u GeneralLock) Bytes() []byte {
	return u
}

func (u GeneralLock) String() string {
	src, err := L().DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return fmt.Sprintf("GeneralLock(%s) (decompile: %s)", easyfl.Fmt(u), src)
}

func (u GeneralLock) Accounts() []Accountable {
	//TODO implement me
	panic("implement me")
}

func GeneralLockFromBytes(data []byte) (GeneralLock, error) {
	if _, err := L().DecompileBytecode(data); err != nil {
		return nil, err
	}
	return data, nil
}
