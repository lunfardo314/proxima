package tests

import (
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

func TestEasyFL(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	t.Run("compile literal 1", func(t *testing.T) {
		const source = "12"

		_, nargs, bytecode, err := lib.CompileExpression(source)
		require.NoError(t, err)
		t.Logf("src: '%s', args: %d, bytecode: %s", source, nargs, hex.EncodeToString(bytecode))
	})
	t.Run("compile literal 2", func(t *testing.T) {
		const source = "u64/1337"

		_, nargs, bytecode, err := lib.CompileExpression(source)
		require.NoError(t, err)
		res, err := lib.EvalFromBytecode(nil, bytecode)
		require.NoError(t, err)

		t.Logf("src: '%s', args: %d, bytecode: %s, eval: %s",
			source, nargs, hex.EncodeToString(bytecode), hex.EncodeToString(res))
	})
	t.Run("compile literal 3", func(t *testing.T) {
		const source = "concat(u16/15, 255)"

		_, nargs, bytecode, err := lib.CompileExpression(source)
		require.NoError(t, err)

		res, err := lib.EvalFromBytecode(nil, bytecode)
		require.NoError(t, err)

		t.Logf("src: '%s', args: %d, bytecode: %s, eval: %s",
			source, nargs, hex.EncodeToString(bytecode), hex.EncodeToString(res))
	})
	t.Run("compile literal 4", func(t *testing.T) {
		const source = "z64/1337"

		_, nargs, bytecode, err := lib.CompileExpression(source)
		require.NoError(t, err)
		res, err := lib.EvalFromBytecode(nil, bytecode)
		require.NoError(t, err)

		t.Logf("src: '%s', args: %d, bytecode: %s, eval: %s",
			source, nargs, hex.EncodeToString(bytecode), hex.EncodeToString(res))
	})
	t.Run("compile literal 5", func(t *testing.T) {
		const source = "z64/0"

		_, nargs, bytecode, err := lib.CompileExpression(source)
		require.NoError(t, err)
		res, err := lib.EvalFromBytecode(nil, bytecode)
		require.NoError(t, err)

		t.Logf("src: '%s', args: %d, bytecode: %s, eval: %s",
			source, nargs, hex.EncodeToString(bytecode), hex.EncodeToString(res))
	})
}
