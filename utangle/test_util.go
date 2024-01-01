package utangle

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle/attacher"
	"github.com/lunfardo314/proxima/utangle/dag"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/blake2b"
)

type testingWorkflow struct {
	*dag.DAG
	txBytesStore global.TxBytesStore
	log          *zap.SugaredLogger
}

func newTestingWorkflow(txBytesStore global.TxBytesStore, dag *dag.DAG) *testingWorkflow {
	return &testingWorkflow{
		txBytesStore: txBytesStore,
		DAG:          dag,
		log:          global.NewLogger("", zap.InfoLevel, nil, ""),
	}
}

func (w *testingWorkflow) Pull(txid core.TransactionID) {
	go func() {
		w.log.Infof("pull %s", txid.StringShort())
		txBytes := w.txBytesStore.GetTxBytes(&txid)
		if len(txBytes) == 0 {
			return
		}
		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		util.AssertNoError(err, "transaction.FromBytes")
		attacher.AttachTransaction(tx, w)
	}()
}

func (w *testingWorkflow) OnChangeNotify(onChange, notify *vertex.WrappedTx) {
}

func (w *testingWorkflow) Notify(changed *vertex.WrappedTx) {
}

func (w *testingWorkflow) Log() *zap.SugaredLogger {
	return w.log
}

func (w *testingWorkflow) syncLog() {
	_ = w.log.Sync()
}

type conflictTestData struct {
	t                      *testing.T
	wrk                    *testingWorkflow
	txStore                global.TxBytesStore
	bootstrapChainID       core.ChainID
	distributionBranchTxID core.TransactionID
	privKey                ed25519.PrivateKey
	addr                   core.AddressED25519
	privKeyAux             ed25519.PrivateKey
	addrAux                core.AddressED25519
	stateIdentity          genesis.LedgerIdentityData
	originBranchTxid       core.TransactionID
	forkOutput             *core.OutputWithID
	auxOutput              *core.OutputWithID
	txBytes                [][]byte
	conflictingOutputs     []*core.OutputWithID
	chainOrigins           []*core.OutputWithChainID
	pkController           []ed25519.PrivateKey
	chainOriginsTx         *transaction.Transaction
	seqStart               [][]byte
}

func initConflictTest(t *testing.T, nConflicts int, nChains int, targetLockChain bool) *conflictTestData {
	const initBalance = 10_000_000
	genesisPrivKey := testutil.GetTestingPrivateKey()
	par := genesis.DefaultIdentityData(genesisPrivKey)
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistribution(2, initBalance)
	if nChains > 0 {
		distrib[1] = core.LockBalance{
			Lock:    addrs[1],
			Balance: uint64(initBalance * nChains),
		}
	}
	ret := &conflictTestData{
		t:             t,
		stateIdentity: *par,
		privKey:       privKeys[0],
		addr:          addrs[0],
		privKeyAux:    privKeys[1],
		addrAux:       addrs[1],
	}
	require.True(t, core.AddressED25519MatchesPrivateKey(ret.addr, ret.privKey))

	ret.pkController = make([]ed25519.PrivateKey, nConflicts)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	stateStore := common.NewInMemoryKVStore()
	ret.txStore = txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

	var genesisRoot common.VCommitment
	ret.bootstrapChainID, genesisRoot = genesis.InitLedgerState(ret.stateIdentity, stateStore)
	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)
	err = ret.txStore.SaveTxBytes(txBytes)
	require.NoError(t, err)

	ret.distributionBranchTxID, _, err = transaction.IDAndTimestampFromTransactionBytes(txBytes)
	require.NoError(t, err)

	const printDistributionTx = false
	if printDistributionTx {
		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(t, err)
		genesisState := multistate.MustNewReadable(stateStore, genesisRoot)
		t.Logf("--------------- distribution tx:\n%s\n--------------", tx.ToString(genesisState.GetUTXO))
	}

	ret.wrk = newTestingWorkflow(ret.txStore, dag.New(stateStore))

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	t.Logf("origing branch txid: %s", ret.originBranchTxid.StringShort())

	for i := range distrib {
		t.Logf("distributed %s -> %s", util.GoThousands(distrib[i].Balance), distrib[i].Lock.String())
	}
	t.Logf("%s", ret.wrk.Info())

	err = attacher.EnsureLatestBranches(ret.wrk)
	require.NoError(t, err)

	rdr := ret.wrk.HeaviestStateForLatestTimeSlot()
	bal, _ := multistate.BalanceOnLock(rdr, ret.addr)
	require.EqualValues(t, initBalance, int(bal))

	oDatas, err := rdr.GetUTXOsLockedInAccount(ret.addr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.forkOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.Amount()))
	t.Logf("forked output ID: %s", ret.forkOutput.IDShort())

	oDatas, err = rdr.GetUTXOsLockedInAccount(ret.addrAux.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.auxOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.Amount()))
	t.Logf("auxiliary output ID: %s", ret.forkOutput.IDShort())

	ret.txBytes = make([][]byte, nConflicts)

	td := txbuilder.NewTransferData(ret.privKey, ret.addr, core.LogicalTimeNow()).
		MustWithInputs(ret.forkOutput)

	for i := 0; i < nConflicts; i++ {
		td.WithAmount(uint64(100 + i))
		if targetLockChain {
			td.WithTargetLock(core.ChainLockFromChainID(ret.bootstrapChainID))
		} else {
			td.WithTargetLock(ret.addr)
		}
		ret.txBytes[i], err = txbuilder.MakeTransferTransaction(td)
		require.NoError(t, err)
	}
	require.EqualValues(t, nConflicts, len(ret.txBytes))

	ret.conflictingOutputs = make([]*core.OutputWithID, nConflicts)
	for i := range ret.conflictingOutputs {
		tx, err := transaction.FromBytesMainChecksWithOpt(ret.txBytes[i])
		require.NoError(t, err)
		ret.conflictingOutputs[i] = tx.MustProducedOutputWithIDAt(1)
		require.EqualValues(t, 100+i, int(ret.conflictingOutputs[i].Output.Amount()))
	}
	return ret
}

// makes chain origins transaction from aux output
func (td *conflictTestData) makeChainOrigins(n int) {
	if n == 0 {
		return
	}
	txb := txbuilder.NewTransactionBuilder()
	_, _ = txb.ConsumeOutputWithID(td.auxOutput)
	txb.PutSignatureUnlock(0)
	amount := td.auxOutput.Output.Amount() / uint64(n)
	for i := 0; i < n; i++ {
		o := core.NewOutput(func(o *core.Output) {
			o.WithAmount(amount)
			o.WithLock(td.addrAux)
			_, _ = o.PushConstraint(core.NewChainOrigin().Bytes())
		})
		_, _ = txb.ProduceOutput(o)
	}
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.TransactionData.Timestamp = td.auxOutput.Timestamp().AddTicks(core.TransactionPaceInTicks)
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(td.privKeyAux)

	var err error
	txBytes := txb.TransactionData.Bytes()
	td.chainOriginsTx, err = transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	require.NoError(td.t, err)
	td.chainOrigins = make([]*core.OutputWithChainID, n)
	td.chainOriginsTx.ForEachProducedOutput(func(idx byte, o *core.Output, oid *core.OutputID) bool {
		td.chainOrigins[idx] = &core.OutputWithChainID{
			OutputWithID: core.OutputWithID{
				ID:     *oid,
				Output: o,
			},
			ChainID: blake2b.Sum256(oid[:]),
		}
		td.t.Logf("chain origin %s : %s", oid.StringShort(), td.chainOrigins[idx].ChainID.String())
		return true
	})
}

func (td *longConflictTestData) makeSeqStarts(withConflictingFees bool) {
	util.Assertf(len(td.chainOrigins) == len(td.conflictingOutputs), "td.chainOrigins)==len(td.conflictingOutputs)")
	var err error
	td.seqStart = make([][]byte, len(td.chainOrigins))
	var additionalIn []*core.OutputWithID
	for i, chainOrigin := range td.chainOrigins {
		var ts core.LogicalTime
		if withConflictingFees {
			additionalIn = []*core.OutputWithID{td.terminalOutputs[i]}
			ts = core.MaxLogicalTime(chainOrigin.Timestamp(), td.terminalOutputs[i].Timestamp())
		} else {
			additionalIn = nil
			ts = chainOrigin.Timestamp()
		}
		ts = ts.AddTicks(core.TransactionPaceInTicks)
		td.seqStart[i], err = txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "1",
			ChainInput:       chainOrigin,
			Timestamp:        ts,
			Endorsements:     []*core.TransactionID{&td.distributionBranchTxID},
			PrivateKey:       td.privKeyAux,
			AdditionalInputs: additionalIn,
		})
		require.NoError(td.t, err)
	}
}

func (td *conflictTestData) logDAGInfo() {
	td.t.Logf("DAG INFO:\n%s", td.wrk.Info())
	slot := td.wrk.LatestBranchSlot()
	td.t.Logf("VERTICES in the latest slot %d\n%s", slot, td.wrk.LinesVerticesInSlotAndAfter(slot).String())
}

type longConflictTestData struct {
	conflictTestData
	txSequences     [][][]byte
	terminalOutputs []*core.OutputWithID
}

func initLongConflictTestData(t *testing.T, nConflicts int, nChains int, howLong int) *longConflictTestData {
	util.Assertf(nChains == 0 || nChains == nConflicts, "nChains == 0 || nChains == nConflicts")
	ret := &longConflictTestData{
		conflictTestData: *initConflictTest(t, nConflicts, nChains, false),
		txSequences:      make([][][]byte, nConflicts),
		terminalOutputs:  make([]*core.OutputWithID, nConflicts),
	}
	ret.makeChainOrigins(nChains)
	var prev *core.OutputWithID
	var err error

	td := &ret.conflictTestData

	for seqNr, originOut := range ret.conflictingOutputs {
		ret.txSequences[seqNr] = make([][]byte, howLong)
		for i := 0; i < howLong; i++ {
			if i == 0 {
				prev = originOut
			}
			prev = originOut
			trd := txbuilder.NewTransferData(td.privKey, td.addr, originOut.Timestamp().AddTicks(core.TransactionPaceInTicks*(i+1)))
			trd.WithAmount(originOut.Output.Amount())
			trd.MustWithInputs(prev)
			if i < howLong-1 {
				trd.WithTargetLock(td.addr)
			} else {
				if nChains == 0 {
					trd.WithTargetLock(core.ChainLockFromChainID(ret.bootstrapChainID))
				} else {
					trd.WithTargetLock(core.ChainLockFromChainID(ret.chainOrigins[seqNr%nChains].ChainID))
				}
			}
			ret.txSequences[seqNr][i], err = txbuilder.MakeSimpleTransferTransaction(trd)
			require.NoError(t, err)

			tx, err := transaction.FromBytesMainChecksWithOpt(ret.txSequences[seqNr][i])
			require.NoError(t, err)

			prev = tx.MustProducedOutputWithIDAt(0)
			if i == howLong-1 {
				ret.terminalOutputs[seqNr] = prev
			}
		}
	}
	return ret
}

func (td *longConflictTestData) txBytesToStore() {
	err := td.txStore.SaveTxBytes(td.chainOriginsTx.Bytes())
	require.NoError(td.t, err)

	for _, txBytes := range td.txBytes {
		err = td.txStore.SaveTxBytes(txBytes)
		require.NoError(td.t, err)
	}
	for _, txSeq := range td.txSequences {
		for _, txBytes := range txSeq {
			err = td.txStore.SaveTxBytes(txBytes)
			require.NoError(td.t, err)
		}
	}
}

func (td *longConflictTestData) txBytesAttach() {
	_, err := attacher.AttachTransactionFromBytes(td.chainOriginsTx.Bytes(), td.wrk)
	require.NoError(td.t, err)

	for _, txBytes := range td.txBytes {
		_, err = attacher.AttachTransactionFromBytes(txBytes, td.wrk)
		require.NoError(td.t, err)
	}
	for _, txSeq := range td.txSequences {
		for _, txBytes := range txSeq {
			_, err = attacher.AttachTransactionFromBytes(txBytes, td.wrk)
			require.NoError(td.t, err)
		}
	}
}
