package utangle

import (
	"context"
	"crypto/ed25519"
	"fmt"
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
	"github.com/lunfardo314/proxima/utangle/poker"
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
	poker        *poker.Poker
}

func newTestingWorkflow(txBytesStore global.TxBytesStore, dag *dag.DAG, ctx context.Context) *testingWorkflow {
	return &testingWorkflow{
		txBytesStore: txBytesStore,
		DAG:          dag,
		poker:        poker.New(ctx),
		log:          global.NewLogger("", zap.InfoLevel, nil, ""),
	}
}

func (w *testingWorkflow) Pull(txid core.TransactionID) {
	w.log.Infof("pull request %s", txid.StringShort())
	go func() {
		txBytes := w.txBytesStore.GetTxBytes(&txid)
		if len(txBytes) == 0 {
			return
		}
		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		util.AssertNoError(err, "transaction.FromBytes")
		w.log.Infof("pull send %s", txid.StringShort())
		attacher.AttachTransaction(tx, w)
	}()
}

const tracePoking = false

func (w *testingWorkflow) PokeMe(me, with *vertex.WrappedTx) {
	if tracePoking {
		w.log.Infof("poke me %s with %s", me.IDShortString(), with.IDShortString())
	}
	w.poker.PokeMe(me, with)
}

func (w *testingWorkflow) PokeAllWith(wanted *vertex.WrappedTx) {
	if tracePoking {
		w.log.Infof("poke all with %s", wanted.IDShortString())
	}
	w.poker.PokeAllWith(wanted)
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
	originBranchTxid       core.TransactionID
	distributionBranchTxID core.TransactionID
	privKey                ed25519.PrivateKey
	addr                   core.AddressED25519
	privKeyAux             ed25519.PrivateKey
	addrAux                core.AddressED25519
	stateIdentity          genesis.LedgerIdentityData
	forkOutput             *core.OutputWithID
	auxOutput              *core.OutputWithID
	txBytes                [][]byte
	conflictingOutputs     []*core.OutputWithID
	chainOrigins           []*core.OutputWithChainID
	pkController           []ed25519.PrivateKey
	chainOriginsTx         *transaction.Transaction
	seqChain               [][]*transaction.Transaction
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

	ret.wrk = newTestingWorkflow(ret.txStore, dag.New(stateStore), context.Background())

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

func (td *longConflictTestData) makeSeqBeginnings(withConflictingFees bool) {
	util.Assertf(len(td.chainOrigins) == len(td.conflictingOutputs), "td.chainOrigins)==len(td.conflictingOutputs)")
	td.seqChain = make([][]*transaction.Transaction, len(td.chainOrigins))
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
		td.seqChain[i] = make([]*transaction.Transaction, 0)
		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "1",
			ChainInput:       chainOrigin,
			Timestamp:        ts,
			Endorsements:     []*core.TransactionID{&td.distributionBranchTxID},
			PrivateKey:       td.privKeyAux,
			AdditionalInputs: additionalIn,
		})
		require.NoError(td.t, err)
		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(td.t, err)
		td.seqChain[i] = append(td.seqChain[i], tx)
	}
}

func (td *longConflictTestData) makeSeqChains(howLong int) {
	for i := 0; i < howLong; i++ {
		for seqNr := range td.seqChain {
			endorsedSeqNr := (seqNr + 1) % len(td.seqChain)
			endorse := td.seqChain[endorsedSeqNr][i].ID()
			txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:      fmt.Sprintf("seq%d", i),
				ChainInput:   td.seqChain[seqNr][i].SequencerOutput().MustAsChainOutput(),
				StemInput:    nil,
				Timestamp:    td.seqChain[seqNr][i].Timestamp().AddTicks(core.TransactionPaceInTicks),
				Endorsements: util.List(endorse),
				PrivateKey:   td.privKeyAux,
			})
			require.NoError(td.t, err)
			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			require.NoError(td.t, err)
			td.seqChain[seqNr] = append(td.seqChain[seqNr], tx)
		}
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

func (td *longConflictTestData) storeTxBytes(txBytesMulti ...[]byte) {
	for _, txBytes := range txBytesMulti {
		err := td.wrk.txBytesStore.SaveTxBytes(txBytes)
		require.NoError(td.t, err)
	}
}

func (td *longConflictTestData) storeTransactions(txs ...*transaction.Transaction) {
	txBytes := make([][]byte, len(txs))
	for i, tx := range txs {
		txBytes[i] = tx.Bytes()
	}
	td.storeTxBytes(txBytes...)
}

func (td *longConflictTestData) attachTxBytes(txBytesMulti ...[]byte) {
	for _, txBytes := range txBytesMulti {
		_, err := attacher.AttachTransactionFromBytes(txBytes, td.wrk)
		require.NoError(td.t, err)
	}
}

func (td *longConflictTestData) attachTransactions(txs ...*transaction.Transaction) {
	for _, tx := range txs {
		attacher.AttachTransaction(tx, td.wrk)
	}
}

func (td *longConflictTestData) txBytesToStore() {
	err := td.txStore.SaveTxBytes(td.chainOriginsTx.Bytes())
	require.NoError(td.t, err)

	td.storeTxBytes(td.txBytes...)
	for _, txSeq := range td.txSequences {
		td.storeTxBytes(txSeq...)
	}
}

func (td *longConflictTestData) txBytesAttach() {
	_, err := attacher.AttachTransactionFromBytes(td.chainOriginsTx.Bytes(), td.wrk)
	require.NoError(td.t, err)

	td.attachTxBytes(td.txBytes...)
	for _, txSeq := range td.txSequences {
		td.attachTxBytes(txSeq...)
	}
}

func (td *longConflictTestData) printTxIDs() {
	td.t.Logf("Origin branch txid: %s", td.originBranchTxid.StringShort())
	td.t.Logf("Distribution txid: %s", td.distributionBranchTxID.StringShort())
	td.t.Logf("Fork output: %s", td.forkOutput.ID.StringShort())
	td.t.Logf("Aux output: %s", td.auxOutput.ID.StringShort())
	td.t.Logf("Conflicting outputs (%d):", len(td.conflictingOutputs))
	for i, o := range td.conflictingOutputs {
		td.t.Logf("%2d: conflicting chain start: %s", i, o.ID.StringShort())
		for j, txBytes := range td.txSequences[i] {
			txid, _, _ := transaction.IDAndTimestampFromTransactionBytes(txBytes)
			td.t.Logf("      %2d : %s", j, txid.StringShort())
		}
	}
	td.t.Logf("-------------- Sequencer chains-----------")
	for i, seqChain := range td.seqChain {
		td.t.Logf("seq chain #%d, len = %d", i, len(seqChain))
		for j, tx := range seqChain {
			td.t.Logf("       %2d : %s", j, tx.IDShortString())
		}
	}
}
