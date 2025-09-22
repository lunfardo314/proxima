package tests

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestAmountsBase(t *testing.T) {
	t.Run("compile", func(t *testing.T) {
		compFun := func(src string) []byte {
			_, _, code, err := ledger.L().CompileExpression(src)
			require.NoError(t, err)
			srcBack, err := ledger.L().DecompileBytecode(code)
			require.NoError(t, err)
			t.Logf("\n    src: '%s'\n    bytecode: %s\n    decompiled: '%s'", src, easyfl_util.Fmt(code), srcBack)
			return code
		}
		checkNargs := func(code []byte, nargs string) {
			src := fmt.Sprintf("parseNumArgs(0x%s)", hex.EncodeToString(code))
			ledger.L().MustEqual(src, nargs)
		}
		checkArg := func(code []byte, idx, val string) {
			src := fmt.Sprintf("amountAt(0x%s,%s)", hex.EncodeToString(code), idx)
			ledger.L().MustEqual(src, val)

		}
		code := compFun("amounts")
		checkNargs(code, "0")
		checkArg(code, "0", "u64/0")

		code = compFun("amounts(1)")
		checkNargs(code, "1")
		checkArg(code, "0", "u64/1")
		checkArg(code, "5", "u64/0")

		code = compFun("amounts(0x)")
		checkNargs(code, "1")
		checkArg(code, "0", "u64/0")
		checkArg(code, "11", "u64/0")

		code = compFun("amounts(1,2,3)")
		checkNargs(code, "3")
		checkArg(code, "0", "u64/1")
		checkArg(code, "1", "u64/2")
		checkArg(code, "2", "u64/3")
		checkArg(code, "11", "u64/0")

		compFun("amounts(0x,0x,3)")
		compFun("amounts(1,2,3,4,5,6,7,8,9,10,11,12,13,14,0x010203040506)")
		compFun("amounts(z64/1000, z64/0,z64/11111111111111111111)")
	})

	var addr0 ledger.AddressED25519
	var u *utxodb.UTXODB
	var privKey0 ed25519.PrivateKey

	const amountFromFaucet = 1_000_000_000_000
	_ = privKey0
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, amountFromFaucet)
		require.NoError(t, err)
	}
	t.Run("fail not at index 0", func(t *testing.T) {
		initTest()

		const transferAmount = 100_000_000
		outs, amount := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(addr0, transferAmount)
		require.True(t, len(outs) == 1)
		require.True(t, amount >= transferAmount)

		txb := txbuilder.New()
		_, ts, err := txb.ConsumeOutputsUnlock(outs...)
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(transferAmount).
				WithLock(addr0).
				MustPushConstraint(ledger.NewAmounts(1, 2).Bytes())
		}))
		require.NoError(t, err)

		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(amountFromFaucet - transferAmount).
				WithLock(addr0)
		}))
		require.NoError(t, err)

		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.TransactionData.Timestamp = ts.AddTicks(int(ledger.Const.TransactionPace))
		txb.SignED25519(privKey0)

		_, _, _, err = txb.BytesWithValidation()
		util.RequireErrorWithOld(t, err, "'amounts' must be at index 0")
	})
}
