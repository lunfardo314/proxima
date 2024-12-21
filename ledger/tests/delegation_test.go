package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/require"
)

func TestDelegation(t *testing.T) {
	var privKey []ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr []ledger.AddressED25519

	const (
		tokensFromFaucet0 = 10_000_000
		tokensFromFaucet1 = 10_000_001
		delegatedTokens   = 2_000_000
	)
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey, _, addr = u.GenerateAddresses(0, 2)
		err := u.TokensFromFaucet(addr[0], tokensFromFaucet0)
		require.NoError(t, err)
		err = u.TokensFromFaucet(addr[1], tokensFromFaucet1)
		require.NoError(t, err)
	}

	t.Run("1", func(t *testing.T) {
		initTest()
		par, err := u.MakeTransferInputData(privKey[0], nil, ledger.TimeNow())

		lock := ledger.NewDelegationLock(addr[0], addr[1], 2)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.
			WithAmount(delegatedTokens).
			WithTargetLock(lock).
			WithConstraint(ledger.NewChainOrigin()),
		)
		require.NoError(t, err)
		t.Logf(u.TxToString(txBytes))

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-tokensFromFaucet0-tokensFromFaucet1, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, tokensFromFaucet0, u.Balance(addr[0]))
		require.EqualValues(t, 2, u.NumUTXOs(addr[0]))
		require.EqualValues(t, 2, u.NumUTXOs(addr[1]))

		rdr := multistate.MakeSugared(u.StateReader())

		outs, err := rdr.GetOutputsDelegatedToAccount(addr[0])
		require.NoError(t, err)
		require.EqualValues(t, 0, len(outs))

		outs, err = rdr.GetOutputsDelegatedToAccount(addr[1])
		require.NoError(t, err)
		require.EqualValues(t, 1, len(outs))

		t.Logf("delegated output to addr %s:\n%s", addr[1].Short(), outs[0].Lines("      ").String())
	})
}
