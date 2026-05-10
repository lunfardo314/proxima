package tests

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util"
	"github.com/stretchr/testify/require"
)

func TestOutput(t *testing.T) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	pubKey, _, err := ed25519.GenerateKey(rnd)
	require.NoError(t, err)

	t.Run("basic", func(t *testing.T) {
		out := ledger.OutputBasic(0, ledger.SigLock{})
		outBack, err := ledger.OutputFromBytes(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("empty output: %d bytes", len(out.Bytes()))
	})
	t.Run("address", func(t *testing.T) {
		addr := ledger.SigLockFromED25519PublicKey(pubKey)
		t.Logf("address: %s", addr.String())
		t.Logf("address hex: 0x%s", hex.EncodeToString(addr[:]))
		out := ledger.OutputBasic(0, ledger.SigLockFromED25519PublicKey(pubKey))
		outBack, err := ledger.OutputFromBytes(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("output: %d bytes", len(out.Bytes()))
		t.Logf("output:\n%s", out.Lines().String())

		// Round-trip: parsed lock must be a SigLock with the same holder.
		parsed, ok := outBack.Lock().(ledger.SigLock)
		require.True(t, ok)
		require.EqualValues(t, addr, parsed)
	})
	t.Run("tokens", func(t *testing.T) {
		out := ledger.OutputBasic(1337, ledger.SigLock{})
		outBack, err := ledger.OutputFromBytes(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("output: %d bytes", len(out.Bytes()))

		tokensBack := outBack.TokenBalance()
		require.EqualValues(t, 1337, tokensBack)
	})
}

func TestMainConstraints(t *testing.T) {
	t.Run("faucet", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		_, _, addr := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, int(u.Supply()-u.FaucetBalance()-1_000_000_000), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))
	})
	t.Run("simple transfer", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		in, err := u.MakeTransferInputData(privKey1, nil, base.NilLedgerTime)
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(100_000_000))
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000-100_000_000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))
		require.EqualValues(t, 100_000_000, u.Balance(addrNext))
		require.EqualValues(t, 1, u.NumUTXOs(addrNext))
	})
	t.Run("transfer wrong key", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		privKeyWrong, _, _ := u.GenerateAddress(3)
		in, err := u.MakeTransferInputData(privKey1, nil, base.NilLedgerTime)
		in.SenderPrivateKey = privKeyWrong
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(100_000_000))
		require.NoError(t, util.MustErrorWith(err, "failed"))
	})
	t.Run("not enough deposit", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		in, err := u.MakeTransferInputData(privKey1, nil, base.NilLedgerTime)
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(1))
		require.NoError(t, util.MustErrorWith(err, "storage deposit not met"))
	})
}

func TestTxID(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	privKey0, _, addr0 := u.GenerateAddress(0)
	err := u.TokensFromFaucet(addr0, 1_000_000_000)
	require.NoError(t, err)

	_, _, addr1 := u.GenerateAddress(1)

	ts := ledger.TimeNow()
	t.Logf("now ts: %s", ts)
	par, err := u.MakeTransferInputData(privKey0, nil, ts)
	require.NoError(t, err)

	timelockSlot := ts.Slot + 1

	par.WithAmount(200).
		WithTargetLock(addr1).
		WithConstraint(ledger.NewTimelock(timelockSlot))
	par.AdjustToMinimum = true
	txBytes, err := txbuilder.MakeTransferTransaction(par)
	require.NoError(t, err)

	ctx, err := u.TxFullContextFromBytes(txBytes)
	require.NoError(t, err)

	lib := ledger.L(0)
	txID := ctx.ID()
	dctx := lib.NewGlobalDataTracePrint(ledger.NewEvalContext(ctx))
	res, err := lib.EvalFromSource(dctx, "atPath(pathToSequencerDataBytes)")
	require.NoError(t, err)
	require.EqualValues(t, 0, len(res))

	// taking txid from the embedded function
	res, err = lib.EvalFromSource(dctx, "txID")
	require.NoError(t, err)

	require.EqualValues(t, txID[:], res)

	// direct computation of the txid in EasyFL
	const directTxID = `
      concat(
         if(
            isSequencerTransaction, 
            bitwiseOR(txTimestampBytes, 0x0000000001), 
            txTimestampBytes
         ), 
         byte(sub(numProducedOutputs,1), 7), 
         slice(blake2b(
            concat(
              atPath(pathToTxVersion),
              atPath(pathToTimestamp),
              atPath(pathToSequencerDataBytes),
              atPath(pathToInputCommitment),
		  	  atPath(pathToExplicitBaseline),
              atPath(pathToInputIDs), 
              atPath(pathToUnlockParams),
              atPath(pathToProducedOutputs), 
              atPath(pathToEndorsements),
              atPath(pathToTxConstraints), 
              atPath(pathToOtherData)
            )
         ),6,31))
`
	res, err = lib.EvalFromSource(dctx, directTxID)
	require.NoError(t, err)

	require.EqualValues(t, txID[:], res)
}

func TestTimelock(t *testing.T) {
	t.Run("time lock 1", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 20_000_000_000)
		require.NoError(t, err)

		priv1, _, addr1 := u.GenerateAddress(1)

		ts := ledger.TimeNow()
		t.Logf("now ts: %s", ts)
		par, err := u.MakeTransferInputData(privKey0, nil, ts)
		require.NoError(t, err)

		timelockSlot := ts.Slot + 1

		par.WithAmount(200_000_000).
			WithTargetLock(addr1).
			WithConstraint(ledger.NewTimelock(timelockSlot))
		txBytes, err := txbuilder.MakeTransferTransaction(par)
		require.NoError(t, err)

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
		t.Logf("200 timelocked until slot %d in addr1", timelockSlot)

		require.EqualValues(t, 200_000_000, u.Balance(addr1))

		timelockSlot = ts.Slot + (1 + 10)
		par, err = u.MakeTransferInputData(privKey0, nil, ts.AddSlots(1))
		require.NoError(t, err)
		par.WithAmount(200_000_000).
			WithTargetLock(addr1).
			WithConstraint(ledger.NewTimelock(timelockSlot))
		err = u.DoTransfer(par)
		require.NoError(t, err)
		t.Logf("2000 timelocked until slot %d in addr1", timelockSlot)

		// total 400_000_000, but with different time locks
		require.EqualValues(t, 400_000_000, int(u.Balance(addr1)))

		txTs := ts.AddSlots(2)
		par, err = u.MakeTransferInputData(priv1, nil, txTs)
		require.NoError(t, err)
		t.Logf("AdditionalInputs: \n%s\n", ledger.OutputsWithIDToString(par.Inputs...))

		err = u.DoTransfer(par.
			WithAmount(400_000_000).
			WithTargetLock(addr0),
		)

		require.NoError(t, util.MustErrorWith(err, "timelock(", "failed"))
		require.EqualValues(t, 400000000, int(u.Balance(addr1))) // funds weren't moved
		t.Logf("failed tx with ts %s", par.Timestamp)

		txTs = ts.AddSlots(14)
		require.True(t, txTs.Slot > timelockSlot)
		par, err = u.MakeTransferInputData(priv1, nil, txTs)
		require.NoError(t, err)
		t.Logf("tx time: %s", par.Timestamp)
		txBytes, err = u.DoTransferTx(par.
			WithAmount(350_000_000).
			WithTargetLock(addr0),
		)
		if err != nil {
			tx, err1 := transaction.ParseWithPartialValidation(txBytes)
			require.NoError(t, err1)
			t.Logf("resulting tx ts: %s", tx.Timestamp())
			require.True(t, tx.Timestamp().Slot > timelockSlot)
		}
		require.NoError(t, err)
		require.EqualValues(t, 50_000_000, int(u.Balance(addr1)))
	})
	t.Run("time lock 2", func(t *testing.T) {
		u := utxodb.NewUTXODB(genesisPrivateKey, true)

		privKey0, _, addr0 := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 800_000_000)
		require.NoError(t, err)

		priv1, _, addr1 := u.GenerateAddress(1)

		ts := ledger.TimeNow()
		par, err := u.MakeTransferInputData(privKey0, nil, ts)
		require.NoError(t, err)
		txBytes, err := txbuilder.MakeTransferTransaction(par.
			WithAmount(30_000_000).
			WithTargetLock(addr1).
			WithConstraint(ledger.NewTimelock(ts.Slot + 1)),
		)
		require.NoError(t, err)
		t.Logf("tx with timelock len: %d", len(txBytes))
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, 30_000_000, int(u.Balance(addr1)))

		par, err = u.MakeTransferInputData(privKey0, nil, ts.AddSlots(1))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(30_000_000).
			WithTargetLock(addr1).
			WithConstraint(ledger.NewTimelock(ts.Slot + 11)),
		)
		require.NoError(t, err)

		require.EqualValues(t, 60_000_000, int(u.Balance(addr1)))

		par, err = u.MakeTransferInputData(priv1, nil, ts.AddSlots(2))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(40_000_000).
			WithTargetLock(addr0),
		)
		require.NoError(t, util.MustErrorWith(err, "failed"))
		require.EqualValues(t, 60_000_000, int(u.Balance(addr1)))

		par, err = u.MakeTransferInputData(priv1, nil, ts.AddSlots(12))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(14_000_000).
			WithTargetLock(addr0),
		)
		require.NoError(t, err)
		require.EqualValues(t, 46_000_000, int(u.Balance(addr1)))
	})
}

func TestChain1(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 ledger.SigLock
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 1_000_000_000)
		require.NoError(t, err)
	}
	initTest2 := func() []*ledger.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow().AddSlots(1))
		outs, err := u.DoTransferOutputs(par.
			WithAmount(30_000_000).
			WithTargetLock(addr0).
			WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, int(u.Supply()-u.FaucetBalance()-1_000_000_000), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, 1_000_000_000, int(u.Balance(addr0)))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := ledger.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	t.Run("compile", func(t *testing.T) {
		const source = "chain(0x0000000000000000000000000000000000000000000000000000000000000000, 0x, z32/1000, 0x, 0x, 0x, 0x)"
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(source)
		require.NoError(t, err)
		origBytecode := ledger.NewChainOrigin(1000).Bytes()
		require.EqualValues(t, origBytecode, code)
	})
	t.Run("create origin ok", func(t *testing.T) {
		initTest2()
	})
	t.Run("create origin ok 2", func(t *testing.T) {
		initTest()

		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow().AddSlots(1))
		err = u.DoTransfer(par.
			WithAmount(100_000_000).
			WithTargetLock(addr0).
			WithConstraintBinary(ledger.NewChainOrigin(par.Timestamp.Slot).Bytes()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, int(u.Supply()-u.FaucetBalance()-1_000_000_000), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, 1_000_000_000, int(u.Balance(addr0)))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
	})
	t.Run("create origin twice in the same output", func(t *testing.T) {
		initTest()

		// With chain constraint enforcing index 2, a second chain at index 3 is now rejected

		// First get inputs with a placeholder timestamp
		par, err := u.MakeTransferInputData(privKey0, nil, base.NilLedgerTime)
		require.NoError(t, err)
		// Derive timestamp from actual inputs to avoid timing race (see CLAUDE.local.md)
		inputTs := par.Inputs[0].Timestamp()
		par.Timestamp = inputTs.AddTicks(int(ledger.L(inputTs.Slot).TransactionPace))
		if par.Timestamp.IsSlotBoundary() {
			par.Timestamp = par.Timestamp.AddTicks(1)
		}
		code := ledger.NewChainOrigin(par.Timestamp.Slot).Bytes()
		err = u.DoTransfer(par.
			WithAmount(60_000_000).
			WithTargetLock(addr0).
			WithConstraintBinary(code).
			WithConstraintBinary(code),
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "chain constraint must be at index 2")
	})
	t.Run("create origin wrong 1", func(t *testing.T) {
		initTest()

		const source = "chain(0x0001, 0x0102, 1, 5, 6, 7, 3)"
		_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(source)
		require.NoError(t, err)

		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow())
		par.WithAmount(2000).WithTargetLock(addr0)

		err = u.DoTransfer(par.WithConstraintBinary(code))
		require.Error(t, err)

		err = u.DoTransfer(par.WithConstraintBinary(bytes.Repeat([]byte{0}, 35)))
		require.Error(t, err)

		err = u.DoTransfer(par.WithConstraintBinary(nil))
		require.Error(t, err)
	})
	t.Run("create origin indexer", func(t *testing.T) {
		chains := initTest2()
		require.EqualValues(t, 1, len(chains))
		chs, err := u.StateReader().GetUTXOForChainID(chains[0].ChainID)
		require.NoError(t, err)
		o, err := ledger.OutputFromBytes(chs.Data)
		require.NoError(t, err)
		ch := o.ChainConstraint()
		require.NotNil(t, ch)
		require.True(t, ch.IsOrigin())
		t.Logf("chain created: %s", easyfl_util.Fmt(chains[0].ChainID[:]))
	})
	t.Run("create-destroy", func(t *testing.T) {
		// creates and immediately destroys chain output
		chains := initTest2()
		require.EqualValues(t, 1, len(chains))
		chainID := chains[0].ChainID
		// find chain output by chain id. It must be exactly one in the state
		// chs is unparsed data
		chs, err := u.StateReader().GetUTXOForChainID(chainID)
		require.NoError(t, err)

		// parse raw output data
		chainIN, err := chs.Parse()
		require.NoError(t, err)
		// get chain constraint from the output
		// It is expected to be origin
		ch := chainIN.Output.ChainConstraint()
		require.NotNil(t, ch)
		require.True(t, ch.IsOrigin())
		t.Logf("chain created: %s", easyfl_util.Fmt(chains[0].ChainID[:]))

		// add ticks to output timestamp to have valid timestamp of the next transaction
		ts := chainIN.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))

		// create transaction builder
		txb := txbuilder.New()
		// consume predecessor chain output. It will be the only input to the transaction
		consumedIndex, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
		require.NoError(t, err)

		// produce new output with same amount but without chain constraint
		// it will be the only produced output of the transaction
		outNonChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(chainIN.Output.TokenBalance())).
				WithLock(chainIN.Output.Lock())
		})
		_, err = txb.ProduceOutput(outNonChain)
		require.NoError(t, err)

		// we put empty unlock parameters for the chain constraint in the predecessor output
		// It makes the chain constraint script of the consumed output not to enforce produced successor,
		// as in the usual chain transition from predecessor to successor. With this chain is discontinued.
		txb.PutUnlockParams(consumedIndex, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

		// put unlock parameters for the chain controller lock. It is locked with usual sig lock
		// The signature unlock of the address25519 constraint just refers to the signature at the
		// transaction level, which is always valid. The address25519 script check if address data
		// is equal to the blake2b hash of the public key in the signature
		txb.PutSignatureUnlock(consumedIndex) // it knows the lock is always at index 1

		// finalize the transaction
		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(privKey0)

		txbytes := txb.TransactionData.Bytes()
		err = u.AddTransaction(txbytes)
		require.NoError(t, err)

		_, err = u.StateReader().GetUTXOForChainID(chainID)
		require.True(t, errors.Is(err, multistate.ErrNotFound))

		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, int(u.Supply()-u.FaucetBalance()-1_000_000_000), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, 1_000_000_000, int(u.Balance(addr0)))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))

		// it does not matter that chainID in the consumed output is all 0.
		// Here we create chain origin and immediately destroy it
		t.Logf("---- single consumed output (input #0):\n%s", chainIN.Output.Lines("   ").String())
		t.Logf("---- single produced output #0:\n%s", outNonChain.Lines("   ").String())
	})
}

func TestChain2(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 ledger.SigLock
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 1_000_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr0))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))
	}
	initTest2 := func() []*ledger.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow().AddSlots(1))
		outs, err := u.DoTransferOutputs(par.
			WithAmount(200_000_000).
			WithTargetLock(addr0).
			WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := ledger.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	runOption := func(optionConstraint, optionUnlock int) (string, error) {
		chains := initTest2()
		require.EqualValues(t, 1, len(chains))
		theChainData := chains[0]
		chainID := theChainData.ChainID
		chs, err := u.StateReader().GetUTXOForChainID(chainID)
		require.NoError(t, err)

		chainIN, err := chs.Parse()
		require.NoError(t, err)

		cc := chainIN.Output.ChainConstraint()
		require.NotNil(t, cc)

		ts := chainIN.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
		txb := txbuilder.New()
		predIdx, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
		require.NoError(t, err)

		var nextChainConstraint *ledger.ChainConstraint
		// options of making it wrong
		switch optionConstraint {
		case 0:
			// good
			nextChainConstraint = ledger.NewChainConstraint(theChainData.ChainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
		case 1:
			// wrong predecessor input index
			nextChainConstraint = ledger.NewChainConstraint(theChainData.ChainID, 0xff, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)
		case 4:
			// wrong origin slot
			nextChainConstraint = ledger.NewChainConstraint(theChainData.ChainID, predIdx, cc.OriginSlot+1, 0, 0, cc.TransitionCounter+1, 0)
		case 5:
			// wrong transition counter
			nextChainConstraint = ledger.NewChainConstraint(theChainData.ChainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+99, 0)
		default:
			panic("wrong test option 1")
		}

		chainOut := chainIN.Output.Clone(func(out *ledger.OutputBuilder) {
			out.PutConstraint(nextChainConstraint.Bytes(), ledger.ConstraintIndexChain)
		})

		succIdx, err := txb.ProduceOutput(chainOut)
		require.NoError(t, err)

		// options of wrong unlock params
		switch optionUnlock {
		case 0:
			// good
			txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
		case 1:
			// wrong successor output index
			txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, []byte{0xff})
		default:
			panic("wrong test option 2")
		}
		txb.PutSignatureUnlock(0)

		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)

		txb.SignED25519(privKey0)

		txBytes, _, txString, err := txb.BytesWithValidation()
		if err != nil {
			t.Logf("\n---- error: %v", err)
			return txString, err
		}
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		_, err = u.StateReader().GetUTXOForChainID(chainID)
		require.NoError(t, err)

		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_000_000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		return txString, nil
	}
	const printTx = false
	prn := func(str string) {
		if printTx {
			t.Logf("---------------------------\n%s", str)
		}
	}

	t.Run("transit 0,0", func(t *testing.T) {
		txString, err := runOption(0, 0)
		prn(txString)
		require.NoError(t, err)
	})
	t.Run("transit 1,0", func(t *testing.T) {
		txString, err := runOption(1, 0)
		prn(txString)
		require.NoError(t, util.MustErrorWith(err, "successor reference crosscheck failed"))
	})
	// Cases 2,0 and 3,0 removed: they tested wrong constraint index in chain constraint,
	// which is now always implicit (ConstraintIndexChain=2).
	t.Run("transit 4,0", func(t *testing.T) {
		txString, err := runOption(4, 0)
		prn(txString)
		require.NoError(t, util.MustErrorWith(err, "origin slot mismatch"))
	})
	t.Run("transit 5,0", func(t *testing.T) {
		txString, err := runOption(5, 0)
		prn(txString)
		require.NoError(t, util.MustErrorWith(err, "wrong transition counter"))
	})
	t.Run("transit 0,1", func(t *testing.T) {
		txString, err := runOption(0, 1)
		prn(txString)
		require.NoError(t, util.MustErrorWith(err, "index is out of range"))
	})
	// Cases 0,2 and 0,3 removed: they tested wrong constraint index in unlock params,
	// which is now always implicit (ConstraintIndexChain=2).
}

func TestChain3(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 ledger.SigLock
	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 1_000_0000_000)
		require.NoError(t, err)
	}
	initTest2 := func() []*ledger.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow().AddSlots(1))
		outs, err := u.DoTransferOutputs(par.
			WithAmount(200_000_000).
			WithTargetLock(addr0).
			WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_0000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_0000_000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := ledger.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	chains := initTest2()
	require.EqualValues(t, 1, len(chains))
	theChainData := chains[0]
	chainID := theChainData.ChainID
	chs, err := u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)

	chainIN, err := chs.Parse()
	require.NoError(t, err)

	cc := chainIN.Output.ChainConstraint()
	require.NotNil(t, cc)

	ts := chainIN.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb := txbuilder.New()
	predIdx, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
	require.NoError(t, err)

	nextChainConstraint := ledger.NewChainConstraint(theChainData.ChainID, predIdx, cc.OriginSlot, 0, 0, cc.TransitionCounter+1, 0)

	chainOut := chainIN.Output.Clone(func(out *ledger.OutputBuilder) {
		out.PutConstraint(nextChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	})
	succIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(succIdx))
	txb.PutSignatureUnlock(0)

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)

	txb.SignED25519(privKey0)

	txBytes, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		t.Logf("error: %v\n---------------------------\n%s", err, txString)
	}

	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	_, err = u.StateReader().GetUTXOForChainID(chainID)
	require.NoError(t, err)

	require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
	require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_0000_000, u.Balance(u.GenesisControllerAddress()))
	require.EqualValues(t, 1_000_0000_000, u.Balance(addr0))
	require.EqualValues(t, 2, u.NumUTXOs(addr0))

}

func TestChainLock(t *testing.T) {
	var privKey0, privKey1 ed25519.PrivateKey
	var addr0, addr1 ledger.SigLock
	var u *utxodb.UTXODB
	var chainID base.ChainID
	var chainAddr ledger.ChainLock

	initTest := func() {
		u = utxodb.NewUTXODB(genesisPrivateKey, true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 1_000_0000_000)
		require.NoError(t, err)
	}
	initTest2 := func() *ledger.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, ledger.TimeNow().AddSlots(1))
		outs, err := u.DoTransferOutputs(par.
			WithAmount(200_000_000).
			WithTargetLock(addr0).
			WithConstraint(ledger.NewChainOrigin(par.Timestamp.Slot)),
		)
		require.NoError(t, err)
		require.EqualValues(t, 2, u.NumUTXOs(u.GenesisControllerAddress())) // sequencer output + controller dust output
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-1_000_0000_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 1_000_0000_000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := ledger.FilterChainOutputs(outs)
		require.NoError(t, err)
		require.EqualValues(t, 1, len(chains))

		chainID = chains[0].ChainID
		chainAddr = ledger.ChainLockFromChainID(chainID)
		require.NoError(t, err)
		require.EqualValues(t, chainID, chainAddr.ChainID())

		onLocked, onChainOut, err := u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 0, onLocked)
		require.EqualValues(t, 200_000_000, onChainOut)

		_, err = u.StateReader().GetUTXOForChainID(chainID)
		require.NoError(t, err)

		privKey1, _, addr1 = u.GenerateAddress(1)
		err = u.TokensFromFaucet(addr1, 200_000_000)
		require.NoError(t, err)
		require.EqualValues(t, 200_000_000, u.Balance(addr1))
		return chains[0]
	}
	sendFun := func(amount uint64, ts base.LedgerTime) {
		par, err := u.MakeTransferInputData(privKey1, nil, ts)
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(amount).
			WithTargetLock(chainAddr),
		)
		require.NoError(t, err)
	}
	t.Run("send", func(t *testing.T) {
		initTest2()
		require.EqualValues(t, 200_000_000, u.Balance(addr1))

		ts := ledger.TimeNow().AddTicks(5)

		sendFun(50_000_000, ts)
		sendFun(60_000_000, ts.AddTicks(1))
		require.EqualValues(t, 90_000_000, int(u.Balance(addr1)))
		require.EqualValues(t, 110_000_000, int(u.Balance(chainAddr)))
		require.EqualValues(t, 2, u.NumUTXOs(chainAddr))

		onLocked, onChainOut, err := u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 110_000_000, int(onLocked))
		require.EqualValues(t, 200_000_000, int(onChainOut))

		outs, err := u.StateReader().GetUTXOsForController(chainAddr.ControllerID())
		require.NoError(t, err)
		require.EqualValues(t, 2, len(outs))

		require.EqualValues(t, 10_000_000_000, int(u.Balance(addr0)))
		par, err := u.MakeTransferInputData(privKey0, chainAddr, ts)
		par.WithAmount(40_000_000).WithTargetLock(addr0)
		require.NoError(t, err)
		txBytes, err := txbuilder.MakeTransferTransaction(par)
		require.NoError(t, err)

		v, err := u.TxFullContextFromBytes(txBytes)
		require.NoError(t, err)
		t.Logf("\n%s", v.String())

		require.EqualValues(t, 10_000_000_000, int(u.Balance(addr0)))
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		onLocked, onChainOut, err = u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 60_000_000, int(onLocked))
		require.EqualValues(t, 210_000_000, int(onChainOut))
		require.EqualValues(t, 10_050_000_000, int(u.Balance(addr0))) // also includes 500 on chain
	})
}

func TestGGG(t *testing.T) {
	lib := ledger.L(base.MaxSlot)
	t.Logf("now = %d", uint32(time.Now().Unix()))
	loc, err := time.LoadLocation("UTC")
	require.NoError(t, err)
	jan1 := time.Date(2023, 1, 1, 0, 0, 0, 0, loc)
	t.Logf("Jan 1, 2023 UTC = %d", uint32(jan1.Unix()))

	_, _, bin, err := lib.CompileExpression("sigLock")
	require.NoError(t, err)
	prefix, err := lib.ParsePrefixBytecode(bin)
	require.NoError(t, err)
	t.Logf("bin = %s, prefix = %s", hex.EncodeToString(bin), hex.EncodeToString(prefix))
}
