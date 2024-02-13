package workflow

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	transaction2 "github.com/lunfardo314/proxima/ledger/transaction"
	txbuilder2 "github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/countdown"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/proxima/workflow"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap/zapcore"
	"golang.org/x/crypto/blake2b"
)

type workflowTestData struct {
	initLedgerStatePar      ledger.IdentityData
	distributionPrivateKeys []ed25519.PrivateKey
	distributionAddrs       []ledger.AddressED25519
	faucetOutputs           []*ledger.OutputWithID
	ut                      *utangle_old.UTXOTangle
	bootstrapChainID        ledger.ChainID
	distributionTxID        ledger.TransactionID
	w                       *workflow.Workflow
}

const initDistributedBalance = 10_000_000

func initWorkflowTest(t *testing.T, nDistribution int, nowis ledger.Time, configOptions ...workflow.ConfigOption) *workflowTestData {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	t.Logf("nowis timestamp: %s", nowis.String())
	genesisPrivKey := testutil.GetTestingPrivateKey()
	par := *ledger.DefaultIdentityData(genesisPrivKey, nowis.Slot())
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistributionOld(nDistribution, initDistributedBalance)
	ret := &workflowTestData{
		initLedgerStatePar:      par,
		distributionPrivateKeys: privKeys,
		distributionAddrs:       addrs,
		faucetOutputs:           make([]*ledger.OutputWithID, nDistribution),
	}

	stateStore := common.NewInMemoryKVStore()
	txStore := txstore.NewDummyTxBytesStore()

	ret.bootstrapChainID, _ = multistate.InitStateStore(par, stateStore)
	txBytes, err := txbuilder2.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)

	ret.ut = utangle_old.Load(stateStore)

	ret.distributionTxID, _, err = transaction2.IDAndTimestampFromTransactionBytes(txBytes)
	require.NoError(t, err)

	for i := range ret.faucetOutputs {
		outs, err := ret.ut.HeaviestStateForLatestTimeSlot().GetOutputsForAccount(ret.distributionAddrs[i].AccountID())
		require.NoError(t, err)
		require.EqualValues(t, 1, len(outs))
		ret.faucetOutputs[i] = outs[0]

	}
	_, err = txStore.PersistTxBytesWithMetadata(txBytes)
	require.NoError(t, err)

	ret.w = workflow.New(ret.ut, peering.NewPeersDummy(), txStore, configOptions...)
	return ret
}

func (wd *workflowTestData) makeTxFromFaucet(amount uint64, target ledger.AddressED25519, idx ...int) ([]byte, error) {
	idxFaucet := 0
	if len(idx) > 0 {
		idxFaucet = idx[0]
	}
	td := txbuilder2.NewTransferData(wd.distributionPrivateKeys[idxFaucet], wd.distributionAddrs[idxFaucet], wd.faucetOutputs[idxFaucet].Timestamp()).
		WithAmount(amount).
		WithTargetLock(target).
		MustWithInputs(wd.faucetOutputs[idxFaucet])

	_, err := ledger.TimeFromBytes(td.Timestamp[:])
	util.AssertNoError(err)

	txBytes, remainder, err := txbuilder2.MakeSimpleTransferTransactionWithRemainder(td)
	if err != nil {
		return nil, err
	}
	wd.faucetOutputs[idxFaucet] = remainder
	return txBytes, nil
}

func (wd *workflowTestData) setNewVertexCounter(waitCounter *countdown.Countdown) {
	wd.w.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
		waitCounter.Tick()
	})
}

func TestWorkflowBasic(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		wd := initWorkflowTest(t, 1, ledger.TimeNow(), workflow.WithLogLevel(zapcore.DebugLevel))
		wd.w.Start()
		time.Sleep(10 * time.Millisecond)
		wd.w.Stop()
		time.Sleep(10 * time.Millisecond)
	})
	t.Run("2", func(t *testing.T) {
		wd := initWorkflowTest(t, 1, ledger.TimeNow(), workflow.WithLogLevel(zapcore.DebugLevel))
		wd.w.Start()
		err := wd.w.TransactionIn(nil)
		require.Error(t, err)
		err = wd.w.TransactionIn([]byte("abc"))
		require.Error(t, err)
		util.RequirePanicOrErrorWith(t, func() error {
			return wd.w.TransactionIn([]byte("0000000000"))
		}, "basic parse failed")
		time.Sleep(1000 * time.Millisecond)
		wd.w.Stop()
		time.Sleep(1000 * time.Millisecond)
	})
}

func TestWorkflowSync(t *testing.T) {
	t.Run("1 sync", func(t *testing.T) {
		const numRuns = 200

		wd := initWorkflowTest(t, 1, ledger.TimeNow())

		t.Logf("timestamp now: %s", ledger.TimeNow().String())
		t.Logf("distribution timestamp: %s", wd.distributionTxID.Timestamp().String())
		t.Logf("origin slot: %d", wd.initLedgerStatePar.GenesisSlot)

		estimatedTimeout := (time.Duration(numRuns) * ledger.TransactionTimePaceDuration()) + (5 * time.Second)
		waitCounter := countdown.New(numRuns, estimatedTimeout)
		var cnt atomic.Int32
		err := wd.w.OnEvent(workflow.EventNewVertex, func(v *workflow.NewVertexEventData) {
			waitCounter.Tick()
			cnt.Inc()
		})
		require.NoError(t, err)

		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			txBytes, err := wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)

			_, err = wd.w.TransactionInWaitAppend(txBytes, 5*time.Second)
			require.NoError(t, err)
		}
		err = waitCounter.Wait()
		require.NoError(t, err)

		wd.w.Stop()
		wd.w.WaitStop()
		t.Logf("UTXO tangle:\n%s", wd.ut.Info())
		require.EqualValues(t, numRuns, cnt.Load())
	})
	t.Run("duplicates", func(t *testing.T) {
		const (
			numTx   = 10
			numRuns = 10
		)

		wd := initWorkflowTest(t, 1, ledger.TimeNow()) //, DebugConfig{PrimaryInputConsumerName: zapcore.DebugLevel})

		var err error
		txBytes := make([][]byte, numTx)
		for i := range txBytes {
			txBytes[i], err = wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)
		}

		waitCounterAdd := countdown.NewNamed("addTx", numTx, 5*time.Second)
		waitCounterDuplicate := countdown.NewNamed("duplicates", numTx*(numRuns-1), 5*time.Second)

		err = wd.w.OnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			waitCounterAdd.Tick()
		})
		require.NoError(t, err)
		err = wd.w.OnEvent(workflow.EventCodeDuplicateTx, func(_ *ledger.TransactionID) {
			waitCounterDuplicate.Tick()
		})
		require.NoError(t, err)
		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			for j := range txBytes {
				_, err = wd.w.TransactionInWaitAppend(txBytes[j], 5*time.Second)
				require.True(t, err == nil || strings.Contains(err.Error(), "duplicate"))
			}
		}
		err = waitCounterAdd.Wait()
		require.NoError(t, err)
		err = waitCounterDuplicate.Wait()
		require.NoError(t, err)

		wd.w.Stop()
		t.Logf("%s", wd.w.UTXOTangle().Info())
	})
	t.Run("listen", func(t *testing.T) {
		const numRuns = 200

		wd := initWorkflowTest(t, 1, ledger.TimeNow())

		t.Logf("timestamp now: %s", ledger.TimeNow().String())
		t.Logf("distribution timestamp: %s", wd.distributionTxID.Timestamp().String())
		t.Logf("origin slot: %d", wd.initLedgerStatePar.GenesisSlot)

		estimatedTimeout := (time.Duration(numRuns) * ledger.TransactionTimePaceDuration()) + (10 * time.Second)
		waitCounter := countdown.New(numRuns, estimatedTimeout)
		err := wd.w.OnEvent(workflow.EventNewVertex, func(v *workflow.NewVertexEventData) {
			waitCounter.Tick()
		})
		require.NoError(t, err)

		var listenerCounter atomic.Uint32
		err = wd.w.Events().ListenAccount(wd.distributionAddrs[0], func(_ utangle_old.WrappedOutput) {
			listenerCounter.Inc()
		})
		require.NoError(t, err)

		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			txBytes, err := wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)

			_, err = wd.w.TransactionInWaitAppend(txBytes, 5*time.Second)
			require.NoError(t, err)
		}
		err = waitCounter.Wait()

		time.Sleep(100 * time.Millisecond) // otherwise listen counter sometimes fails

		require.NoError(t, err)
		require.EqualValues(t, 2*numRuns, int(listenerCounter.Load()))

		wd.w.Stop()
		t.Logf("UTXO tangle:\n%s", wd.ut.Info())
	})
}

func TestWorkflowAsync(t *testing.T) {
	t.Run("1 async", func(t *testing.T) {
		const numRuns = 200

		wd := initWorkflowTest(t, 1, ledger.TimeNow())

		t.Logf("timestamp now: %s", ledger.TimeNow().String())
		t.Logf("distribution timestamp: %s", wd.distributionTxID.Timestamp().String())
		t.Logf("origin slot: %d", wd.initLedgerStatePar.GenesisSlot)

		estimatedTimeout := (time.Duration(numRuns) * ledger.TransactionTimePaceDuration()) + (5 * time.Second)
		waitCounter := countdown.New(numRuns, estimatedTimeout)
		var cnt atomic.Uint32
		err := wd.w.OnEvent(workflow.EventNewVertex, func(v *workflow.NewVertexEventData) {
			waitCounter.Tick()
			cnt.Inc()
		})
		require.NoError(t, err)

		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			txBytes, err := wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)

			err = wd.w.TransactionIn(txBytes)
			require.NoError(t, err)
		}
		err = waitCounter.Wait()
		require.NoError(t, err)

		wd.w.Stop()
		t.Logf("UTXO tangle:\n%s", wd.ut.Info())
		require.EqualValues(t, numRuns, cnt.Load())
	})
	t.Run("duplicates", func(t *testing.T) {
		const (
			numTx   = 10
			numRuns = 10
		)

		wd := initWorkflowTest(t, 1, ledger.TimeNow()) //, DebugConfig{PrimaryInputConsumerName: zapcore.DebugLevel})

		var err error
		txBytes := make([][]byte, numTx)
		for i := range txBytes {
			txBytes[i], err = wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)
		}

		waitCounterAdd := countdown.NewNamed("addTx", numTx, 5*time.Second)
		waitCounterDuplicate := countdown.NewNamed("duplicates", numTx*(numRuns-1), 5*time.Second)

		err = wd.w.OnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			waitCounterAdd.Tick()
		})
		require.NoError(t, err)
		err = wd.w.OnEvent(workflow.EventCodeDuplicateTx, func(_ *ledger.TransactionID) {
			waitCounterDuplicate.Tick()
		})
		require.NoError(t, err)
		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			for j := range txBytes {
				err = wd.w.TransactionIn(txBytes[j])
				require.NoError(t, err)
			}
		}
		err = waitCounterAdd.Wait()
		require.NoError(t, err)
		err = waitCounterDuplicate.Wait()
		require.NoError(t, err)

		wd.w.Stop()
		t.Logf("%s", wd.w.UTXOTangle().Info())
	})
	t.Run("listen", func(t *testing.T) {
		const numRuns = 200

		wd := initWorkflowTest(t, 1, ledger.TimeNow())

		t.Logf("timestamp now: %s", ledger.TimeNow().String())
		t.Logf("distribution timestamp: %s", wd.distributionTxID.Timestamp().String())
		t.Logf("origin slot: %d", wd.initLedgerStatePar.GenesisSlot)

		estimatedTimeout := (time.Duration(numRuns) * ledger.TransactionTimePaceDuration()) + (6 * time.Second)
		waitCounter := countdown.New(numRuns, estimatedTimeout)
		err := wd.w.OnEvent(workflow.EventNewVertex, func(v *workflow.NewVertexEventData) {
			waitCounter.Tick()
		})
		require.NoError(t, err)

		var listenerCounter atomic.Uint32
		err = wd.w.Events().ListenAccount(wd.distributionAddrs[0], func(_ utangle_old.WrappedOutput) {
			listenerCounter.Inc()
		})
		require.NoError(t, err)

		wd.w.Start()

		for i := 0; i < numRuns; i++ {
			txBytes, err := wd.makeTxFromFaucet(100+uint64(i), wd.distributionAddrs[0])
			require.NoError(t, err)

			err = wd.w.TransactionIn(txBytes)
			require.NoError(t, err)
		}
		err = waitCounter.Wait()
		require.NoError(t, err)
		require.EqualValues(t, 2*numRuns, int(listenerCounter.Load()))

		wd.w.Stop()
		t.Logf("UTXO tangle:\n%s", wd.ut.Info())
	})
}

func TestSolidifier(t *testing.T) {
	t.Run("one tx", func(t *testing.T) {
		wd := initWorkflowTest(t, 1, ledger.TimeNow(), workflow.WithLogLevel(zapcore.DebugLevel))
		cd := countdown.New(1, 3*time.Second)
		wd.setNewVertexCounter(cd)

		txBytes, err := wd.makeTxFromFaucet(10_000, wd.distributionAddrs[0])
		require.NoError(t, err)

		wd.w.Start()
		err = wd.w.TransactionIn(txBytes)
		require.NoError(t, err)

		err = cd.Wait()
		require.NoError(t, err)
		wd.w.Stop()

		t.Logf(wd.w.CounterInfo())
		err = wd.w.CheckDebugCounters(map[string]int{"addtx.ok": 1})
		require.NoError(t, err)
	})
	t.Run("several tx usual seq", func(t *testing.T) {
		const howMany = 3 // 100
		wd := initWorkflowTest(t, 1, ledger.TimeNow(), workflow.WithLogLevel(zapcore.DebugLevel))
		cd := countdown.New(howMany, 300*time.Second) // 10*time.Second)
		wd.setNewVertexCounter(cd)
		var err error

		txBytes := make([][]byte, howMany)
		for i := range txBytes {
			txBytes[i], err = wd.makeTxFromFaucet(10_000, wd.distributionAddrs[0])
			require.NoError(t, err)
		}
		wd.w.Start()
		for i := range txBytes {
			err = wd.w.TransactionIn(txBytes[i], workflow.WithOnWorkflowEventPrefix("checkNewDependency.", func(event string, data any) {
				fmt.Printf("checkNewDependency %s\n", data.(*ledger.TransactionID).StringShort())
			}))
			require.NoError(t, err)
		}
		err = cd.Wait()
		require.NoError(t, err)
		wd.w.Stop()

		t.Logf(wd.w.CounterInfo())
		err = wd.w.CheckDebugCounters(map[string]int{"[addtx].ok": howMany})
	})
	t.Run("several tx reverse seq", func(t *testing.T) {
		const howMany = 10
		wd := initWorkflowTest(t, 1, ledger.TimeNow())
		cd := countdown.New(howMany, 10*time.Second)
		wd.setNewVertexCounter(cd)

		var err error

		txBytes := make([][]byte, howMany)
		for i := range txBytes {
			txBytes[i], err = wd.makeTxFromFaucet(10_000, wd.distributionAddrs[0])
			require.NoError(t, err)
		}
		wd.w.Start()
		for i := len(txBytes) - 1; i >= 0; i-- {
			err = wd.w.TransactionIn(txBytes[i])
			require.NoError(t, err)
		}
		err = cd.Wait()
		require.NoError(t, err)
		wd.w.Stop()

		t.Logf(wd.w.CounterInfo())
		err = wd.w.CheckDebugCounters(map[string]int{"[addtx].ok": howMany})
	})
	t.Run("several tx reverse seq no waiting room", func(t *testing.T) {
		const howMany = 100
		// create all tx in the past, so that won't wait in the waiting room
		// all are sent to solidifier in the reverse order
		nowis := time.Now().Add(-10 * time.Second)
		wd := initWorkflowTest(t, 1, ledger.TimeFromRealTime(nowis))
		cd := countdown.New(howMany, 10*time.Second)
		wd.setNewVertexCounter(cd)

		var err error

		txBytes := make([][]byte, howMany)
		for i := range txBytes {
			txBytes[i], err = wd.makeTxFromFaucet(10_000, wd.distributionAddrs[0])
			require.NoError(t, err)
		}
		wd.w.Start()
		for i := len(txBytes) - 1; i >= 0; i-- {
			err = wd.w.TransactionIn(txBytes[i])
			require.NoError(t, err)
		}
		err = cd.Wait()
		require.NoError(t, err)
		wd.w.Stop()

		t.Logf(wd.w.CounterInfo())
		err = wd.w.CheckDebugCounters(map[string]int{"[addtx].ok": howMany})
	})
	t.Run("parallel rnd seqs no waiting room", func(t *testing.T) {
		const (
			howMany    = 50
			nAddresses = 5
		)
		// create all tx in the past, so that won't wait in the waiting room
		// all are sent to solidifier in the reverse order
		nowis := time.Now().Add(-10 * time.Second)
		wd := initWorkflowTest(t, nAddresses, ledger.TimeFromRealTime(nowis))
		cd := countdown.New(howMany*nAddresses, 10*time.Second)
		wd.setNewVertexCounter(cd)

		var err error

		txSequences := make([][][]byte, nAddresses)
		for iSeq := range txSequences {
			txSequences[iSeq] = make([][]byte, howMany)
			for i := range txSequences[iSeq] {
				txSequences[iSeq][i], err = wd.makeTxFromFaucet(10_000, wd.distributionAddrs[iSeq])
				require.NoError(t, err)
			}
			sort.Slice(txSequences[iSeq], func(i, j int) bool {
				hi := blake2b.Sum256(txSequences[iSeq][i])
				hj := blake2b.Sum256(txSequences[iSeq][j])
				return bytes.Compare(hi[:], hj[:]) < 0
			})
		}
		wd.w.Start()
		for iSeq := range txSequences {
			for i := len(txSequences[iSeq]) - 1; i >= 0; i-- {
				err = wd.w.TransactionIn(txSequences[iSeq][i])
				require.NoError(t, err)
			}
		}
		err = cd.Wait()
		require.NoError(t, err)
		wd.w.Stop()

		t.Logf(wd.w.CounterInfo())
		err = wd.w.CheckDebugCounters(map[string]int{"[addtx].ok": howMany})
		t.Logf("UTXO UTXOTangle:\n%s", wd.ut.Info())
	})
}

type multiChainTestData struct {
	t                  *testing.T
	ts                 ledger.Time
	ut                 *utangle_old.UTXOTangle
	txBytesStore       global.TxBytesStore
	bootstrapChainID   ledger.ChainID
	privKey            ed25519.PrivateKey
	addr               ledger.AddressED25519
	faucetPrivKey      ed25519.PrivateKey
	faucetAddr         ledger.AddressED25519
	faucetOrigin       *ledger.OutputWithID
	sPar               ledger.IdentityData
	originBranchTxid   ledger.TransactionID
	txBytesChainOrigin []byte
	txBytes            [][]byte // with chain origins
	chainOrigins       []*ledger.OutputWithChainID
	total              uint64
	pkController       []ed25519.PrivateKey
}

const onChainAmount = 1_000_000

func initMultiChainTest(t *testing.T, nChains int, verbose bool, secondsInThePast int) *multiChainTestData {
	ledger.SetTimeTickDuration(10 * time.Millisecond)
	nowisTs := ledger.TimeFromRealTime(time.Now().Add(time.Duration(-secondsInThePast) * time.Second))

	t.Logf("initMultiChainTest: now is: %s, %v", ledger.TimeNow().String(), time.Now())
	t.Logf("time tick duration is %v", ledger.TickDuration())
	t.Logf("initMultiChainTest: timeSlot now is assumed: %d, %v", nowisTs.Slot(), ledger.MustNewLedgerTime(nowisTs.Slot(), 0).Time())

	ret := &multiChainTestData{t: t}
	var privKeys []ed25519.PrivateKey
	var addrs []ledger.AddressED25519

	genesisPrivKey := testutil.GetTestingPrivateKey()
	ret.sPar = *ledger.DefaultIdentityData(genesisPrivKey, nowisTs.Slot())
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistributionOld(2, onChainAmount*uint64(nChains))
	ret.privKey = privKeys[0]
	ret.addr = addrs[0]
	ret.faucetPrivKey = privKeys[1]
	ret.faucetAddr = addrs[1]

	ret.pkController = make([]ed25519.PrivateKey, nChains)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	stateStore := common.NewInMemoryKVStore()
	ret.txBytesStore = txstore.NewDummyTxBytesStore()

	ret.bootstrapChainID, _ = multistate.InitStateStore(ret.sPar, stateStore)
	txBytes, err := txbuilder2.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)

	_, err = ret.txBytesStore.PersistTxBytesWithMetadata(txBytes)
	require.NoError(t, err)

	ret.ut = utangle_old.Load(stateStore)

	ret.originBranchTxid, _, err = transaction2.IDAndTimestampFromTransactionBytes(txBytes)
	require.NoError(t, err)

	stateReader := ret.ut.HeaviestStateForLatestTimeSlot()

	t.Logf("state identity:\n%s", ledger.MustLedgerIdentityDataFromBytes(stateReader.MustLedgerIdentityBytes()).String())
	t.Logf("origin branch txid: %s", ret.originBranchTxid.StringShort())
	t.Logf("%s", ret.ut.Info())

	ret.faucetOrigin = &ledger.OutputWithID{
		ID:     ledger.NewOutputID(&ret.originBranchTxid, 0),
		Output: nil,
	}
	bal, _ := multistate.BalanceOnLock(stateReader, ret.addr)
	require.EqualValues(t, onChainAmount*int(nChains), int(bal))
	bal, _ = multistate.BalanceOnLock(stateReader, ret.faucetAddr)
	require.EqualValues(t, onChainAmount*int(nChains), int(bal))

	oDatas, err := stateReader.GetUTXOsLockedInAccount(ret.addr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	firstOut, err := oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, onChainAmount*uint64(nChains), firstOut.Output.Amount())

	faucetDatas, err := stateReader.GetUTXOsLockedInAccount(ret.faucetAddr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.faucetOrigin, err = faucetDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, onChainAmount*uint64(nChains), ret.faucetOrigin.Output.Amount())

	// Create transaction with nChains new chain origins.
	// It is not a sequencer tx with many chain origins
	txb := txbuilder2.NewTransactionBuilder()
	_, err = txb.ConsumeOutput(firstOut.Output, firstOut.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	ret.ts = firstOut.Timestamp().AddTicks(ledger.TransactionPaceInTicks)

	ret.chainOrigins = make([]*ledger.OutputWithChainID, nChains)
	for range ret.chainOrigins {
		o := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(onChainAmount).WithLock(ret.addr)
			_, err := o.PushConstraint(ledger.NewChainOrigin().Bytes())
			require.NoError(t, err)
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)
	}

	txb.TransactionData.Timestamp = ret.ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(ret.privKey)

	ret.txBytesChainOrigin = txb.TransactionData.Bytes()

	tx, err := transaction2.FromBytesMainChecksWithOpt(ret.txBytesChainOrigin)
	require.NoError(t, err)

	if verbose {
		t.Logf("chain origin tx: %s", tx.ToString(stateReader.GetUTXO))
	}

	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		out := ledger.OutputWithID{
			ID:     *oid,
			Output: o,
		}
		if int(idx) != nChains {
			chainID, ok := out.ExtractChainID()
			require.True(t, ok)
			ret.chainOrigins[idx] = &ledger.OutputWithChainID{
				OutputWithID: out,
				ChainID:      chainID,
			}
		}
		return true
	})

	if verbose {
		cstr := make([]string, 0)
		for _, o := range ret.chainOrigins {
			cstr = append(cstr, o.ChainID.StringShort())
		}
		t.Logf("Chain IDs:\n%s\n", strings.Join(cstr, "\n"))
	}

	_, _, err = ret.ut.AppendVertexFromTransactionBytesDebug(ret.txBytesChainOrigin, func() error {
		return ret.txBytesStore.PersistTxBytesWithMetadata(ret.txBytesChainOrigin)
	})
	require.NoError(t, err)
	return ret
}

func (r *multiChainTestData) createSequencerChain1(chainIdx int, pace int, printtx bool, exitFun func(i int, tx *transaction2.Transaction) bool) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)

	ret := make([][]byte, 0)
	outConsumeChain := r.chainOrigins[chainIdx]
	r.t.Logf("chain #%d, ID: %s, origin: %s", chainIdx, outConsumeChain.ChainID.StringShort(), outConsumeChain.ID.StringShort())
	chainID := outConsumeChain.ChainID

	par := txbuilder2.MakeSequencerTransactionParams{
		ChainInput:        outConsumeChain,
		StemInput:         nil,
		Timestamp:         outConsumeChain.Timestamp(),
		MinimumFee:        0,
		AdditionalInputs:  nil,
		AdditionalOutputs: nil,
		Endorsements:      nil,
		PrivateKey:        r.privKey,
		TotalSupply:       0,
	}

	lastStem := r.ut.HeaviestStemOutput()
	//r.t.Logf("lastStem #0 = %s, ts: %s", lastStem.ID.StringShort(), par.LogicalTime.String())
	lastBranchID := r.originBranchTxid

	var tx *transaction2.Transaction
	for i := 0; !exitFun(i, tx); i++ {
		prevTs := par.Timestamp
		toNext := par.Timestamp.TimesTicksToNextSlotBoundary()
		if toNext == 0 || toNext > pace {
			par.Timestamp = par.Timestamp.AddTicks(pace)
		} else {
			par.Timestamp = par.Timestamp.NextTimeSlotBoundary()
		}
		curTs := par.Timestamp
		//r.t.Logf("       %s -> %s", prevTs.String(), curTs.String())

		par.StemInput = nil
		if par.Timestamp.Tick() == 0 {
			par.StemInput = lastStem
		}

		par.Endorsements = nil
		if !par.ChainInput.ID.SequencerFlagON() {
			par.Endorsements = []*ledger.TransactionID{&lastBranchID}
		}

		txBytes, err := txbuilder2.MakeSequencerTransaction(par)
		require.NoError(r.t, err)
		ret = append(ret, txBytes)
		require.NoError(r.t, err)

		tx, err = transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)

		if printtx {
			ce := ""
			if prevTs.Slot() != curTs.Slot() {
				ce = "(cross-slot)"
			}
			r.t.Logf("tx %d : %s    %s", i, tx.IDShortString(), ce)
		}

		require.True(r.t, tx.IsSequencerMilestone())
		if par.StemInput != nil {
			require.True(r.t, tx.IsBranchTransaction())
		}

		o := tx.FindChainOutput(chainID)
		require.True(r.t, o != nil)

		par.ChainInput.OutputWithID = *o.Clone()
		if par.StemInput != nil {
			lastStem = tx.FindStemProducedOutput()
			require.True(r.t, lastStem != nil)
			//r.t.Logf("lastStem #%d = %s", i, lastStem.ID.StringShort())
		}
	}
	return ret
}

func (r *multiChainTestData) createSequencerChains1(pace int, howLong int) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		// sequencer tx
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
			counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		counter++
	}

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		ts := ledger.MaxTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		var endorse []*ledger.TransactionID
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			endorse = []*ledger.TransactionID{lastInChain(curChainIdx).ID()}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:    stemOut,
			Endorsements: endorse,
			Timestamp:    ts,
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if stemOut == nil {
			r.t.Logf("%d : chain #%d, txid: %s, endorse(%d): %s, timestamp: %s",
				i, nextChainIdx, tx.IDShortString(), curChainIdx, endorse[0].StringShort(), tx.Timestamp().String())
		} else {
			r.t.Logf("%d : chain #%d, txid: %s, timestamp: %s <- branch tx",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String())
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

// n parallel sequencer chains. Each sequencer transaction endorses 1 or 2 previous if possible
func (r *multiChainTestData) createSequencerChains2(pace int, howLong int) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
			counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		counter++
	}

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		ts := ledger.MaxTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		endorse := make([]*ledger.TransactionID, 0)
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			const B = 4
			endorse = endorse[:0]
			endorsedIdx := curChainIdx
			maxEndorsements := B
			if maxEndorsements > nChains {
				maxEndorsements = nChains
			}
			for k := 0; k < maxEndorsements; k++ {
				endorse = append(endorse, lastInChain(endorsedIdx).ID())
				if endorsedIdx == 0 {
					endorsedIdx = nChains - 1
				} else {
					endorsedIdx--
				}
				if lastInChain(endorsedIdx).Slot() != ts.Slot() {
					break
				}
			}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:    stemOut,
			Endorsements: endorse,
			Timestamp:    ts,
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if stemOut == nil {
			lst := make([]string, 0)
			for _, txid := range endorse {
				lst = append(lst, txid.StringShort())
			}
			r.t.Logf("%d : chain #%d, txid: %s, ts: %s, endorse: (%s)",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), strings.Join(lst, ","))
		} else {
			r.t.Logf("%d : chain #%d, txid: %s, ts: %s <- branch tx",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String())
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

// n parallel sequencer chains. Each sequencer transaction endorses 1 or 2 previous if possible
// adding faucet transactions in between
func (r *multiChainTestData) createSequencerChains3(pace int, howLong int, printTx bool) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		if printTx {
			r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
				counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		}
		counter++
	}

	faucetOutput := r.faucetOrigin

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var tx *transaction2.Transaction
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		// create faucet tx
		td := txbuilder2.NewTransferData(r.faucetPrivKey, r.faucetAddr, faucetOutput.Timestamp().AddTicks(ledger.TransactionPaceInTicks))
		td.WithTargetLock(ledger.ChainLockFromChainID(r.chainOrigins[nextChainIdx].ChainID)).
			WithAmount(100).
			MustWithInputs(faucetOutput)
		txBytes, err = txbuilder2.MakeTransferTransaction(td)
		require.NoError(r.t, err)
		tx, err = transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		faucetOutput = tx.MustProducedOutputWithIDAt(0)
		feeOutput := tx.MustProducedOutputWithIDAt(1)
		ret = append(ret, txBytes)
		if printTx {
			r.t.Logf("faucet tx %s: amount left on faucet: %d", tx.IDShortString(), faucetOutput.Output.Amount())
		}

		ts := ledger.MaxTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
			tx.Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		endorse := make([]*ledger.TransactionID, 0)
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			const B = 4
			endorse = endorse[:0]
			endorsedIdx := curChainIdx
			maxEndorsements := B
			if maxEndorsements > nChains {
				maxEndorsements = nChains
			}
			for k := 0; k < maxEndorsements; k++ {
				endorse = append(endorse, lastInChain(endorsedIdx).ID())
				if endorsedIdx == 0 {
					endorsedIdx = nChains - 1
				} else {
					endorsedIdx--
				}
				if lastInChain(endorsedIdx).Slot() != ts.Slot() {
					break
				}
			}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:        stemOut,
			AdditionalInputs: []*ledger.OutputWithID{feeOutput},
			Endorsements:     endorse,
			Timestamp:        ts,
			PrivateKey:       r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if printTx {
			total := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0).Output.Amount()
			if stemOut == nil {
				lst := make([]string, 0)
				for _, txid := range endorse {
					lst = append(lst, txid.StringShort())
				}
				r.t.Logf("%d : chain #%d, txid: %s, ts: %s, total: %d, endorse: (%s)",
					i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), total, strings.Join(lst, ","))
			} else {
				r.t.Logf("%d : chain #%d, txid: %s, ts: %s, total: %d <- branch tx",
					i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), total)
			}
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

func TestMultiChainWorkflow(t *testing.T) {
	t.Run("one chain past time", func(t *testing.T) {
		const (
			nChains              = 1
			howLong              = 100
			chainPaceInTimeTicks = 23
			printBranchTx        = false
		)
		r := initMultiChainTest(t, nChains, false, 60)
		txBytesSeq := r.createSequencerChain1(0, chainPaceInTimeTicks, true, func(i int, tx *transaction2.Transaction) bool {
			return i == howLong
		})
		require.EqualValues(t, howLong, len(txBytesSeq))

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		cd := countdown.New(howLong*nChains, 10*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		var listenCounter atomic.Uint32
		err := wrk.Events().ListenSequencer(r.chainOrigins[0].ChainID, func(vid *utangle_old.WrappedTx) {
			//t.Logf("listen seq %s: %s", r.chainOrigins[0].ChainID.StringShort(), vertex.Tx.IDShortString())
			listenCounter.Inc()
		})

		wrk.Start()

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			if tx.IsBranchTransaction() {
				t.Logf("append %d txid = %s <-- branch transaction", i, tx.IDShortString())
			} else {
				t.Logf("append %d txid = %s", i, tx.IDShortString())
			}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			err = wrk.TransactionIn(txBytes)
			require.NoError(r.t, err)
		}

		err = cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		require.EqualValues(t, howLong*nChains, listenCounter.Load())

		t.Logf("%s", r.ut.Info())
	})
	t.Run("one chain real time", func(t *testing.T) {
		const (
			nChains              = 1
			howLong              = 10
			chainPaceInTimeSlots = 23
			printBranchTx        = false
		)
		r := initMultiChainTest(t, nChains, false, 0)
		txBytesSeq := r.createSequencerChain1(0, chainPaceInTimeSlots, true, func(i int, tx *transaction2.Transaction) bool {
			return i == howLong
		})
		require.EqualValues(t, howLong, len(txBytesSeq))

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		cd := countdown.New(howLong*nChains, 10*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			if tx.IsBranchTransaction() {
				t.Logf("append %d txid = %s <-- branch transaction", i, tx.IDShortString())
			} else {
				t.Logf("append %d txid = %s", i, tx.IDShortString())
			}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			err = wrk.TransactionIn(txBytes)
			require.NoError(r.t, err)
		}

		err := cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("%s", r.ut.Info())
	})
	t.Run("several chains until branch real time", func(t *testing.T) {
		const (
			nChains              = 15
			chainPaceInTimeSlots = 13
			printBranchTx        = false
		)
		r := initMultiChainTest(t, nChains, false, 0)

		txBytesSeq := make([][][]byte, nChains)
		for i := range txBytesSeq {
			txBytesSeq[i] = r.createSequencerChain1(i, chainPaceInTimeSlots+i, false, func(i int, tx *transaction2.Transaction) bool {
				// until first branch
				return i > 0 && tx.IsBranchTransaction()
			})
			t.Logf("seq %d, length: %d", i, len(txBytesSeq[i]))
		}

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore, workflow.WithConsumerLogLevel(workflow.PreValidateConsumerName, zapcore.DebugLevel))
		nTransactions := 0
		for i := range txBytesSeq {
			nTransactions += len(txBytesSeq[i])
		}
		t.Logf("number of transactions: %d", nTransactions)
		cd := countdown.New(nTransactions, 10*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for seqIdx := range txBytesSeq {
			for i, txBytes := range txBytesSeq[seqIdx] {
				//r.t.Logf("tangle info: %s", r.ut.Info())
				tx, err := transaction2.FromBytes(txBytes)
				require.NoError(r.t, err)
				//if tx.IsBranchTransaction() {
				//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
				//} else {
				//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
				//}
				if tx.IsBranchTransaction() {
					if printBranchTx {
						t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
					}
				}
				err = wrk.TransactionIn(txBytes)
				require.NoError(r.t, err)
			}

		}
		err := cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
	t.Run("several chains until branch past time", func(t *testing.T) {
		const (
			nChains              = 15
			chainPaceInTimeSlots = 13
			printBranchTx        = false
			nowait               = true
		)
		r := initMultiChainTest(t, nChains, false, 60)

		txBytesSeq := make([][][]byte, nChains)
		for i := range txBytesSeq {
			txBytesSeq[i] = r.createSequencerChain1(i, chainPaceInTimeSlots+i, false, func(i int, tx *transaction2.Transaction) bool {
				// until first branch
				return i > 0 && tx.IsBranchTransaction()
			})
			t.Logf("seq %d, length: %d", i, len(txBytesSeq[i]))
		}

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore) //workflow_old.WithConsumerLogLevel(workflow_old.PreValidateConsumerName, zapcore.DebugLevel),
		//workflow_old.WithConsumerLogLevel(workflow_old.SolidifyConsumerName, zapcore.DebugLevel),
		//workflow_old.WithConsumerLogLevel(workflow_old.ValidateConsumerName, zapcore.DebugLevel),
		//workflow_old.WithConsumerLogLevel(workflow_old.AppendTxConsumerName, zapcore.DebugLevel),

		nTransactions := 0
		for i := range txBytesSeq {
			nTransactions += len(txBytesSeq[i])
		}
		t.Logf("number of transactions: %d", nTransactions)
		cd := countdown.New(nTransactions, 5*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for seqIdx := range txBytesSeq {
			for i, txBytes := range txBytesSeq[seqIdx] {
				//r.t.Logf("tangle info: %s", r.ut.Info())
				tx, err := transaction2.FromBytes(txBytes)
				require.NoError(r.t, err)
				//if tx.IsBranchTransaction() {
				//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
				//} else {
				//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
				//}
				if tx.IsBranchTransaction() {
					if printBranchTx {
						t.Logf("branch tx %d : %s", i, r.ut.TransactionStringFromBytes(txBytes))
					}
				}
				if nowait {
					err = wrk.TransactionIn(txBytes)
				} else {
					_, err = wrk.TransactionInWaitAppend(txBytes, 5*time.Second)
				}
				require.NoError(r.t, err)
			}

		}
		err := cd.Wait()
		if err != nil {
			t.Logf("==== counter info: %s", wrk.CounterInfo())
			//t.Logf("====== %s", wrk.DumpUnresolvedDependencies().String()) // <<<<<< ???
		}
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
	t.Run("endorse conflicting chain", func(t *testing.T) {
		const (
			nChains              = 2
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			howLong              = 50
			realTime             = false
		)
		var r *multiChainTestData
		if realTime {
			r = initMultiChainTest(t, nChains, false, 0)
		} else {
			r = initMultiChainTest(t, nChains, false, 60)
		}

		txBytesSeq := make([][][]byte, nChains)
		for i := range txBytesSeq {
			numBranches := 0
			txBytesSeq[i] = r.createSequencerChain1(i, chainPaceInTimeSlots, false, func(i int, tx *transaction2.Transaction) bool {
				// up to given length and first non branch tx
				if tx != nil && tx.IsBranchTransaction() {
					numBranches++
				}
				return i >= howLong && numBranches > 0 && !tx.IsBranchTransaction()
			})
			t.Logf("seq %d, length: %d", i, len(txBytesSeq[i]))
		}
		// take the last transaction of the second sequence
		txBytes := txBytesSeq[1][len(txBytesSeq[1])-1]
		txEndorser, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(t, err)
		require.True(t, txEndorser.IsSequencerMilestone())
		require.False(t, txEndorser.IsBranchTransaction())
		require.EqualValues(t, 1, txEndorser.NumProducedOutputs())
		out := txEndorser.MustProducedOutputWithIDAt(0)
		t.Logf("output to consume:\n%s", out.Short())

		idToBeEndorsed, tsToBeEndorsed, err := transaction2.IDAndTimestampFromTransactionBytes(txBytesSeq[0][len(txBytesSeq[0])-1])
		require.NoError(t, err)
		ts := ledger.MaxTime(tsToBeEndorsed, txEndorser.Timestamp())
		ts = ts.AddTicks(ledger.TransactionPaceInTicks)
		t.Logf("timestamp to be endorsed: %s, endorser's timestamp: %s", tsToBeEndorsed.String(), ts.String())
		require.True(t, ts.Slot() != 0 && ts.Slot() == txEndorser.Timestamp().Slot())
		t.Logf("ID to be endorsed: %s", idToBeEndorsed.StringShort())

		txBytesConflict, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *out,
				ChainID:      r.chainOrigins[1].ChainID,
			},
			Timestamp:    ts,
			Endorsements: []*ledger.TransactionID{&idToBeEndorsed},
			PrivateKey:   r.privKey,
		})
		require.NoError(t, err)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		nTransactions := 0
		for i := range txBytesSeq {
			nTransactions += len(txBytesSeq[i])
		}
		t.Logf("number of transactions: %d", nTransactions)
		cd := countdown.New(nTransactions, 10*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for seqIdx := range txBytesSeq {
			for i, txBytes := range txBytesSeq[seqIdx] {
				tx, err := transaction2.FromBytes(txBytes)
				require.NoError(r.t, err)
				//if tx.IsBranchTransaction() {
				//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
				//} else {
				//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
				//}
				if tx.IsBranchTransaction() {
					if printBranchTx {
						t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
					}
				}
				err = wrk.TransactionIn(txBytes)
				require.NoError(r.t, err)
			}
		}
		err = wrk.TransactionIn(txBytesConflict)
		require.NoError(r.t, err)

		err = cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
	t.Run("cross endorsing chains 1", func(t *testing.T) {
		const (
			nChains              = 15
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			howLong              = 500 // 1000
			realTime             = false
			nowait               = true
		)
		var r *multiChainTestData
		if realTime {
			r = initMultiChainTest(t, nChains, false, 0)
		} else {
			r = initMultiChainTest(t, nChains, false, 60)
		}

		txBytesSeq := r.createSequencerChains1(chainPaceInTimeSlots, howLong)
		require.EqualValues(t, howLong, len(txBytesSeq))
		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		cd := countdown.New(howLong, 20*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			if nowait {
				err = wrk.TransactionIn(txBytes)
			} else {
				_, err = wrk.TransactionInWaitAppend(txBytes, 5*time.Second)
			}
			require.NoError(r.t, err)
		}

		err := cd.Wait()
		wrk.Stop()
		t.Logf("length of the pull list: %d", wrk.PullListLen())
		if err != nil {
			t.Logf("===== counters: %s", wrk.CounterInfo())
		}
		require.NoError(t, err)
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
	t.Run("cross multi-endorsing chains", func(t *testing.T) {
		const (
			nChains              = 5
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			howLong              = 1000
			realTime             = false
		)
		var r *multiChainTestData
		if realTime {
			r = initMultiChainTest(t, nChains, false, 0)
		} else {
			r = initMultiChainTest(t, nChains, false, 60)
		}

		txBytesSeq := r.createSequencerChains2(chainPaceInTimeSlots, howLong)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		cd := countdown.New(howLong, 10*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			err = wrk.TransactionIn(txBytes)
			require.NoError(r.t, err)
		}
		err := cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
	t.Run("cross multi-endorsing chains with fees", func(t *testing.T) {
		const (
			nChains              = 5
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			printTx              = false
			howLong              = 50 // 505 fails due to not enough tokens in the faucet
			realTime             = true
		)
		var r *multiChainTestData
		if realTime {
			r = initMultiChainTest(t, nChains, false, 0)
		} else {
			r = initMultiChainTest(t, nChains, false, 60)
		}

		txBytesSeq := r.createSequencerChains3(chainPaceInTimeSlots, howLong, printTx)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		wrk := workflow.New(r.ut, peering.NewPeersDummy(), r.txBytesStore)
		cd := countdown.New(len(txBytesSeq), 20*time.Second)
		wrk.MustOnEvent(workflow.EventNewVertex, func(_ *workflow.NewVertexEventData) {
			cd.Tick()
		})
		wrk.Start()

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			err = wrk.TransactionIn(txBytes)
			require.NoError(r.t, err)
		}
		err := cd.Wait()
		require.NoError(t, err)
		wrk.Stop()
		t.Logf("UTXO tangle:\n%s", r.ut.Info())
	})
}
