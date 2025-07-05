package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
)

func TestAmountsBase(t *testing.T) {
	compFun := func(src string) {
		_, _, code, err := ledger.L().CompileExpression(src)
		require.NoError(t, err)
		srcBack, err := ledger.L().DecompileBytecode(code)
		require.NoError(t, err)
		t.Logf("\n    src: '%s'\n    bytecode: %s\n    decompiled: '%s'", src, easyfl_util.Fmt(code), srcBack)
	}
	t.Run("compile", func(t *testing.T) {
		compFun("amounts")
		compFun("amounts(1)")
		compFun("amounts(0x)")
		compFun("amounts(1,2,3)")
		compFun("amounts(0x,0x,3)")
		compFun("amounts(1,2,3,4,5,6,7,8,9,10,11,12,13,14,0x010203040506)")
		compFun("amounts(z64/1000, z64/0,z64/11111111111111111111)")
	})
}
