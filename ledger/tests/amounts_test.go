package tests

import (
	"crypto/ed25519"
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
		compFun := func(src string) {
			_, _, code, err := ledger.L().CompileExpression(src)
			require.NoError(t, err)
			srcBack, err := ledger.L().DecompileBytecode(code)
			require.NoError(t, err)
			t.Logf("\n    src: '%s'\n    bytecode: %s\n    decompiled: '%s'", src, easyfl_util.Fmt(code), srcBack)
		}
		compFun("amounts")
		compFun("amounts(1)")
		compFun("amounts(0x)")
		compFun("amounts(1,2,3)")
		compFun("amounts(0x,0x,3)")
		compFun("amounts(1,2,3,4,5,6,7,8,9,10,11,12,13,14,0x010203040506)")
		compFun("amounts(z64/1000, z64/0,z64/11111111111111111111)")
	})

	var addr0 ledger.AddressED25519
	var u *utxodb.UTXODB
	var privKey0 ed25519.PrivateKey

	const amountFromFaucet = 10_000_000
	_ = privKey0
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, amountFromFaucet)
		require.NoError(t, err)
	}

	t.Run("basic", func(t *testing.T) {
		initTest()
		const transferAmount = 1_000_000
		outs, amount := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(addr0, transferAmount)
		util.Assertf(len(outs) == 1, "expected 1 output")
		util.Assertf(amount >= transferAmount, "expected 1 output")

		txb := txbuilder.New()
		_, ts, err := txb.ConsumeOutputsUnlock(outs...)
		require.NoError(t, err)

		_, _ = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(transferAmount).
				WithLock(addr0).
				MustPushConstraint(ledger.NewAmounts(1, 2).Bytes())
		}))
		_, _ = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmount(amountFromFaucet - transferAmount).
				WithLock(addr0)
		}))

		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.TransactionData.Timestamp = ts.AddTicks(int(ledger.L().ID.TransactionPace))
		txb.SignED25519(privKey0)

		txBytes, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Fatal(fmt.Errorf("error: %v\n-------------- failing tx ---------------\n%s", err, txString))
		} else {
			t.Logf("-------------- valid tx ---------------\n%s", txString)
			err = u.AddTransaction(txBytes)
			require.NoError(t, err)
		}
	})
}
