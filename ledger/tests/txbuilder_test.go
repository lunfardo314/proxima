package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestBasics(t *testing.T) {
	t.Run("utxodb 1", func(t *testing.T) {
		//transaction.SetPrintEasyFLTraceOnFail(true)

		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl_util.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl_util.Fmt(pub))
		t.Logf("origin address: %s", easyfl_util.Fmt(u.GenesisControllerAddress()))

		t.Logf("current timestamp: %s", ledger.TimeNow().String())
		_, _, addr := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))

		_, stemOutData := u.StateReader().GetStem()

		stemOut, _, _, err := ledger.OutputFromBytesMain(stemOutData)
		require.NoError(t, err)
		require.EqualValues(t, 0, stemOut.TokenBalance())
		_, ok := stemOut.StemLock()
		require.True(t, ok)

	})
	t.Run("utxodb 2", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl_util.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl_util.Fmt(pub))
		t.Logf("origin address: %s", easyfl_util.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)
		err = u.TokensFromFaucet(addr)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000-ledger.DefaultStorageDeposit(), u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000+ledger.DefaultStorageDeposit(), u.Balance(addr))
		require.EqualValues(t, 2, u.NumUTXOs(addr))

		err = u.TransferTokens(privKey, addr, u.Balance(addr))
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-1_000_000_000-u.FaucetBalance()-ledger.DefaultStorageDeposit(), u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000+ledger.DefaultStorageDeposit(), u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))
	})
	t.Run("utxodb 3 compress outputs", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl_util.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl_util.Fmt(pub))
		t.Logf("origin address: %s", easyfl_util.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		const howMany = 256

		total := uint64(0)
		numOuts := 0
		for i := 1; i <= howMany; i++ {
			s := uint64(100_000_000 + i)
			err := u.TokensFromFaucet(addr, s)
			require.NoError(t, err)
			total += s
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
		t.Logf("orig priv key: %s", easyfl_util.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl_util.Fmt(pub))
		t.Logf("origin address: %s", easyfl_util.Fmt(u.GenesisControllerAddress()))

		privKey, _, addr := u.GenerateAddress(0)
		const howMany = 400

		total := uint64(0)
		numOuts := 0
		for i := 0; i <= howMany; i++ {
			s := uint64(100_000_000 + i)
			err := u.TokensFromFaucet(addr, s)
			require.NoError(t, err)
			total += s
			numOuts++

			require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
			require.EqualValues(t, u.Supply()-u.FaucetBalance()-total, u.Balance(u.GenesisControllerAddress()))
			require.EqualValues(t, total, u.Balance(addr))
			require.EqualValues(t, numOuts, u.NumUTXOs(addr))
		}
		err := u.TransferTokens(privKey, addr, u.Balance(addr))
		util.RequireErrorWithOld(t, err, "exceeded max number of consumed outputs")
	})
	t.Run("utxodb fan out outputs", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		priv, pub := u.GenesisKeys()
		t.Logf("orig priv key: %s", easyfl_util.Fmt(priv))
		t.Logf("orig pub key: %s", easyfl_util.Fmt(pub))
		t.Logf("origin address: %s", easyfl_util.Fmt(u.GenesisControllerAddress()))

		privKey0, _, addr0 := u.GenerateAddress(0)
		const (
			howMany = 100
			amount  = 100_000_000
		)
		err := u.TokensFromFaucet(addr0, howMany*amount)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, int(u.Supply()-u.FaucetBalance()-howMany*amount), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, howMany*amount, int(u.Balance(addr0)))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))

		privKey1, _, addr1 := u.GenerateAddress(1)

		for i := 0; i < howMany; i++ {
			err = u.TransferTokens(privKey0, addr1, amount)
			require.NoError(t, err)
		}
		require.EqualValues(t, howMany*amount, int(u.Balance(addr1)))
		require.EqualValues(t, howMany, u.NumUTXOs(addr1))
		require.EqualValues(t, 0, u.Balance(addr0))
		require.EqualValues(t, 0, u.NumUTXOs(addr0))

		outs, err := u.StateReader().GetUTXOsInAccount(addr1.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, howMany, len(outs))

		err = u.TransferTokens(privKey1, addr0, howMany*amount)
		require.EqualValues(t, howMany*amount, u.Balance(addr0))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))
		require.EqualValues(t, 0, u.Balance(addr1))
		require.EqualValues(t, 0, u.NumUTXOs(addr1))

		outs, err = u.StateReader().GetUTXOsInAccount(addr0.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(outs))
	})
	t.Run("multi faucet", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		_, _, addrs := u.GenerateAddressesWithFaucetAmount(1, 255, 100_000_000)
		for i := range addrs {
			require.EqualValues(t, 100_000_000, u.Balance(addrs[i]))
		}
	})
}

func TestManyInputs(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	const (
		numAddr    = 256
		initAmount = 1_000_000_000
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

func TestChainSuccessorTransaction(t *testing.T) {
	t.Run("wrong input parameters", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 2
			initAmount = 100_000_000_000
		)
		privKeys, _, _ := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)

		ts := ledger.TimeNow()
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(10)
		}
		chainInput, err := u.CreateChainOrigin(privKeys[0], ts.AddSlots(1))
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ts.AddSlots(1))
		require.NoError(t, err)
		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            chainInput.Timestamp().AddSlots(1),
			EnforceProfitability: true,
			WithdrawAmount:       100_000_000,
			WithdrawTarget:       ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)

		par.Timestamp = base.T(100000, 0)
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, util.MustErrorWith(err, "timestamp is on slot boundary"))

		par.Timestamp = par.ChainInput.Timestamp()
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		util.RequireErrorWithOld(t, err, "is inconsistent with latest chain output timestamp")

	})
	t.Run("normal run", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 2
			initAmount = 100_000_000_000
			fee        = 300
		)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)

		ts := ledger.TimeNow()
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(10)
		}
		chainInput, err := u.CreateChainOrigin(privKeys[0], ts.AddSlots(1))
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ts.AddSlots(1))
		require.NoError(t, err)

		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            chainInput.Timestamp().AddSlots(1),
			EnforceProfitability: true,
			WithdrawAmount:       fee,
			WithdrawTarget:       ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		txBytes, inflation, _, err := txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)
		err = u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
			if err != nil {
				return fmt.Errorf("Error: %v\n%s", err, ctx.String())
			}
			return nil
		})
		require.NoError(t, err)
		require.EqualValues(t, util.Th(initAmount+inflation-fee), util.Th(u.Balance(addrs[0])))
		require.EqualValues(t, initAmount, u.Balance(addrs[1]))
	})
	t.Run("test enforce profitability", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			initAmount = 100_000_000_000
			fee        = 100
		)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, 2, initAmount)

		ts := ledger.TimeNow()
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(10)
		}
		chainInput, err := u.CreateChainOrigin(privKeys[0], ts.AddSlots(1))
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ts.AddSlots(1))
		require.NoError(t, err)
		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            chainInput.Timestamp().AddSlots(1),
			EnforceProfitability: false,
			WithdrawAmount:       fee,
			WithdrawTarget:       ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		_, inflationAmount, _, err := txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)

		par.WithdrawAmount = inflationAmount
		_, inflationAmount1, _, err := txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)
		require.EqualValues(t, inflationAmount, inflationAmount1)

		par.WithdrawAmount = inflationAmount + initAmount + fee
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		util.RequireErrorWithOld(t, err, "not enough tokens")

		par.WithdrawAmount = inflationAmount + initAmount - 200
		_, inflationAmount1, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)
		require.EqualValues(t, inflationAmount, inflationAmount1)

		par.WithdrawAmount = inflationAmount + 1
		par.EnforceProfitability = true
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		util.RequireErrorWithOld(t, err, "not profitable")

		par.WithdrawAmount = inflationAmount
		par.EnforceProfitability = true
		txBytes, _, _, err := txbuilder.MakeChainSuccessorTransaction(&par)
		require.NoError(t, err)

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
		require.EqualValues(t, initAmount, u.Balance(addrs[0]))

		lockedOnChain, _, err := u.BalanceOnChain(target.ChainID)
		require.NoError(t, err)

		require.EqualValues(t, inflationAmount, lockedOnChain)
	})
	t.Run("small amount", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 2
			initAmount = 1_000_000_000
			fee        = 50
			slots      = 10
		)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)
		_ = addrs
		ts := ledger.TimeNow()
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(10)
		}
		chainInput, err := u.CreateChainOrigin(privKeys[0], ts.AddSlots(1))
		require.NoError(t, err)

		target, err := u.CreateChainOrigin(privKeys[1], ts.AddSlots(1))
		require.NoError(t, err)

		par := txbuilder.MakeChainSuccTransactionParams{
			ChainInput:           chainInput,
			Timestamp:            chainInput.Timestamp().AddSlots(slots),
			EnforceProfitability: true,
			WithdrawAmount:       fee,
			WithdrawTarget:       ledger.ChainLockFromChainID(target.ChainID),
			PrivateKey:           privKeys[0],
		}
		_, _, _, err = txbuilder.MakeChainSuccessorTransaction(&par)
		util.RequireErrorWithOld(t, err, "chain transition is not profitable")
		//require.NoError(t, err)
		//profit := int64(inflation) - fee
		//t.Logf("inflation of %s tokens over %d slots is %s, profit is %s",
		//	util.Th(chainInput.Output.TokenBalance()), slots, util.Th(inflation), util.Th(profit))
		//
		//err = u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
		//	if err != nil {
		//		return fmt.Errorf("Error: %v\n%s", err, ctx.String())
		//	}
		//	return nil
		//})
		//require.NoError(t, err)
		//require.EqualValues(t, util.Th(initAmount+inflation-fee), util.Th(u.Balance(addrs[0])))
		//require.EqualValues(t, initAmount, u.Balance(addrs[1]))
	})
	t.Run("benchmark tx validation", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		const (
			numAddr    = 100
			initAmount = 100_000_000
		)
		privKeys, _, addrs := u.GenerateAddressesWithFaucetAmount(1, numAddr, initAmount)

		type txWithInputLoader struct {
			txBytes     []byte
			inputLoader func(i byte) (*ledger.Output, error)
		}

		txs := make([]txWithInputLoader, numAddr)

		ts := ledger.TimeNow()
		if ts.IsSlotBoundary() {
			ts = ts.AddTicks(10)
		}
		for i := range privKeys {
			require.EqualValues(t, initAmount, u.Balance(addrs[i]))
			ts := ts.AddSlots(1)
			if ts.IsSlotBoundary() {
				ts = ts.AddTicks(10)
			}
			chainOrig, err := u.CreateChainOrigin(privKeys[i], ts)
			require.NoError(t, err)

			if chainOrig.Timestamp().IsSlotBoundary() {
				ts = chainOrig.Timestamp().AddTicks(10).AddSlots(1)
			} else {
				ts = chainOrig.Timestamp().AddSlots(1)
			}
			par := txbuilder.MakeChainSuccTransactionParams{
				ChainInput:        chainOrig,
				Timestamp:         ts,
				PrivateKey:        privKeys[i],
				ReturnInputLoader: true,
			}
			txBytes, _, inputLoader, err := txbuilder.MakeChainSuccessorTransaction(&par)
			require.NoError(t, err)

			txs[i] = txWithInputLoader{
				txBytes:     txBytes,
				inputLoader: inputLoader,
			}
		}

		start := time.Now()
		for i := range txs {
			tx, err := transaction.FromBytes(txs[i].txBytes, transaction.MainTxValidationOptions...)
			require.NoError(t, err)

			txCtx, err := transaction.TxContextFromTransaction(tx, txs[i].inputLoader)
			require.NoError(t, err)

			err = txCtx.Validate()
			require.NoError(t, err)
		}
		elapsed := time.Since(start)
		elapsedMillis := float64(elapsed) / float64(time.Millisecond)
		t.Logf("time elapsed: %v", elapsed)
		t.Logf("time per tx: %.2f ms", elapsedMillis/float64(numAddr))
	})
}
