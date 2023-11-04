package sequencer

import (
	"crypto/ed25519"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/proxima/workflow"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap/zapcore"
)

const (
	initFaucetBalance  = 20_000_000
	initOnChainBalance = 5 * core.MinimumAmountOnSequencer // 2_000_000
	feeAmount          = 100
)

type sequencerTestData struct {
	t                           *testing.T
	stateIdentity               genesis.StateIdentityData
	originControllerPrivateKey  ed25519.PrivateKey
	originDistribution          []core.LockBalance
	faucetPrivateKeys           []ed25519.PrivateKey
	faucetAddresses             []core.AddressED25519
	faucetOutputs               []*core.OutputWithID
	chainControllersPrivateKeys []ed25519.PrivateKey
	chainControllersAddresses   []core.AddressED25519
	bootstrapChainID            core.ChainID
	distributionTxID            core.TransactionID
	chainOrigins                []*core.OutputWithChainID
	txChainOrigins              *transaction.Transaction
	ut                          *utangle.UTXOTangle
	wrk                         *workflow.Workflow
	bootstrapSeq                *sequencer.Sequencer
	sequencers                  []*sequencer.Sequencer
}

func TestMax(t *testing.T) {
	t.Logf("Max uint64 = %s", util.GoThousands(uint64(math.MaxUint64)))
}

func initSequencerTestData(t *testing.T, nFaucets, nAdditionalChains int, logicalNow core.LogicalTime, workflowOpt ...workflow.ConfigOption) *sequencerTestData {
	core.SetTimeTickDuration(20 * time.Millisecond)

	require.True(t, nFaucets >= 0)
	t.Logf("time tick duration: %v, time slot duration: %v", core.TimeTickDuration(), core.TimeSlotDuration())
	now := time.Now()
	t.Logf("now is: %v, %s", now.Format("04:05.00000"), core.LogicalTimeFromTime(now).String())
	t.Logf("logical now: %v, %s", logicalNow.Time().Format("04:05.00000"), logicalNow.String())
	ret := &sequencerTestData{t: t}
	ret.originControllerPrivateKey = testutil.GetTestingPrivateKey()
	ret.stateIdentity = *genesis.DefaultIdentityData(ret.originControllerPrivateKey)
	ret.originDistribution, ret.faucetPrivateKeys, ret.faucetAddresses =
		inittest.GenesisParamsWithPreDistribution(nFaucets, initFaucetBalance)

	stateStore := common.NewInMemoryKVStore()
	txStore := txstore.NewDummyTxBytesStore()

	ret.bootstrapChainID, _ = genesis.InitLedgerState(ret.stateIdentity, stateStore)
	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, ret.originControllerPrivateKey, ret.originDistribution)
	require.NoError(t, err)

	err = txStore.SaveTxBytes(txBytes)
	require.NoError(t, err)

	ret.ut = utangle.Load(stateStore, txStore)

	ret.distributionTxID, _, err = transaction.IDAndTimestampFromTransactionBytes(txBytes)
	require.NoError(t, err)

	stateReader := ret.ut.HeaviestStateForLatestTimeSlot()
	ret.faucetOutputs = make([]*core.OutputWithID, nFaucets)
	for i := range ret.faucetOutputs {
		outs, err := stateReader.GetOutputsForAccount(ret.faucetAddresses[i].AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(outs))
		ret.faucetOutputs[i] = outs[0]
	}

	ret.makeAdditionalChainOrigins(0, nAdditionalChains)

	t.Logf("state identity:\n%s", genesis.MustStateIdentityDataFromBytes(ret.ut.HeaviestStateForLatestTimeSlot().MustStateIdentityBytes()).String())
	ret.wrk = workflow.New(ret.ut, workflowOpt...)
	return ret
}

func (r *sequencerTestData) makeAdditionalChainOrigins(faucetIdx int, nChains int) {
	if nChains <= 0 {
		return
	}
	r.chainControllersPrivateKeys = testutil.GetTestingPrivateKeys(nChains)
	r.chainControllersAddresses = make([]core.AddressED25519, nChains)
	for i := range r.chainControllersAddresses {
		r.chainControllersAddresses[i] = core.AddressED25519FromPrivateKey(r.chainControllersPrivateKeys[i])
	}
	var err error

	txb := txbuilder.NewTransactionBuilder()
	_, err = txb.ConsumeOutputWithID(r.faucetOutputs[faucetIdx])
	require.NoError(r.t, err)
	txb.PutSignatureUnlock(0)

	ts := r.faucetOutputs[faucetIdx].Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)

	r.chainOrigins = make([]*core.OutputWithChainID, nChains)
	for i := range r.chainOrigins {
		o := core.NewOutput(func(o *core.Output) {
			o.WithAmount(initOnChainBalance).WithLock(r.chainControllersAddresses[i])
			_, err = o.PushConstraint(core.NewChainOrigin().Bytes())
			require.NoError(r.t, err)
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(r.t, err)
	}
	// fee output to the bootstrap chain and the remainder
	oFee := core.NewOutput(func(o *core.Output) {
		o.WithAmount(feeAmount).WithLock(r.bootstrapChainID.AsChainLock())
	})
	_, err = txb.ProduceOutput(oFee)
	require.NoError(r.t, err)

	consumedAmount := feeAmount + uint64(nChains)*initOnChainBalance
	require.True(r.t, initFaucetBalance > consumedAmount)

	oFaucetRemainder := core.NewOutput(func(o *core.Output) {
		o.WithAmount(initFaucetBalance - consumedAmount).
			WithLock(r.faucetAddresses[faucetIdx])
	})
	faucetRemainderIdx, err := txb.ProduceOutput(oFaucetRemainder)

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(r.faucetPrivateKeys[faucetIdx])

	txBytesChainOrigins := txb.TransactionData.Bytes()

	r.txChainOrigins, err = transaction.FromBytesMainChecksWithOpt(txBytesChainOrigins)
	require.NoError(r.t, err)

	r.t.Logf("chain origins transaction: %s", r.txChainOrigins.IDShort())
	r.txChainOrigins.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		out := core.OutputWithID{
			ID:     *oid,
			Output: o,
		}
		if int(idx) < nChains {
			chainID, ok := out.ExtractChainID()
			require.True(r.t, ok)
			r.chainOrigins[idx] = &core.OutputWithChainID{
				OutputWithID: out,
				ChainID:      chainID,
			}
			r.t.Logf(" --- chain ID %s @ origin %s", chainID.Short(), oid.Short())
		}
		return true
	})
	r.faucetOutputs[faucetIdx] = r.txChainOrigins.MustProducedOutputWithIDAt(faucetRemainderIdx)
}

func (r *sequencerTestData) allSequencerIDs() []core.ChainID {
	ret := make([]core.ChainID, len(r.chainOrigins)+1)
	ret[0] = r.bootstrapChainID
	for i := range r.chainOrigins {
		ret[i+1] = r.chainOrigins[i].ChainID
	}
	return ret
}

func (r *sequencerTestData) makeFaucetTransaction(targetSeqID core.ChainID, faucetIdx int, targetLock core.Lock, amount uint64) *transaction.Transaction {
	txb := txbuilder.NewTransactionBuilder()
	_, err := txb.ConsumeOutputWithID(r.faucetOutputs[faucetIdx])
	require.NoError(r.t, err)
	txb.PutSignatureUnlock(0)

	mainOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(amount).
			WithLock(targetLock)
	})
	_, err = txb.ProduceOutput(mainOut)
	require.NoError(r.t, err)

	feeOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(feeAmount).
			WithLock(targetSeqID.AsChainLock())
	})
	_, err = txb.ProduceOutput(feeOut)
	require.NoError(r.t, err)

	remainderOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(r.faucetOutputs[faucetIdx].Output.Amount() - amount - feeAmount).
			WithLock(r.faucetAddresses[faucetIdx])
	})
	remainderIdx, err := txb.ProduceOutput(remainderOut)
	require.NoError(r.t, err)

	txb.TransactionData.Timestamp = r.faucetOutputs[faucetIdx].Timestamp().AddTimeTicks(core.TransactionTimePaceInTicks)
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(r.faucetPrivateKeys[faucetIdx])

	tx, err := transaction.FromBytesMainChecksWithOpt(txb.TransactionData.Bytes())
	r.faucetOutputs[faucetIdx] = tx.MustProducedOutputWithIDAt(remainderIdx)
	//r.t.Logf("++++++ tx %s\n%s", tx.IDShort(), tx.ProducedOutputsToString())
	return tx
}

const indexOffset = 10000

func makeAddresses(n int) ([]core.AddressED25519, []ed25519.PrivateKey) {
	retPrivKeys := testutil.GetTestingPrivateKeys(n, indexOffset)
	retAddrs := make([]core.AddressED25519, n)
	for i := range retAddrs {
		retAddrs[i] = core.AddressED25519FromPrivateKey(retPrivKeys[i])
	}
	return retAddrs, retPrivKeys
}

func Test1Sequencer(t *testing.T) {
	t.Run("run idle", func(t *testing.T) {
		const maxSlots = 7
		r := initSequencerTestData(t, 1, 0, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(true)
		r.wrk.Start()

		sequencer.SetTraceProposer(sequencer.BaseProposerName, false)

		seq, err := sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
			sequencer.WithName("boot"),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
			sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxSlots+2)),
			sequencer.WithLogLevel(zapcore.InfoLevel),
		)
		require.NoError(t, err)

		msCounter := 0
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, vid *utangle.WrappedOutput) {
			msCounter++
		})

		seq.WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())
		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")
		numTx := r.ut.NumVertices()
		t.Logf("number of transactions on UTXO tangle: %d", numTx)
		t.Logf("ms counter: %d", msCounter)
		require.EqualValues(t, numTx, msCounter+1)
	})
	t.Run("run add chain origins tx", func(t *testing.T) {
		const maxTimeSlots = 10

		r := initSequencerTestData(t, 1, 1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()

		sequencer.SetTraceAll(false)

		t.Logf("chain origins tx:\n%s", r.txChainOrigins.ToString(r.ut.HeaviestStateForLatestTimeSlot().GetUTXO))

		seq, err := sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
			sequencer.WithName("boot"),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxTimeSlots),
			sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxTimeSlots+2)),
		)
		require.NoError(t, err)

		// add transaction with chain origins
		_, err = r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		seq.WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		// difficult to predict number of milestones
		//_, numTx := r.ut.NumVertices()
		//require.EqualValues(t, 4, numTx)

		r.ut.SaveGraph(fnameFromTestName(t))

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		t.Logf("checking state at root: %s", heaviestState.Root().String())
		for _, o := range r.chainOrigins {
			_, found := heaviestState.GetUTXO(&o.ID)
			require.True(t, found)
		}
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")
	})
	t.Run("1 faucet txs sync", func(t *testing.T) {
		const (
			transferAmount        = 100
			numFaucetTransactions = 300                                    // 79 // 200 // more than 150 does not work with this pace
			maxFeeInputs          = 100                                    // sequencer.DefaultMaxFeeInputs
			maxSlots              = numFaucetTransactions/maxFeeInputs + 3 // 10
		)

		r := initSequencerTestData(t, 1, 1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()

		sequencer.SetTraceAll(false)
		sequencer.SetTraceProposer(sequencer.BaseProposerName, false)

		seq, err := sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
			sequencer.WithName("boot"),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
			sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxSlots+2)),
			sequencer.WithMaxFeeInputs(maxFeeInputs),
		)
		require.NoError(t, err)

		totalInflation := uint64(0)
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, msOutput *utangle.WrappedOutput) {
			totalInflation += msOutput.VID.InflationAmount()
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance, int(initOnSeqBalance))

		// add transaction with chain origins
		_, err = r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		addrs, _ := makeAddresses(1)
		t.Logf("additional address: %s", addrs[0].String())
		for i := 0; i < numFaucetTransactions; i++ {
			tx := r.makeFaucetTransaction(r.bootstrapChainID, 0, addrs[0], transferAmount)
			_, err = r.wrk.TransactionInWaitAppend(tx.Bytes(), 5*time.Second)
			require.NoError(t, err)
		}
		seq.WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		for _, o := range r.chainOrigins {
			_, found := heaviestState.GetUTXO(&o.ID)
			require.True(t, found)
		}
		r.ut.SaveGraph(fnameFromTestName(t))

		bal := heaviestState.BalanceOf(addrs[0].AccountID())
		require.EqualValues(t, numFaucetTransactions*transferAmount, int(bal))

		bal = heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, util.GoThousands(initOnSeqBalance+totalInflation+(1+numFaucetTransactions)*feeAmount), util.GoThousands(bal))
	})
	t.Run("1 faucet txs async", func(t *testing.T) {
		const (
			numFaucetTransactions = 120 // 300 // 402 // limit
			transferAmount        = 100
			maxInputs             = sequencer.DefaultMaxFeeInputs
			maxSlots              = numFaucetTransactions/maxInputs + 3
		)

		r := initSequencerTestData(t, 1, 1, core.LogicalTimeNow())
		//workflow.WithConsumerLogLevel(workflow.RejectConsumerName, zapcore.DebugLevel),
		//workflow.WithConsumerLogLevel(workflow.PreValidateConsumerName, zapcore.DebugLevel),
		//workflow.WithConsumerLogLevel(workflow.SolidifyConsumerName, zapcore.DebugLevel),

		r.wrk.MustOnEvent(workflow.EventRejectedTx, func(inp *workflow.RejectInputData) {
			r.t.Logf("rejected %s : '%s'", inp.TxID.Short(), inp.Msg)
		})
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()

		sequencer.SetTraceAll(false)

		seq, err := sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
			sequencer.WithName("boot"),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots+2),
			sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxSlots+2)),
			sequencer.WithMaxFeeInputs(maxInputs),
		)
		require.NoError(t, err)

		totalInflation := uint64(0)
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, msOutput *utangle.WrappedOutput) {
			totalInflation += msOutput.VID.InflationAmount()
		})

		var allFeeInputsConsumed atomic.Bool
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				go seq.Stop()
			}
		})
		require.NoError(t, err)

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance, int(initOnSeqBalance))

		// add transaction with chain origins
		_, err = r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		addrs, _ := makeAddresses(1)
		require.NoError(t, err)

		t.Logf("additional address: %s", addrs[0].String())
		for i := 0; i < numFaucetTransactions; i++ {
			tx := r.makeFaucetTransaction(r.bootstrapChainID, 0, addrs[0], transferAmount)
			err = r.wrk.TransactionIn(tx.Bytes()) // <- async
			require.NoError(t, err)
		}
		seq.WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		t.Logf("---- counter info ------\n%s", r.wrk.CounterInfo())
		//r.wrk.UTXOTangle().SaveGraph("utxo_tangle")

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		t.Logf("stem output of the heaviest state: %s", heaviestState.GetStemOutput().ID.Short())
		for _, o := range r.chainOrigins {
			_, found := heaviestState.GetUTXO(&o.ID)
			require.True(t, found)
		}
		require.EqualValues(t, numFaucetTransactions+1, seq.NumOutputsInPool())
		nOuts := heaviestState.NumOutputs(addrs[0].AccountID())
		require.EqualValues(t, numFaucetTransactions, nOuts)
		bal := heaviestState.BalanceOf(addrs[0].AccountID())
		require.EqualValues(t, numFaucetTransactions*transferAmount, int(bal))

		bal = heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, int(initOnSeqBalance+totalInflation+(1+numFaucetTransactions)*feeAmount), int(bal))
		r.ut.SaveGraph(fnameFromTestName(t))
	})
	t.Run("N faucets async", func(t *testing.T) {
		const (
			numFaucets            = 3
			numFaucetTransactions = 50
			transferAmount        = 100
			maxInputs             = 254 // sequencer.DefaultMaxFeeInputs
			maxSlots              = numFaucetTransactions*numFaucets/maxInputs + 6
		)
		t.Logf("numFaucets: %d, numFaucetTransactions: %d", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, 1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()

		sequencer.SetTraceAll(false)

		seq, err := sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
			sequencer.WithName("boot"),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
			sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxSlots+2)),
			sequencer.WithMaxFeeInputs(maxInputs),
		)
		require.NoError(t, err)

		totalInflation := uint64(0)
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, msOutput *utangle.WrappedOutput) {
			totalInflation += msOutput.VID.InflationAmount()
		})

		var allFeeInputsConsumed atomic.Bool
		seq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			seq.LogMilestoneSubmitDefault(wOut)
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				go seq.Stop()
			}
		})
		require.NoError(t, err)

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnSeqBalance))

		// add transaction with chain origins
		_, err = r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		addrs, _ := makeAddresses(1)
		require.NoError(t, err)

		t.Logf("additional address: %s", addrs[0].String())
		for i := 0; i < numFaucets; i++ {
			for j := 0; j < numFaucetTransactions; j++ {
				tx := r.makeFaucetTransaction(r.bootstrapChainID, i, addrs[0], transferAmount)
				err = r.wrk.TransactionIn(tx.Bytes()) // <- async
				//_, err = r.wrk.TransactionInWaitAppend(tx.Bytes()) // sync
				require.NoError(t, err)
			}
		}

		seq.WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		//testutil.PrintRTStatsForSomeTime(3 * time.Second)

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		for _, o := range r.chainOrigins {
			_, found := heaviestState.GetUTXO(&o.ID)
			require.True(t, found)
		}
		bal := heaviestState.BalanceOf(addrs[0].AccountID())
		require.EqualValues(t, numFaucetTransactions*numFaucets*transferAmount, int(bal))

		bal = heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, int(initOnSeqBalance+totalInflation+(1+numFaucetTransactions*numFaucets)*feeAmount), int(bal))

		r.ut.SaveGraph(fnameFromTestName(t))
	})
}

func (r *sequencerTestData) createSequencers(maxInputsInTx, maxSlots, pace int, loglevel zapcore.Level, dontTagAlong ...bool) {
	var err error
	endorse := r.ut.HeaviestStemOutput().ID.TransactionID()
	r.t.Logf("endorse: %v", endorse.String())
	r.bootstrapSeq, err = sequencer.StartNew(r.wrk, r.bootstrapChainID, r.originControllerPrivateKey,
		sequencer.WithName("boot"),
		sequencer.WithLogLevel(loglevel),
		sequencer.WithPace(pace),
		sequencer.WithMaxBranches(maxSlots),
		sequencer.WithMaxTargetTs(core.LogicalTimeNow().AddTimeSlots(maxSlots)),
		sequencer.WithMaxFeeInputs(maxInputsInTx),
	)
	require.NoError(r.t, err)

	maxTargetTs := core.LogicalTimeNow().AddTimeSlots(maxSlots)
	r.sequencers = make([]*sequencer.Sequencer, len(r.chainOrigins))
	for i := range r.chainOrigins {
		chainOut, ok, wrong := r.ut.GetWrappedOutput(&r.chainOrigins[i].OutputWithID.ID)
		require.False(r.t, wrong)
		require.True(r.t, ok)
		r.sequencers[i], err = sequencer.StartNew(r.wrk, r.chainOrigins[i].ChainID, r.chainControllersPrivateKeys[i],
			sequencer.WithName(fmt.Sprintf("seq%d", i)),
			sequencer.WithLogLevel(loglevel),
			sequencer.WithPace(pace),
			sequencer.WithMaxTargetTs(maxTargetTs),
			sequencer.WithMaxFeeInputs(maxInputsInTx),
			sequencer.WithStartOutput(chainOut),
		)
		r.t.Logf("new seq #%d: %v", i, err)
		require.NoError(r.t, err)
	}
}

func (r *sequencerTestData) createTransactionLogger() {
	f, err := os.OpenFile(fnameFromTestName(r.t),
		os.O_CREATE|os.O_WRONLY, 0644)
	require.NoError(r.t, err)

	txCounter := 0
	err = r.wrk.Events().ListenTransactions(func(vid *utangle.WrappedTx) {
		_, _ = fmt.Fprintf(f, "------------ %d %s\n", txCounter, vid.Lines().String())
		txCounter++
		_ = f.Sync()
	})
	require.NoError(r.t, err)
}

func fnameFromTestName(t *testing.T) string {
	return "test-out/" + strings.Replace(t.Name(), "/", "_", -1)
}

const transferAmount = 1_000

func (r *sequencerTestData) issueTransfersRndSeq(targetAddress core.Lock, numFaucets, numFaucetTransactions int, expected map[core.ChainID]uint64) uint64 {
	r.t.Logf("target address: %s", targetAddress.String())
	targetSeqIdx := 0
	seqIDs := r.allSequencerIDs()
	for i := 0; i < numFaucets; i++ {
		for j := 0; j < numFaucetTransactions; j++ {
			targetSeqID := seqIDs[targetSeqIdx]
			tx := r.makeFaucetTransaction(targetSeqID, i, targetAddress, transferAmount)
			err := r.wrk.TransactionIn(tx.Bytes()) // <- async
			//_, err = r.wrk.TransactionInWaitAppend(tx.Bytes()) // sync
			require.NoError(r.t, err)
			expected[targetSeqID] += feeAmount
			targetSeqIdx = (targetSeqIdx + 1) % len(seqIDs)
		}
	}
	return uint64(numFaucets * numFaucetTransactions * transferAmount)
}

func (r *sequencerTestData) issueTransfersWithSeqID(targetAddress core.Lock, targetSeqID core.ChainID, numFaucets, numFaucetTransactions int, expected map[core.ChainID]uint64) uint64 {
	r.t.Logf("target address: %s", targetAddress.String())
	for i := 0; i < numFaucets; i++ {
		for j := 0; j < numFaucetTransactions; j++ {
			tx := r.makeFaucetTransaction(targetSeqID, i, targetAddress, transferAmount)
			err := r.wrk.TransactionIn(tx.Bytes()) // <- async
			//_, err = r.wrk.TransactionInWaitAppend(tx.Bytes()) // sync
			require.NoError(r.t, err)
			expected[targetSeqID] += feeAmount
		}
	}
	return uint64(numFaucets * numFaucetTransactions * transferAmount)
}

func TestNSequencers(t *testing.T) {
	t.Run("2 seq", func(t *testing.T) {
		const (
			maxSlots              = 100
			numFaucets            = 1
			numFaucetTransactions = 1
			maxTxInputs           = sequencer.DefaultMaxFeeInputs
			stopAfterBranches     = 60
			tagAlong              = false
		)
		t.Logf("\n   numFaucets: %d\n   numFaucetTransactions: %d\n", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, 1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BaseProposerName, false)
		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlots, 5, zapcore.InfoLevel, !tagAlong)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				go r.sequencers[0].Stop()
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnSeqBalance))

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		// also asserts consistency of supply and inflation
		summarySupply := r.ut.FetchSummarySupplyAndInflationOnHeaviestBranch(-1)
		t.Logf("Heaviest branch summary: \n%s", summarySupply.Lines("     ").String())
	})
	t.Run("2 seq, transfers 1", func(t *testing.T) {
		const (
			nSequencers       = 2
			maxSlots          = 20
			numFaucets        = 2
			numTxPerFaucet    = 10
			maxTxInputs       = 0
			stopAfterBranches = 20
			tagAlong          = false
		)
		t.Logf("\n   numFaucets: %d\n   numTxPerFaucet: %d\n   transferAmount: %d",
			numFaucets, numTxPerFaucet, transferAmount)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()
		//r.createTransactionLogger()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlots, 5, zapcore.InfoLevel, !tagAlong)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numTxPerFaucet*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				go r.sequencers[0].Stop()
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		addrs, _ := makeAddresses(1)
		targetAddress := addrs[0]
		require.NoError(t, err)

		allSeqIDs := r.allSequencerIDs()
		expectedOnChainBalancePerSeqID := make(map[core.ChainID]uint64)
		for _, seqID := range allSeqIDs {
			expectedOnChainBalancePerSeqID[seqID] = initOnChainBalance // each sequencer spends fee for boostrap once
		}
		expectedOnChainBalancePerSeqID[r.bootstrapChainID] = initOnBootstrapSeqBalance + uint64(feeAmount*len(r.chainOrigins))

		totalAmountToTargetAddress := r.issueTransfersRndSeq(targetAddress, numFaucets, numTxPerFaucet, expectedOnChainBalancePerSeqID)

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()

		// check balance on the target address
		bal := heaviestState.BalanceOf(targetAddress.AccountID())
		require.EqualValues(t, int(totalAmountToTargetAddress), int(bal))

		// check balance on the bootstrap sequencer
		wrong := false
		for _, seqID := range allSeqIDs {
			if bal = heaviestState.BalanceOnChain(&seqID); expectedOnChainBalancePerSeqID[seqID] != bal {
				wrong = true
			}
			boot := " "
			if seqID == r.bootstrapChainID {
				boot = " bootstrap "
			}
			t.Logf("chain balance on%ssequencer %s: %s (expected %s), diff: %d", boot, seqID.Short(),
				util.GoThousands(bal), util.GoThousands(expectedOnChainBalancePerSeqID[seqID]), int64(bal)-int64(expectedOnChainBalancePerSeqID[seqID]))
		}
		require.False(t, wrong)
	})
	t.Run("2 seq, transfers 2", func(t *testing.T) {
		const (
			nSequencers       = 2
			maxSlots          = 40
			numFaucets        = 2
			numTxPerFaucet    = 10
			maxTxInputs       = 100
			stopAfterBranches = 20
		)
		t.Logf("\n   numFaucets: %d\n   numTxPerFaucet: %d\n   transferAmount: %d",
			numFaucets, numTxPerFaucet, transferAmount)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()
		//r.createTransactionLogger()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlots, 5, zapcore.InfoLevel)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		addrs, _ := makeAddresses(1)
		targetAddress := addrs[0]
		require.NoError(t, err)

		allSeqIDs := r.allSequencerIDs()
		expectedOnChainBalancePerSeqID := make(map[core.ChainID]uint64)
		for _, seqID := range allSeqIDs {
			expectedOnChainBalancePerSeqID[seqID] = initOnChainBalance // each sequencer spends fee for boostrap once
		}
		expectedOnChainBalancePerSeqID[r.bootstrapChainID] = initOnBootstrapSeqBalance + uint64(feeAmount*len(r.chainOrigins))

		var glbMutex sync.Mutex
		totalAmountToTargetAddress := uint64(0)
		branchCount := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numTxPerFaucet*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				go r.sequencers[0].Stop()
				return
			}

			if wOut.VID.IsBranchTransaction() {
				if branchCount < stopAfterBranches/2 {
					// issue transfers
					glbMutex.Lock()
					totalAmountToTargetAddress += r.issueTransfersRndSeq(targetAddress, numFaucets, numTxPerFaucet, expectedOnChainBalancePerSeqID)
					glbMutex.Unlock()
				}
				branchCount++
			}
		})

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()

		// check balance on the target address
		bal := heaviestState.BalanceOf(targetAddress.AccountID())
		require.EqualValues(t, int(totalAmountToTargetAddress), int(bal))

		// check balance on the bootstrap sequencer
		oneIsWrong := false
		for _, seqID := range allSeqIDs {
			if bal = heaviestState.BalanceOnChain(&seqID); expectedOnChainBalancePerSeqID[seqID] != bal {
				oneIsWrong = true
			}
			boot := " "
			if seqID == r.bootstrapChainID {
				boot = " bootstrap "
			}
			t.Logf("chain balance on%ssequencer %s: %d (expected %d)", boot, seqID.Short(), bal, expectedOnChainBalancePerSeqID[seqID])
		}
		require.False(t, oneIsWrong)
	})
	t.Run("3 seq", func(t *testing.T) {
		const (
			maxSlot               = 30
			numFaucets            = 1
			numFaucetTransactions = 1
			maxTxInputs           = sequencer.DefaultMaxFeeInputs
			stopAfterBranches     = 20
			nSequencers           = 3
			tagAlong              = true // <<<<   ????????
		)
		t.Logf("\n   numFaucets: %d\n   numFaucetTransactions: %d\n", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlot, 5, zapcore.InfoLevel, !tagAlong)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				for _, s := range r.sequencers {
					go s.Stop()
				}
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		bal := heaviestState.BalanceOnChain(&r.bootstrapChainID)

		require.EqualValues(t, int(initOnBootstrapSeqBalance+feeAmount*nSequencers), int(bal))
	})
	t.Run("5 seq", func(t *testing.T) {
		const (
			maxSlot               = 20
			numFaucets            = 2
			numFaucetTransactions = 10
			maxTxInputs           = 200
			stopAfterBranches     = 20
			nSequencers           = 5
		)
		t.Logf("\n   numFaucets: %d\n   numFaucetTransactions: %d\n", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)
		r.wrk.Start()
		//r.createTransactionLogger()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlot, 5, zapcore.InfoLevel)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			seq.LogMilestoneSubmitDefault(wOut)
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				for _, s := range r.sequencers {
					go s.Stop()
				}
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(r.t))

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		bal := heaviestState.BalanceOnChain(&r.bootstrapChainID)

		require.EqualValues(t, int(initOnBootstrapSeqBalance+feeAmount*nSequencers), int(bal))
		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")
	})
}

func TestPruning(t *testing.T) {
	t.Run("3 seq prune once", func(t *testing.T) {
		const (
			maxSlots              = 20
			numFaucets            = 1
			numFaucetTransactions = 1
			maxTxInputs           = 200
			stopAfterBranches     = 20
			nSequencers           = 3
		)
		t.Logf("\n   numFaucets: %d\n   numFaucetTransactions: %d\n", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)

		r.wrk.Start()

		//r.createTransactionLogger()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlots, 5, zapcore.InfoLevel)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			seq.LogMilestoneSubmitDefault(wOut)
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				for _, s := range r.sequencers {
					go s.Stop()
				}
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(r.t))

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		bal := heaviestState.BalanceOnChain(&r.bootstrapChainID)

		require.EqualValues(t, int(initOnBootstrapSeqBalance+feeAmount+feeAmount*(nSequencers-1)), int(bal))
		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		reachable2, orphaned2, baseline2 := r.ut.ReachableAndOrphaned(2)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			2, len(reachable2), len(orphaned2), time.Since(baseline2), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable2)+len(orphaned2))

		reachable3, orphaned3, baseline3 := r.ut.ReachableAndOrphaned(3)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			3, len(reachable3), len(orphaned3), time.Since(baseline3), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable3)+len(orphaned3))

		reachable5, orphaned5, baseline5 := r.ut.ReachableAndOrphaned(5)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			5, len(reachable5), len(orphaned5), time.Since(baseline5), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable5)+len(orphaned5))

		startT := time.Now()
		nPrunedTx, nPrunedBranches, deletedSlots := r.ut.PruneOrphaned(5)
		t.Logf("pruned %d vertices and %d branches in %v. Deleted slots: %d",
			nPrunedTx, nPrunedBranches, time.Since(startT), deletedSlots)
		r.ut.SaveGraph(fnameFromTestName(t) + "_PRUNE5_before_cut")

		for cutBranchTxID, numTx := r.ut.CutFinalBranchIfExists(5); cutBranchTxID != nil; {
			t.Logf("cut finalized branch %s, cut %d transactions", cutBranchTxID.Short(), numTx)
			cutBranchTxID, numTx = r.ut.CutFinalBranchIfExists(5)
		}

		r.ut.SaveGraph(fnameFromTestName(t) + "_PRUNE5_after_cut")

		startT = time.Now()
		nPrunedTx, nPrunedBranches, deletedSlots = r.ut.PruneOrphaned(5)
		t.Logf("pruned %d vertices and %d branches in %v. Deleted slots: %d",
			nPrunedTx, nPrunedBranches, time.Since(startT), deletedSlots)
		r.ut.SaveGraph(fnameFromTestName(t) + "_PRUNE5_after_cut_final")

		t.Logf("PRUNED: %s", r.ut.Info())
	})
	t.Run("3 seq pruner", func(t *testing.T) {
		const (
			maxSlots              = 40
			numFaucets            = 1
			numFaucetTransactions = 1
			maxTxInputs           = sequencer.DefaultMaxFeeInputs
			stopAfterBranches     = 40
			nSequencers           = 3
		)
		t.Logf("\n   numFaucets: %d\n   numFaucetTransactions: %d\n", numFaucets, numFaucetTransactions)
		r := initSequencerTestData(t, numFaucets, nSequencers-1, core.LogicalTimeNow())
		transaction.SetPrintEasyFLTraceOnFail(false)

		r.wrk.Start()
		r.wrk.StartPruner()

		//r.createTransactionLogger()
		// add transaction with chain origins
		_, err := r.wrk.TransactionInWaitAppend(r.txChainOrigins.Bytes(), 5*time.Second)
		require.NoError(t, err)
		t.Logf("chain origins transaction has been added to the tangle: %s", r.txChainOrigins.IDShort())

		sequencer.SetTraceProposer(sequencer.BacktrackProposerName, false)

		r.createSequencers(maxTxInputs, maxSlots, 5, zapcore.InfoLevel)

		var allFeeInputsConsumed atomic.Bool
		branchesAfterAllConsumed := 0
		cnt := 0
		r.bootstrapSeq.OnMilestoneSubmitted(func(seq *sequencer.Sequencer, wOut *utangle.WrappedOutput) {
			seq.LogMilestoneSubmitDefault(wOut)
			cnt++
			if seq.Info().NumConsumedFeeOutputs >= numFaucetTransactions*numFaucets {
				allFeeInputsConsumed.Store(true)
			}
			if allFeeInputsConsumed.Load() && wOut.VID.IsBranchTransaction() {
				branchesAfterAllConsumed++
			}
			if branchesAfterAllConsumed >= stopAfterBranches {
				go seq.Stop()
				for _, s := range r.sequencers {
					go s.Stop()
				}
			}
		})

		heaviestState := r.ut.HeaviestStateForLatestTimeSlot()
		initOnBootstrapSeqBalance := heaviestState.BalanceOnChain(&r.bootstrapChainID)
		require.EqualValues(t, inittest.InitSupply-initFaucetBalance*numFaucets, int(initOnBootstrapSeqBalance))

		r.bootstrapSeq.WaitStop()
		r.sequencers[0].WaitStop()
		r.wrk.Stop()
		t.Logf("%s", r.ut.Info())

		r.ut.SaveGraph(fnameFromTestName(r.t))

		heaviestState = r.ut.HeaviestStateForLatestTimeSlot()
		latest := r.ut.LatestTimeSlot()
		t.Logf("latest slot: %d", latest)
		for _, o := range r.chainOrigins {
			found := r.wrk.UTXOTangle().HasOutputInTimeSlot(latest, &o.ID)
			require.False(t, found)
		}

		bal := heaviestState.BalanceOnChain(&r.bootstrapChainID)

		require.EqualValues(t, int(initOnBootstrapSeqBalance+feeAmount+feeAmount*(nSequencers-1)), int(bal))
		r.ut.SaveGraph(fnameFromTestName(t))
		r.ut.SaveTree(fnameFromTestName(t) + "_TREE")

		reachable2, orphaned2, baseline2 := r.ut.ReachableAndOrphaned(2)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			2, len(reachable2), len(orphaned2), time.Since(baseline2), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable2)+len(orphaned2))

		reachable3, orphaned3, baseline3 := r.ut.ReachableAndOrphaned(3)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			3, len(reachable3), len(orphaned3), time.Since(baseline3), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable3)+len(orphaned3))

		reachable5, orphaned5, baseline5 := r.ut.ReachableAndOrphaned(5)
		t.Logf("====== top slots: %d, reachable %d, orphaned %d, since baseline: %v, total vertices: %d",
			5, len(reachable5), len(orphaned5), time.Since(baseline5), r.ut.NumVertices())
		require.EqualValues(t, r.ut.NumVertices(), len(reachable5)+len(orphaned5))

		t.Logf("PRUNED: %s", r.ut.Info())
	})
}
