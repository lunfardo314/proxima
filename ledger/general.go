package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
)

type GeneralScript []byte

func NewGeneralScript(data []byte) GeneralScript {
	return data
}

func (u GeneralScript) Name() string {
	return "GeneralScript"
}

func (u GeneralScript) Bytes() []byte {
	return u
}

func (u GeneralScript) String() string {
	lib := L(base.MaxSlot)
	src, err := lib.DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return fmt.Sprintf("GeneralScript(%s) (decompile: %s)", easyfl_util.Fmt(u), src)
}

func (u GeneralScript) Source() string {
	src, err := L(base.MaxSlot).DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return src
}
