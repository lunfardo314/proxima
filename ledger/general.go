package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
)

type GeneralScript []byte

func NewGeneralScript(data []byte) GeneralScript {
	return data
}

func NewGeneralScriptFromSource(src string) (GeneralScript, error) {
	_, _, bytecode, err := L().CompileExpression(src)
	if err != nil {
		return nil, err
	}
	return bytecode, nil
}

func (u GeneralScript) Name() string {
	return "GeneralScript"
}

func (u GeneralScript) Bytes() []byte {
	return u
}

func (u GeneralScript) StringExt() string {
	src, err := L().DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return fmt.Sprintf("GeneralScript(%s) (decompile: %s)", easyfl.Fmt(u), src)
}

func (u GeneralScript) String() string {
	src, err := L().DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return src
}
