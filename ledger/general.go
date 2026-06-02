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

// String renders the script as its EasyFL source decompile (e.g. for a
// callRedeemer-based chess() lock it becomes `if(selfIsProducedOutput,
// callRedeemer(0x…,42), …)`). The raw bytecode/length is available via the
// output printer's verbose mode and via Bytes(); the user-facing line
// should lead with what the code does, not its hex blob.
func (u GeneralScript) String() string {
	lib := L(base.MaxSlot)
	src, err := lib.DecompileBytecode(u)
	if err != nil {
		return fmt.Sprintf("script(%d bytes, decompile failed: %v): %s",
			len(u), err, easyfl_util.Fmt(u))
	}
	return fmt.Sprintf("script(%d bytes): %s", len(u), src)
}

func (u GeneralScript) Source() string {
	src, err := L(base.MaxSlot).DecompileBytecode(u)
	if err != nil {
		src = fmt.Sprintf("failed decompile")
	}
	return src
}
