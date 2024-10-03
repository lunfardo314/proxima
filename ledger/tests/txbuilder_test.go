package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/require"
)

func TestBasics(t *testing.T) {
	t.Run("utxodb 1", func(t *testing.T) {
		//transaction.SetPrintEasyFLTraceOnFail(true)

		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl.Fmt(pub))
		t.Logf("origin address: %s", easyfl.Fmt(u.GenesisControllerAddress()))

		t.Logf("current timestamp: %s", ledger.TimeNow().String())
		_, _, addr := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr, 100)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-100, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 100, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))

		_, stemOutData := u.StateReader().GetStem()

		stemOut, _, _, err := ledger.OutputFromBytesMain(stemOutData)
		require.NoError(t, err)
		require.EqualValues(t, 0, stemOut.Amount())
		_, ok := stemOut.StemLock()
		require.True(t, ok)

	})
	t.Run("utxodb 2", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl.Fmt(pub))
		t.Logf("origin address: %s", easyfl.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr, 100)
		require.NoError(t, err)
		err = u.TokensFromFaucet(addr)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-100-utxodb.TokensFromFaucetDefault, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 100+utxodb.TokensFromFaucetDefault, u.Balance(addr))
		require.EqualValues(t, 2, u.NumUTXOs(addr))

		err = u.TransferTokens(privKey, addr, u.Balance(addr))
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-100-u.FaucetBalance()-utxodb.TokensFromFaucetDefault, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 100+utxodb.TokensFromFaucetDefault, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))
	})
	t.Run("utxodb 3 compress outputs", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl.Fmt(pub))
		t.Logf("origin address: %s", easyfl.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		const howMany = 256

		total := uint64(0)
		numOuts := 0
		for i := uint64(100); i <= howMany; i++ {
			err := u.TokensFromFaucet(addr, i)
			require.NoError(t, err)
			total += i
			numOuts++

			require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
			require.EqualValues(t, u.Supply()-u.FaucetBalance()-total, u.Balance(u.GenesisControllerAddress()))
			require.EqualValues(t, total, u.Balance(addr))
			require.EqualValues(t, numOuts, u.NumUTXOs(addr))
		}

		ts := ledger.TimeNow()
		t.Logf("ts = %s, %s", ts.String(), ts.Hex())
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)
		txBytes, err := txbuilder.MakeTransferTransaction(par.
			WithAmount(u.Balance(addr)).
			WithTargetLock(addr),
		)
		require.NoError(t, err)
		t.Logf("tx size = %d bytes", len(txBytes))

		err = u.TransferTokens(privKey, addr, u.Balance(addr))
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-total, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, total, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))
	})
	t.Run("utxodb too many inputs", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl.Fmt(pub))
		t.Logf("origin address: %s", easyfl.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		const howMany = 400

		total := uint64(0)
		numOuts := 0
		for i := uint64(100); i <= howMany; i++ {
			//st := time.Now()
			err := u.TokensFromFaucet(addr, i)
			require.NoError(t, err)
			//t.Logf("%d elapsed: %v", i, time.Since(st))
			total += i
			numOuts++

			require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
			require.EqualValues(t, u.Supply()-u.FaucetBalance()-total, u.Balance(u.GenesisControllerAddress()))
			require.EqualValues(t, total, u.Balance(addr))
			require.EqualValues(t, numOuts, u.NumUTXOs(addr))
		}
		err := u.TransferTokens(privKey, addr, u.Balance(addr))
		util.RequireErrorWith(t, err, "exceeded max number of consumed outputs")
	})
	t.Run("utxodb fan out outputs", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl.Fmt(pub))
		t.Logf("origin address: %s", easyfl.Fmt(u.GenesisControllerAddress()))

		privKey0, _, addr0 := u.GenerateAddress(0)
		const howMany = 100
		err := u.TokensFromFaucet(addr0, howMany*100)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-howMany*100, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, howMany*100, int(u.Balance(addr0)))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))

		privKey1, _, addr1 := u.GenerateAddress(1)

		for i := 0; i < howMany; i++ {
			err = u.TransferTokens(privKey0, addr1, 100)
			require.NoError(t, err)
		}
		require.EqualValues(t, howMany*100, int(u.Balance(addr1)))
		require.EqualValues(t, howMany, u.NumUTXOs(addr1))
		require.EqualValues(t, 0, u.Balance(addr0))
		require.EqualValues(t, 0, u.NumUTXOs(addr0))

		outs, err := u.StateReader().GetUTXOsLockedInAccount(addr1.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, howMany, len(outs))

		err = u.TransferTokens(privKey1, addr0, howMany*100)
		require.EqualValues(t, howMany*100, u.Balance(addr0))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))
		require.EqualValues(t, 0, u.Balance(addr1))
		require.EqualValues(t, 0, u.NumUTXOs(addr1))

		outs, err = u.StateReader().GetUTXOsLockedInAccount(addr0.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(outs))

		//snd, ok := outs[0].Output.Sender()
		//require.True(t, ok)
		//require.EqualValues(t, addr1, snd)
	})
	t.Run("multi faucet", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		_, _, addrs := u.GenerateAddressesWithFaucetAmount(1, 255, 10_000)
		for i := range addrs {
			require.EqualValues(t, 10000, u.Balance(addrs[i]))
		}
	})
}

func TestManyInputs(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	const (
		numAddr    = 256
		initAmount = 10_000
	)
	privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)
	privKey0, _, addr0 := u.GenerateAddress(0)
	require.EqualValues(t, 0, u.NumUTXOs(addr0))

	for i := range addrs {
		err := u.TransferTokens(privKeys[i], addr0, initAmount)
		require.NoError(t, err)
	}
	require.EqualValues(t, numAddr*initAmount, u.Balance(addr0))
	require.EqualValues(t, numAddr, u.NumUTXOs(addr0))

	tx, err := u.TransferTokensReturnTx(privKey0, addr0, numAddr*initAmount)
	require.NoError(t, err)

	require.EqualValues(t, numAddr, tx.NumInputs())
	require.EqualValues(t, numAddr*initAmount, tx.TotalAmount())

	require.EqualValues(t, 1, u.NumUTXOs(addr0))
}

func TestChainSuccTransaction(t *testing.T) {
	t.Run("wrong input parameters", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 2
			initAmount = 100_000_000_000
		)
		privKeys, _, _ := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)

		chainInput, err := u.CreateChainOrigin(privKeys[0], ledger.TimeNow())
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ledger.TimeNow())
		require.NoError(t, err)
		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            ledger.TimeNow().AddSlots(1),
			EnforceProfitability: true,
			TargetFee:            100,
			Target:               ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.NoError(t, err)

		par.Timestamp = ledger.NewLedgerTime(100000, 0)
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.Error(t, err, "should fail  because wrong timestamp")
		par.Timestamp = ledger.TimeNow().AddSlots(1)

		par.ChainInput = nil
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.Error(t, err, "should fail because ChainInput is nil")
		par.ChainInput = chainInput

		par.TargetFee = 1000000000000
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.Error(t, err, "should fail because target fee too high")
		par.TargetFee = 100
	})
	t.Run("normal run", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 2
			initAmount = 100_000_000_000
		)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)

		chainInput, err := u.CreateChainOrigin(privKeys[0], ledger.TimeNow())
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ledger.TimeNow())
		require.NoError(t, err)

		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            ledger.TimeNow().AddSlots(1),
			EnforceProfitability: true,
			TargetFee:            100,
			Target:               ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		txBytes, _, err := txbuilder.MakeChainSuccTransaction(&par)
		require.NoError(t, err)
		err = u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
			if err != nil {
				return fmt.Errorf("Error: %v\n%s", err, ctx.String())
			}
			return nil
		})
		require.NoError(t, err)
		require.EqualValues(t, initAmount+300, u.Balance(addrs[0]))
		require.EqualValues(t, initAmount, u.Balance(addrs[1]))
	})
	t.Run("test enforce profitability", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 256
			initAmount = 10_000 // low amount so inflation == 0
		)
		privKeys, _, _ := u.GenerateAddressesWithFaucetAmount(1, 2, initAmount)

		chainInput, err := u.CreateChainOrigin(privKeys[0], ledger.TimeNow())
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ledger.TimeNow())
		require.NoError(t, err)
		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            ledger.TimeNow().AddSlots(1),
			EnforceProfitability: false,
			TargetFee:            100,
			Target:               ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.NoError(t, err)

		par.EnforceProfitability = true
		_, _, err = txbuilder.MakeChainSuccTransaction(&par)
		require.Error(t, err, "should fail because not profitable")
	})
}
