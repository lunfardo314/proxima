package noworkflow_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	state "github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

func TestOutput(t *testing.T) {
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))

	pubKey, _, err := ed25519.GenerateKey(rnd)
	require.NoError(t, err)

	const msg = "message to be signed"

	t.Run("basic", func(t *testing.T) {
		out := core.OutputBasic(0, core.AddressED25519Null())
		outBack, err := core.OutputFromBytesReadOnly(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("empty output: %d bytes", len(out.Bytes()))
	})
	t.Run("address", func(t *testing.T) {
		out := core.OutputBasic(0, core.AddressED25519FromPublicKey(pubKey))
		outBack, err := core.OutputFromBytesReadOnly(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("output: %d bytes", len(out.Bytes()))

		_, err = core.AddressED25519FromBytes(outBack.Lock().Bytes())
		require.NoError(t, err)
		require.EqualValues(t, out.Lock(), outBack.Lock())
	})
	t.Run("tokens", func(t *testing.T) {
		out := core.OutputBasic(1337, core.AddressED25519Null())
		outBack, err := core.OutputFromBytesReadOnly(out.Bytes())
		require.NoError(t, err)
		require.EqualValues(t, outBack.Bytes(), out.Bytes())
		t.Logf("output: %d bytes", len(out.Bytes()))

		tokensBack := outBack.Amount()
		require.EqualValues(t, 1337, tokensBack)
	})
}

func TestMainConstraints(t *testing.T) {
	t.Run("faucet", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)
		_, _, addr := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr, 10_000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10_000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10_000, u.Balance(addr))
		require.EqualValues(t, 1, u.NumUTXOs(addr))
	})
	t.Run("simple transfer", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 10000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		in, err := u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(1000))
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000-1000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))
		require.EqualValues(t, 1000, u.Balance(addrNext))
		require.EqualValues(t, 1, u.NumUTXOs(addrNext))
	})
	t.Run("transfer wrong key", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 10000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		privKeyWrong, _, _ := u.GenerateAddress(3)
		in, err := u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
		in.SenderPrivateKey = privKeyWrong
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(1000))
		easyfl.RequireErrorWith(t, err, "addressED25519 unlock failed")
	})
	t.Run("not enough deposit", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)
		privKey1, _, addr1 := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr1, 10000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr1))
		require.EqualValues(t, 1, u.NumUTXOs(addr1))

		_, _, addrNext := u.GenerateAddress(2)
		in, err := u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
		require.NoError(t, err)
		err = u.DoTransfer(in.WithTargetLock(addrNext).WithAmount(1))
		easyfl.RequireErrorWith(t, err, "amount is smaller than expected")
	})
}

func TestTimelock(t *testing.T) {
	t.Run("time lock 1", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)
		privKey0, _, addr0 := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)

		priv1, _, addr1 := u.GenerateAddress(1)

		ts := core.LogicalTimeNow()
		t.Logf("now ts: %s", ts)
		par, err := u.MakeTransferInputData(privKey0, nil, ts)
		require.NoError(t, err)

		timelockEpoch := ts.TimeSlot() + 1

		par.WithAmount(200).
			WithTargetLock(addr1).
			WithConstraint(core.NewTimelock(timelockEpoch))
		txBytes, err := txbuilder.MakeTransferTransaction(par)
		require.NoError(t, err)

		err = u.AddTransaction(txBytes)
		require.NoError(t, err)
		t.Logf("200 timelocked until epoch %d in addr1", timelockEpoch)

		require.EqualValues(t, 200, u.Balance(addr1))

		timelockEpoch = ts.TimeSlot() + (1 + 10)
		par, err = u.MakeTransferInputData(privKey0, nil, ts.AddTimeSlots(1))
		require.NoError(t, err)
		par.WithAmount(2000).
			WithTargetLock(addr1).
			WithConstraint(core.NewTimelock(timelockEpoch))
		err = u.DoTransfer(par)
		require.NoError(t, err)
		t.Logf("2000 timelocked until epoch %d in addr1", timelockEpoch)

		// total 2200, but with different timelocks
		require.EqualValues(t, 2200, u.Balance(addr1))

		txTs := ts.AddTimeSlots(2)
		par, err = u.MakeTransferInputData(priv1, nil, txTs)
		require.NoError(t, err)
		t.Logf("AdditionalInputs: \n%s\n", core.OutputsWithIdToString(par.Inputs...))

		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr0),
		)

		util.RequireErrorWith(t, err, "timelock(", "failed")
		require.EqualValues(t, 2200, u.Balance(addr1)) // funds weren't moved
		t.Logf("failed tx with ts %s", par.Timestamp)

		txTs = ts.AddTimeSlots(14)
		require.True(t, txTs.TimeSlot() > timelockEpoch)
		par, err = u.MakeTransferInputData(priv1, nil, txTs)
		require.NoError(t, err)
		t.Logf("tx time: %s", par.Timestamp)
		txBytes, err = u.DoTransferTx(par.
			WithAmount(2000).
			WithTargetLock(addr0),
		)
		if err != nil {
			tx, err1 := state.TransactionFromBytesAllChecks(txBytes)
			require.NoError(t, err1)
			t.Logf("resulting tx ts: %s", tx.Timestamp())
			require.True(t, tx.Timestamp().TimeSlot() > timelockEpoch)
		}
		require.NoError(t, err)
		require.EqualValues(t, 200, u.Balance(addr1))
	})
	t.Run("time lock 2", func(t *testing.T) {
		u := utxodb.NewUTXODB(true)

		privKey0, _, addr0 := u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)

		priv1, _, addr1 := u.GenerateAddress(1)

		ts := core.LogicalTimeNow()
		par, err := u.MakeTransferInputData(privKey0, nil, ts)
		require.NoError(t, err)
		txBytes, err := txbuilder.MakeTransferTransaction(par.
			WithAmount(200).
			WithTargetLock(addr1).
			WithConstraint(core.NewTimelock(ts.TimeSlot() + 1)),
		)
		require.NoError(t, err)
		t.Logf("tx with timelock len: %d", len(txBytes))
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		require.EqualValues(t, 200, u.Balance(addr1))

		par, err = u.MakeTransferInputData(privKey0, nil, ts.AddTimeSlots(1))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr1).
			WithConstraint(core.NewTimelock(ts.TimeSlot() + 11)),
		)
		require.NoError(t, err)

		require.EqualValues(t, 2200, u.Balance(addr1))

		par, err = u.MakeTransferInputData(priv1, nil, ts.AddTimeSlots(2))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr0),
		)
		easyfl.RequireErrorWith(t, err, "failed")
		require.EqualValues(t, 2200, u.Balance(addr1))

		par, err = u.MakeTransferInputData(priv1, nil, ts.AddTimeSlots(12))
		require.NoError(t, err)
		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr0),
		)
		require.NoError(t, err)
		require.EqualValues(t, 200, u.Balance(addr1))
	})
}

func TestDeadlineLock(t *testing.T) {
	u := utxodb.NewUTXODB(true)
	privKey0, pubKey0, addr0 := u.GenerateAddress(0)
	err := u.TokensFromFaucet(addr0, 10000)
	require.NoError(t, err)

	_, pubKey1, addr1 := u.GenerateAddress(1)
	require.EqualValues(t, 0, u.Balance(addr1))
	require.EqualValues(t, 0, u.NumUTXOs(addr1))

	ts := core.LogicalTimeNow()

	par, err := u.MakeTransferInputData(privKey0, nil, ts)
	require.NoError(t, err)
	deadlineLock := core.NewDeadlineLock(
		ts.AddTimeSlots(10),
		core.AddressED25519FromPublicKey(pubKey1),
		core.AddressED25519FromPublicKey(pubKey0),
	)
	t.Logf("deadline lock: %d bytes", len(deadlineLock.Bytes()))
	dis, err := easyfl.DecompileBytecode(deadlineLock.Bytes())
	require.NoError(t, err)
	t.Logf("disassemble deadlock %s", dis)
	_, err = u.DoTransferTx(par.
		WithAmount(2000).
		WithTargetLock(deadlineLock),
	)
	require.NoError(t, err)

	require.EqualValues(t, 2, u.NumUTXOs(addr0))
	require.EqualValues(t, 10000, u.Balance(addr0))

	require.EqualValues(t, 1, u.NumUTXOs(addr0, ts.AddTimeSlots(9)))
	require.EqualValues(t, 2, u.NumUTXOs(addr0, ts.AddTimeSlots(11)))
	require.EqualValues(t, 8000, int(u.Balance(addr0, ts.AddTimeSlots(9))))
	require.EqualValues(t, 10000, int(u.Balance(addr0, ts.AddTimeSlots(11))))

	require.EqualValues(t, 1, u.NumUTXOs(addr1))
	require.EqualValues(t, 1, u.NumUTXOs(addr1, ts.AddTimeSlots(9)))
	require.EqualValues(t, 0, u.NumUTXOs(addr1, ts.AddTimeSlots(11)))
	require.EqualValues(t, 2000, int(u.Balance(addr1, ts.AddTimeSlots(9))))
	require.EqualValues(t, 0, int(u.Balance(addr1, ts.AddTimeSlots(11))))
}

func TestSenderAddressED25519(t *testing.T) {
	u := utxodb.NewUTXODB(true)
	privKey0, _, addr0 := u.GenerateAddress(0)
	err := u.TokensFromFaucet(addr0, 10000)
	require.NoError(t, err)

	_, _, addr1 := u.GenerateAddress(1)
	require.EqualValues(t, 0, u.Balance(addr1))
	require.EqualValues(t, 0, u.NumUTXOs(addr1))

	par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
	err = u.DoTransfer(par.
		WithAmount(2000).
		WithTargetLock(addr1).
		WithSender(),
	)
	require.NoError(t, err)

	require.EqualValues(t, 1, u.NumUTXOs(addr1))
	require.EqualValues(t, 2000, u.Balance(addr1))

	outDatas, err := u.StateReader().GetUTXOsLockedInAccount(addr1.AccountID())
	require.NoError(t, err)
	outs, err := txutils.ParseAndSortOutputData(outDatas, nil)
	require.NoError(t, err)

	require.EqualValues(t, 1, len(outs))
	saddr, ok := outs[0].Output.SenderAddressED25519()
	require.True(t, ok)
	require.True(t, core.EqualConstraints(addr0, saddr))
}

func TestChain1(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 core.AddressED25519
	initTest := func() {
		u = utxodb.NewUTXODB(true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)
	}
	initTest2 := func() []*core.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		outs, err := u.DoTransferOutputs(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraint(core.NewChainOrigin()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := txutils.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	t.Run("compile", func(t *testing.T) {
		const source = "chain(originChainData)"
		_, _, _, err := easyfl.CompileExpression(source)
		require.NoError(t, err)
	})
	t.Run("create origin ok", func(t *testing.T) {
		initTest2()
	})
	t.Run("create origin ok 2", func(t *testing.T) {
		initTest()

		const source = "chain(originChainData)"
		_, _, code, err := easyfl.CompileExpression(source)
		require.NoError(t, err)

		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraintBinary(code),
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
	})
	t.Run("create origin twice in same output", func(t *testing.T) {
		initTest()

		const source = "chain(originChainData)"
		_, _, code, err := easyfl.CompileExpression(source)
		require.NoError(t, err)

		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		err = u.DoTransfer(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraintBinary(code).
			WithConstraintBinary(code),
		)
		easyfl.RequireErrorWith(t, err, "duplicated constraints")
	})
	t.Run("create origin wrong 1", func(t *testing.T) {
		initTest()

		const source = "chain(0x0001)"
		_, _, code, err := easyfl.CompileExpression(source)
		require.NoError(t, err)

		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
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
		chs, err := u.StateReader().GetUTXOForChainID(&chains[0].ChainID)
		require.NoError(t, err)
		o, err := core.OutputFromBytesReadOnly(chs.OutputData)
		require.NoError(t, err)
		ch, idx := o.ChainConstraint()
		require.True(t, idx != 0xff)
		require.True(t, ch.IsOrigin())
		t.Logf("chain created: %s", easyfl.Fmt(chains[0].ChainID[:]))
	})
	t.Run("create-destroy", func(t *testing.T) {
		chains := initTest2()
		require.EqualValues(t, 1, len(chains))
		chainID := chains[0].ChainID
		chs, err := u.StateReader().GetUTXOForChainID(&chainID)
		require.NoError(t, err)

		chainIN, err := chs.Parse()
		require.NoError(t, err)
		ch, predecessorConstraintIndex := chainIN.Output.ChainConstraint()
		require.True(t, predecessorConstraintIndex != 0xff)
		require.True(t, ch.IsOrigin())
		t.Logf("chain created: %s", easyfl.Fmt(chains[0].ChainID[:]))

		ts := chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)

		txb := txbuilder.NewTransactionBuilder()
		consumedIndex, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
		require.NoError(t, err)
		outNonChain := core.NewOutput(func(o *core.Output) {
			o.WithAmount(chainIN.Output.Amount()).
				WithLock(chainIN.Output.Lock())
		})
		_, err = txb.ProduceOutput(outNonChain)
		require.NoError(t, err)

		txb.Transaction.Timestamp = ts
		txb.Transaction.InputCommitment = txb.InputCommitment()

		txb.PutUnlockParams(consumedIndex, predecessorConstraintIndex, []byte{0xff, 0xff, 0xff})
		txb.PutSignatureUnlock(consumedIndex)
		txb.SignED25519(privKey0)

		txbytes := txb.Transaction.Bytes()
		err = u.AddTransaction(txbytes)
		require.NoError(t, err)

		_, err = u.StateReader().GetUTXOForChainID(&chainID)
		easyfl.RequireErrorWith(t, err, "has not been found")

		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
	})
}

func TestChain2(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 core.AddressED25519
	initTest := func() {
		u = utxodb.NewUTXODB(true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 1, u.NumUTXOs(addr0))
	}
	initTest2 := func() []*core.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		outs, err := u.DoTransferOutputs(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraint(core.NewChainOrigin()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := txutils.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	runOption := func(option1, option2 int) error {
		chains := initTest2()
		require.EqualValues(t, 1, len(chains))
		theChainData := chains[0]
		chainID := theChainData.ChainID
		chs, err := u.StateReader().GetUTXOForChainID(&chainID)
		require.NoError(t, err)

		chainIN, err := chs.Parse()
		require.NoError(t, err)

		_, constraintIdx := chainIN.Output.ChainConstraint()
		require.True(t, constraintIdx != 0xff)

		ts := chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
		txb := txbuilder.NewTransactionBuilder()
		predIdx, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
		require.NoError(t, err)

		var nextChainConstraint *core.ChainConstraint
		// options of making it wrong
		switch option1 {
		case 0:
			// good
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, constraintIdx, 0)
		case 1:
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, 0xff, constraintIdx, 0)
		case 2:
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, 0xff, 0)
		case 3:
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, 0xff, 0xff, 0)
		case 4:
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, constraintIdx, 1)
		case 5:
			nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, 0xff, 0xff, 0xff)
		default:
			panic("wrong test option 1")
		}

		chainOut := chainIN.Output.Clone(func(out *core.Output) {
			out.PutConstraint(nextChainConstraint.Bytes(), constraintIdx)
		})

		succIdx, err := txb.ProduceOutput(chainOut)
		require.NoError(t, err)

		// options of wrong unlock params
		switch option2 {
		case 0:
			// good
			txb.PutUnlockParams(predIdx, constraintIdx, []byte{succIdx, constraintIdx, 0})
		case 1:
			txb.PutUnlockParams(predIdx, constraintIdx, []byte{0xff, constraintIdx, 0})
		case 2:
			txb.PutUnlockParams(predIdx, constraintIdx, []byte{succIdx, 0xff, 0})
		case 3:
			txb.PutUnlockParams(predIdx, constraintIdx, []byte{0xff, 0xff, 0})
		case 4:
			txb.PutUnlockParams(predIdx, constraintIdx, []byte{succIdx, constraintIdx, 1})
		default:
			panic("wrong test option 2")
		}
		txb.PutSignatureUnlock(0)

		txb.Transaction.Timestamp = ts
		txb.Transaction.InputCommitment = txb.InputCommitment()

		txb.SignED25519(privKey0)

		txbytes := txb.Transaction.Bytes()
		err = u.AddTransaction(txbytes)
		if err != nil {
			return err
		}

		_, err = u.StateReader().GetUTXOForChainID(&chainID)
		require.NoError(t, err)

		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		return nil
	}
	t.Run("transit 0,0", func(t *testing.T) {
		err := runOption(0, 0)
		require.NoError(t, err)
	})
	t.Run("transit 1,0", func(t *testing.T) {
		err := runOption(1, 0)
		require.Error(t, err)
	})
	t.Run("transit 2,0", func(t *testing.T) {
		err := runOption(2, 0)
		require.Error(t, err)
	})
	t.Run("transit 3,0", func(t *testing.T) {
		err := runOption(3, 0)
		require.Error(t, err)
	})
	t.Run("transit 4,0", func(t *testing.T) {
		err := runOption(4, 0)
		require.Error(t, err)
	})
	t.Run("transit 5,0", func(t *testing.T) {
		err := runOption(5, 0)
		require.Error(t, err)
	})
	t.Run("transit 0,1", func(t *testing.T) {
		err := runOption(0, 1)
		require.Error(t, err)
	})
	t.Run("transit 0,2", func(t *testing.T) {
		err := runOption(0, 2)
		require.Error(t, err)
	})
	t.Run("transit 0,3", func(t *testing.T) {
		err := runOption(0, 3)
		require.Error(t, err)
	})
	t.Run("transit 0,4", func(t *testing.T) {
		err := runOption(0, 4)
		require.Error(t, err)
	})
	t.Run("transit 4,4", func(t *testing.T) {
		err := runOption(4, 4)
		require.NoError(t, err)
	})
}

func TestChain3(t *testing.T) {
	var privKey0 ed25519.PrivateKey
	var u *utxodb.UTXODB
	var addr0 core.AddressED25519
	initTest := func() {
		u = utxodb.NewUTXODB(true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)
	}
	initTest2 := func() []*core.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		outs, err := u.DoTransferOutputs(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraint(core.NewChainOrigin()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := txutils.FilterChainOutputs(outs)
		require.NoError(t, err)
		return chains
	}
	chains := initTest2()
	require.EqualValues(t, 1, len(chains))
	theChainData := chains[0]
	chainID := theChainData.ChainID
	chs, err := u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	chainIN, err := chs.Parse()
	require.NoError(t, err)

	_, constraintIdx := chainIN.Output.ChainConstraint()
	require.True(t, constraintIdx != 0xff)

	ts := chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb := txbuilder.NewTransactionBuilder()
	predIdx, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
	require.NoError(t, err)

	var nextChainConstraint *core.ChainConstraint
	nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, constraintIdx, 0)

	chainOut := chainIN.Output.Clone(func(out *core.Output) {
		out.PutConstraint(nextChainConstraint.Bytes(), constraintIdx)
	})
	succIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, constraintIdx, []byte{succIdx, constraintIdx, 0})
	txb.PutSignatureUnlock(0)

	txb.Transaction.Timestamp = ts
	txb.Transaction.InputCommitment = txb.InputCommitment()

	txb.SignED25519(privKey0)

	txbytes := txb.Transaction.Bytes()
	err = u.AddTransaction(txbytes)
	require.NoError(t, err)

	_, err = u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
	require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
	require.EqualValues(t, 10000, u.Balance(addr0))
	require.EqualValues(t, 2, u.NumUTXOs(addr0))
}

func TestChainLock(t *testing.T) {
	var privKey0, privKey1 ed25519.PrivateKey
	var addr0, addr1 core.AddressED25519
	var u *utxodb.UTXODB
	var chainID core.ChainID
	var chainAddr core.ChainLock

	initTest := func() {
		u = utxodb.NewUTXODB(true)
		privKey0, _, addr0 = u.GenerateAddress(0)
		err := u.TokensFromFaucet(addr0, 10000)
		require.NoError(t, err)
	}
	initTest2 := func() *core.OutputWithChainID {
		initTest()
		par, err := u.MakeTransferInputData(privKey0, nil, core.LogicalTimeNow())
		outs, err := u.DoTransferOutputs(par.
			WithAmount(2000).
			WithTargetLock(addr0).
			WithConstraint(core.NewChainOrigin()),
		)
		require.NoError(t, err)
		require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
		require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
		require.EqualValues(t, 10000, u.Balance(addr0))
		require.EqualValues(t, 2, u.NumUTXOs(addr0))
		require.EqualValues(t, 2, len(outs))
		chains, err := txutils.FilterChainOutputs(outs)
		require.NoError(t, err)
		require.EqualValues(t, 1, len(chains))

		chainID = chains[0].ChainID
		chainAddr = core.ChainLockFromChainID(chainID)
		require.NoError(t, err)
		require.EqualValues(t, chainID, chainAddr.ChainID())

		onLocked, onChainOut, err := u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 0, onLocked)
		require.EqualValues(t, 2000, onChainOut)

		_, err = u.StateReader().GetUTXOForChainID(&chainID)
		require.NoError(t, err)

		privKey1, _, addr1 = u.GenerateAddress(1)
		err = u.TokensFromFaucet(addr1, 20000)
		require.NoError(t, err)
		require.EqualValues(t, 20000, u.Balance(addr1))
		return chains[0]
	}
	sendFun := func(amount uint64, ts core.LogicalTime) {
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
		require.EqualValues(t, 20000, u.Balance(addr1))

		ts := core.LogicalTimeNow().AddTimeTicks(5)

		sendFun(1000, ts)
		sendFun(2000, ts.AddTimeTicks(1))
		require.EqualValues(t, 20000-3000, int(u.Balance(addr1)))
		require.EqualValues(t, 3000, u.Balance(chainAddr))
		require.EqualValues(t, 2, u.NumUTXOs(chainAddr))

		onLocked, onChainOut, err := u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 3000, onLocked)
		require.EqualValues(t, 2000, onChainOut)

		outs, err := u.StateReader().GetUTXOsLockedInAccount(chainAddr.AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 2, len(outs))

		require.EqualValues(t, 10_000, int(u.Balance(addr0)))
		par, err := u.MakeTransferInputData(privKey0, chainAddr, ts)
		par.WithAmount(500).WithTargetLock(addr0)
		require.NoError(t, err)
		txBytes, err := txbuilder.MakeTransferTransaction(par)
		require.NoError(t, err)

		v, err := u.ValidationContextFromTransaction(txBytes)
		require.NoError(t, err)
		t.Logf("%s", v.String())

		require.EqualValues(t, 10_000, int(u.Balance(addr0)))
		err = u.AddTransaction(txBytes)
		require.NoError(t, err)

		onLocked, onChainOut, err = u.BalanceOnChain(chainID)
		require.NoError(t, err)
		require.EqualValues(t, 2_000, int(onLocked))
		require.EqualValues(t, 2_500, int(onChainOut))
		require.EqualValues(t, 11_000, int(u.Balance(addr0))) // also includes 500 on chain
	})
}

func TestLocalLibrary(t *testing.T) {
	const source = `
 func fun1 : concat($0,$1)
 func fun2 : fun1(fun1($0,$1), fun1($0,$1))
 func fun3 : fun2($0, $0)
`
	libBin, err := core.CompileLocalLibrary(source)
	require.NoError(t, err)
	t.Run("1", func(t *testing.T) {
		src := fmt.Sprintf("callLocalLibrary(0x%s, 2, 5)", hex.EncodeToString(libBin))
		t.Logf("src = '%s', len = %d", src, len(libBin))
		easyfl.MustEqual(src, "0x05050505")
	})
	t.Run("2", func(t *testing.T) {
		src := fmt.Sprintf("callLocalLibrary(0x%s, 0, 5, 6)", hex.EncodeToString(libBin))
		t.Logf("src = '%s', len = %d", src, len(libBin))
		easyfl.MustEqual(src, "0x0506")
	})
	t.Run("3", func(t *testing.T) {
		src := fmt.Sprintf("callLocalLibrary(0x%s, 1, 5, 6)", hex.EncodeToString(libBin))
		t.Logf("src = '%s', len = %d", src, len(libBin))
		easyfl.MustEqual(src, "0x05060506")
	})
	t.Run("4", func(t *testing.T) {
		src := fmt.Sprintf("callLocalLibrary(0x%s, 3)", hex.EncodeToString(libBin))
		t.Logf("src = '%s', len = %d", src, len(libBin))
		easyfl.MustError(src)
	})
}

func TestHashUnlock(t *testing.T) {
	const secretUnlockScript = "func fun1: and" // fun1 always returns true
	libBin, err := core.CompileLocalLibrary(secretUnlockScript)
	require.NoError(t, err)
	t.Logf("library size: %d", len(libBin))
	libHash := blake2b.Sum256(libBin)
	t.Logf("library hash: %s", easyfl.Fmt(libHash[:]))

	u := utxodb.NewUTXODB(true)
	privKey0, _, addr0 := u.GenerateAddress(0)
	err = u.TokensFromFaucet(addr0, 10000)
	require.NoError(t, err)

	constraintSource := fmt.Sprintf("or(isPathToProducedOutput(@),callLocalLibrary(selfHashUnlock(0x%s), 0))", hex.EncodeToString(libHash[:]))
	_, _, constraintBin, err := easyfl.CompileExpression(constraintSource)
	require.NoError(t, err)
	t.Logf("constraint source: %s", constraintSource)
	t.Logf("constraint size: %d", len(constraintBin))

	par, err := u.MakeTransferInputData(privKey0, nil, core.NilLogicalTime)
	require.NoError(t, err)
	constr := core.NewGeneralScript(constraintBin)
	t.Logf("constraint: %s", constr)
	par.WithAmount(1000).
		WithTargetLock(addr0).
		WithConstraint(constr)
	txbytes, err := txbuilder.MakeTransferTransaction(par)
	require.NoError(t, err)

	ctx, err := state.TransactionContextFromTransferableBytes(txbytes, u.StateReader().GetUTXO)
	require.NoError(t, err)

	t.Logf("%s", ctx.String())
	outs, err := u.DoTransferOutputs(par)
	require.NoError(t, err)

	outs = txutils.FilterOutputsSortByAmount(outs, func(o *core.Output) bool {
		return o.Amount() == 1000
	})

	// produce transaction without providing hash unlocking library for the output with script
	par = txbuilder.NewTransferData(privKey0, addr0, core.NilLogicalTime)
	par.MustWithInputs(outs...).
		WithAmount(1000).
		WithTargetLock(addr0)

	txbytes, err = txbuilder.MakeTransferTransaction(par)
	require.NoError(t, err)

	ctx, err = state.TransactionContextFromTransferableBytes(txbytes, u.StateReader().GetUTXO)
	require.NoError(t, err)

	t.Logf("---- transaction without hash unlock: FAILING\n %s", ctx.String())
	err = u.DoTransfer(par)
	require.Error(t, err)

	// now adding unlock data the unlocking library/script
	par.WithUnlockData(0, core.ConstraintIndexFirstOptionalConstraint, libBin)

	txbytes, err = txbuilder.MakeTransferTransaction(par)
	require.NoError(t, err)

	ctx, err = state.TransactionContextFromTransferableBytes(txbytes, u.StateReader().GetUTXO)
	require.NoError(t, err)

	t.Logf("---- transaction with hash unlock, the library/script: SUCCESS\n %s", ctx.String())
	t.Logf("%s", ctx.String())
	err = u.DoTransfer(par)
	require.NoError(t, err)
}

func TestRoyalties(t *testing.T) {
	u := utxodb.NewUTXODB(true)
	privKey0, _, addr0 := u.GenerateAddress(0)
	err := u.TokensFromFaucet(addr0, 10000)
	require.NoError(t, err)

	privKey1, _, addr1 := u.GenerateAddress(1)
	in, err := u.MakeTransferInputData(privKey0, nil, core.NilLogicalTime)
	require.NoError(t, err)
	royaltiesConstraint := core.NewRoyalties(addr0, 500)
	royaltiesBytecode := core.NewGeneralScript(royaltiesConstraint.Bytes())
	in.WithTargetLock(addr1).
		WithAmount(1000).
		WithConstraint(royaltiesBytecode)

	txBytes, err := txbuilder.MakeTransferTransaction(in)
	require.NoError(t, err)

	//t.Debugf("tx1 = %s", u.TxToString(txBytes))

	err = u.AddTransaction(txBytes)
	require.NoError(t, err)

	require.NoError(t, err)
	require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
	require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
	require.EqualValues(t, 10000-1000, int(u.Balance(addr0)))
	require.EqualValues(t, 1000, int(u.Balance(addr1)))
	require.EqualValues(t, 1, u.NumUTXOs(addr1))
	require.EqualValues(t, 1000, u.Balance(addr1))
	require.EqualValues(t, 1, u.NumUTXOs(addr1))

	// fail because not sending royalties
	in, err = u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
	require.NoError(t, err)
	in.WithTargetLock(addr1).
		WithAmount(1000)
	txBytes, err = txbuilder.MakeTransferTransaction(in)
	require.NoError(t, err)
	//t.Debugf("tx2 = %s", u.TxToString(txBytes))
	err = u.AddTransaction(txBytes)
	easyfl.RequireErrorWith(t, err, "constraint 'royaltiesED25519' failed")

	// fail because unlock parameters not set properly
	in, err = u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
	require.NoError(t, err)
	in.WithTargetLock(addr0).
		WithAmount(1000)
	txBytes, err = txbuilder.MakeTransferTransaction(in)
	require.NoError(t, err)
	//t.Debugf("tx3 = %s", u.TxToString(txBytes))
	err = u.AddTransaction(txBytes)
	easyfl.RequireErrorWith(t, err, "constraint 'royaltiesED25519' failed")

	// success
	in, err = u.MakeTransferInputData(privKey1, nil, core.NilLogicalTime)
	require.NoError(t, err)
	in.WithTargetLock(addr0).
		WithAmount(1000).
		WithUnlockData(0, core.ConstraintIndexFirstOptionalConstraint, []byte{0})
	txBytes, err = txbuilder.MakeTransferTransaction(in)
	require.NoError(t, err)
	t.Logf("tx4 = %s", u.TxToString(txBytes))
	err = u.AddTransaction(txBytes)
	require.NoError(t, err)
}

func TestImmutable(t *testing.T) {
	u := utxodb.NewUTXODB(true)
	privKey, _, addr0 := u.GenerateAddress(0)
	err := u.TokensFromFaucet(addr0, 10000)
	require.NoError(t, err)

	// create origin chain
	par, err := u.MakeTransferInputData(privKey, nil, core.LogicalTimeNow())
	par.WithAmount(2000).
		WithTargetLock(addr0).
		WithConstraint(core.NewChainOrigin())
	txbytes, err := txbuilder.MakeTransferTransaction(par)
	require.NoError(t, err)
	t.Logf("tx1 = %s", u.TxToString(txbytes))

	outs, err := u.DoTransferOutputs(par)
	require.NoError(t, err)
	require.EqualValues(t, 1, u.NumUTXOs(u.GenesisControllerAddress()))
	require.EqualValues(t, u.Supply()-u.FaucetBalance()-10000, u.Balance(u.GenesisControllerAddress()))
	require.EqualValues(t, 10000, u.Balance(addr0))
	require.EqualValues(t, 2, u.NumUTXOs(addr0))
	require.EqualValues(t, 2, len(outs))
	chains, err := txutils.FilterChainOutputs(outs)
	require.NoError(t, err)

	theChainData := chains[0]
	chainID := theChainData.ChainID

	// -------------------------- make transition
	chs, err := u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	chainIN, err := chs.Parse()
	require.NoError(t, err)

	_, chainConstraintIdx := chainIN.Output.ChainConstraint()
	require.True(t, chainConstraintIdx != 0xff)

	ts := chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb := txbuilder.NewTransactionBuilder()
	predIdx, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
	require.NoError(t, err)

	var nextChainConstraint *core.ChainConstraint
	nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, chainConstraintIdx, 0)

	var dataConstraintIdx, immutableConstraintIdx byte
	chainOut := chainIN.Output.Clone(func(o *core.Output) {
		o.PutConstraint(nextChainConstraint.Bytes(), chainConstraintIdx)

		immutableData, err := core.NewGeneralScriptFromSource("concat(0x01020304030201)")
		require.NoError(t, err)
		// push data constraint
		dataConstraintIdx, err = o.PushConstraint(immutableData)
		require.NoError(t, err)
		// push immutable constraint
		immutableConstraintIdx, err = o.PushConstraint(core.NewImmutable(chainConstraintIdx, dataConstraintIdx).Bytes())
		require.NoError(t, err)
	})

	succIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, chainConstraintIdx, []byte{succIdx, chainConstraintIdx, 0})
	txb.PutSignatureUnlock(0)

	txb.Transaction.Timestamp = ts
	txb.Transaction.InputCommitment = txb.InputCommitment()

	txb.SignED25519(privKey)

	txbytes = txb.Transaction.Bytes()
	t.Logf("tx2 = %s", u.TxToString(txbytes))
	err = u.AddTransaction(txbytes)
	require.NoError(t, err)

	// -------------------------------- make transition #2
	chs, err = u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	chainIN, err = chs.Parse()
	require.NoError(t, err)

	_, chainConstraintIdx = chainIN.Output.ChainConstraint()
	require.True(t, chainConstraintIdx != 0xff)

	ts = chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb = txbuilder.NewTransactionBuilder()
	predIdx, err = txb.ConsumeOutput(chainIN.Output, chainIN.ID)
	require.NoError(t, err)

	nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, chainConstraintIdx, 0)

	chainOut = chainIN.Output.Clone()

	succIdx, err = txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, chainConstraintIdx, []byte{succIdx, chainConstraintIdx, 0})
	// skip immutable unlock
	txb.PutSignatureUnlock(0)

	txb.Transaction.Timestamp = ts
	txb.Transaction.InputCommitment = txb.InputCommitment()

	txb.SignED25519(privKey)

	txbytes = txb.Transaction.Bytes()
	t.Logf("tx3 = %s", u.TxToString(txbytes))
	err = u.AddTransaction(txbytes)

	// fails because wrong unlock parameters
	easyfl.RequireErrorWith(t, err, "'immutable' failed with error")

	// --------------------------------- transit with wrong immutable data
	chs, err = u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	chainIN, err = chs.Parse()
	require.NoError(t, err)

	_, chainConstraintIdx = chainIN.Output.ChainConstraint()
	require.True(t, chainConstraintIdx != 0xff)

	ts = chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb = txbuilder.NewTransactionBuilder()
	predIdx, err = txb.ConsumeOutput(chainIN.Output, chs.ID)
	require.NoError(t, err)

	nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, chainConstraintIdx, 0)

	chainOut = chainIN.Output.Clone(func(out *core.Output) {
		// put wrong data
		wrongImmutableData, err := core.NewGeneralScriptFromSource("concat(0x010203040302010000)")
		require.NoError(t, err)
		out.PutConstraint(wrongImmutableData.Bytes(), dataConstraintIdx)
	})
	succIdx, err = txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, chainConstraintIdx, []byte{succIdx, chainConstraintIdx, 0})
	// put correct unlock params
	txb.PutUnlockParams(predIdx, dataConstraintIdx, []byte{dataConstraintIdx, immutableConstraintIdx})

	// skip immutable unlock
	txb.PutSignatureUnlock(0)

	txb.Transaction.Timestamp = ts
	txb.Transaction.InputCommitment = txb.InputCommitment()

	txb.SignED25519(privKey)

	txbytes = txb.Transaction.Bytes()
	t.Logf("tx4 = %s", u.TxToString(txbytes))
	err = u.AddTransaction(txbytes)

	// fails because wrong unlock parameters
	easyfl.RequireErrorWith(t, err, "'immutable' failed with error")

	// put it all correct
	chs, err = u.StateReader().GetUTXOForChainID(&chainID)
	require.NoError(t, err)

	chainIN, err = chs.Parse()
	require.NoError(t, err)

	_, chainConstraintIdx = chainIN.Output.ChainConstraint()
	require.True(t, chainConstraintIdx != 0xff)

	ts = chainIN.Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb = txbuilder.NewTransactionBuilder()
	predIdx, err = txb.ConsumeOutput(chainIN.Output, chs.ID)
	require.NoError(t, err)

	nextChainConstraint = core.NewChainConstraint(theChainData.ChainID, predIdx, chainConstraintIdx, 0)

	chainOut = chainIN.Output.Clone(func(out *core.Output) {
		// put wrong data
		sameImmutableData, err := core.NewGeneralScriptFromSource("concat(0x01020304030201)")
		require.NoError(t, err)
		out.PutConstraint(sameImmutableData.Bytes(), dataConstraintIdx)
	})

	succIdx, err = txb.ProduceOutput(chainOut)
	require.NoError(t, err)

	txb.PutUnlockParams(predIdx, chainConstraintIdx, []byte{succIdx, chainConstraintIdx, 0})
	// put correct unlock params
	txb.PutUnlockParams(predIdx, immutableConstraintIdx, []byte{dataConstraintIdx, immutableConstraintIdx})

	// skip immutable unlock
	txb.PutSignatureUnlock(0)

	txb.Transaction.Timestamp = ts
	txb.Transaction.InputCommitment = txb.InputCommitment()

	txb.SignED25519(privKey)

	txbytes = txb.Transaction.Bytes()
	t.Logf("tx5 = %s", u.TxToString(txbytes))
	err = u.AddTransaction(txbytes)
	require.NoError(t, err)
}
func TestGGG(t *testing.T) {
	t.Logf("now = %d", uint32(time.Now().Unix()))
	loc, err := time.LoadLocation("UTC")
	require.NoError(t, err)
	jan1 := time.Date(2023, 1, 1, 0, 0, 0, 0, loc)
	t.Logf("Jan 1, 2023 UTC = %d", uint32(jan1.Unix()))

	_, _, bin, err := easyfl.CompileExpression("amount(u64/1337)")
	require.NoError(t, err)
	prefix, err := easyfl.ParseBytecodePrefix(bin)
	require.NoError(t, err)
	t.Logf("bin = %s, prefix = %s", hex.EncodeToString(bin), hex.EncodeToString(prefix))
}
