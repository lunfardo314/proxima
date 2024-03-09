package tests

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

type workflowDummyEnvironment struct {
	*global.Global
	stateStore   global.StateStore
	txBytesStore global.TxBytesStore
}

func (w *workflowDummyEnvironment) StateStore() global.StateStore {
	return w.stateStore
}

func (w *workflowDummyEnvironment) TxBytesStore() global.TxBytesStore {
	return w.txBytesStore
}

func newWorkflowDummyEnvironment(stateStore global.StateStore, txStore global.TxBytesStore) *workflowDummyEnvironment {
	return &workflowDummyEnvironment{
		Global:       global.NewDefault(),
		stateStore:   stateStore,
		txBytesStore: txStore,
	}
}

type workflowTestData struct {
	t                      *testing.T
	env                    *workflowDummyEnvironment
	wrk                    *workflow.Workflow
	txStore                global.TxBytesStore
	genesisPrivKey         ed25519.PrivateKey
	bootstrapChainID       ledger.ChainID
	originBranchTxid       ledger.TransactionID
	distributionBranchTxID ledger.TransactionID
	distributionBranchTx   *transaction.Transaction
	privKey                ed25519.PrivateKey
	addr                   ledger.AddressED25519
	privKeyAux             ed25519.PrivateKey
	addrAux                ledger.AddressED25519
	privKeyFaucet          ed25519.PrivateKey
	addrFaucet             ledger.AddressED25519
	stateIdentity          ledger.IdentityData
	forkOutput             *ledger.OutputWithID
	auxOutput              *ledger.OutputWithID
	faucetOutput           *ledger.OutputWithID
	remainderOutput        *ledger.OutputWithID
	txBytesConflicting     [][]byte
	conflictingOutputs     []*ledger.OutputWithID
	chainOrigins           []*ledger.OutputWithChainID
	pkController           []ed25519.PrivateKey
	chainOriginsTx         *transaction.Transaction
	seqChain               [][]*transaction.Transaction
	transferChain          []*transaction.Transaction
	bootstrapSeq           *sequencer.Sequencer
	sequencers             []*sequencer.Sequencer
}

type longConflictTestData struct {
	workflowTestData
	txSequences     [][][]byte
	terminalOutputs []*ledger.OutputWithID
}

const (
	initBalance = 10_000_000_000
	tagAlongFee = 500
)

func initWorkflowTest(t *testing.T, nChains int, startPruner ...bool) *workflowTestData {
	util.Assertf(nChains > 0, "nChains > 0")
	genesisPrivKey := testutil.GetTestingPrivateKey()
	stateID := ledger.DefaultIdentityData(genesisPrivKey)
	t.Logf("genesis state ID: %s", stateID.String())

	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistribution(initBalance, uint64(nChains*initBalance+tagAlongFee), initBalance)
	ret := &workflowTestData{
		t:              t,
		genesisPrivKey: genesisPrivKey,
		stateIdentity:  *stateID,
		privKey:        privKeys[0],
		addr:           addrs[0],
		privKeyAux:     privKeys[1],
		addrAux:        addrs[1],
		privKeyFaucet:  privKeys[2],
		addrFaucet:     addrs[2],
	}
	require.True(t, ledger.AddressED25519MatchesPrivateKey(ret.addr, ret.privKey))

	stateStore := common.NewInMemoryKVStore()
	ret.txStore = txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

	var genesisRoot common.VCommitment
	ret.bootstrapChainID, genesisRoot = multistate.InitStateStore(ret.stateIdentity, stateStore)
	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)
	_, err = ret.txStore.PersistTxBytesWithMetadata(txBytes, nil)
	require.NoError(t, err)

	ret.distributionBranchTx, err = transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	require.NoError(t, err)
	ret.distributionBranchTxID = *ret.distributionBranchTx.ID()
	t.Logf("distribution txID: %s", ret.distributionBranchTxID.StringShort())

	ret.faucetOutput = ret.distributionBranchTx.MustProducedOutputWithIDAt(4)
	const printDistributionTx = false
	if printDistributionTx {
		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(t, err)
		genesisState := multistate.MustNewReadable(stateStore, genesisRoot)
		t.Logf("--------------- distribution tx:\n%s\n--------------", tx.ToString(genesisState.GetUTXO))
		t.Logf("--------------- faucet output\n%s\n--------------", ret.faucetOutput)
	}

	ret.env = newWorkflowDummyEnvironment(stateStore, ret.txStore)
	if len(startPruner) > 0 && startPruner[0] {
		ret.wrk = workflow.New(ret.env, peering.NewPeersDummy())
	} else {
		ret.wrk = workflow.New(ret.env, peering.NewPeersDummy(), workflow.OptionDoNotStartPruner)
	}
	ret.wrk.Start()

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	t.Logf("origing branch txid: %s", ret.originBranchTxid.StringShort())

	for i := range distrib {
		t.Logf("distributed %s -> %s", util.GoTh(distrib[i].Balance), distrib[i].Lock.String())
	}
	return ret
}

func (td *workflowTestData) saveFullDAG(fname string) {
	branchTxIDS := multistate.FetchLatestBranchTransactionIDs(td.wrk.StateStore())
	tmpDag := memdag.MakeDAGFromTxStore(td.txStore, 0, branchTxIDS...)
	tmpDag.SaveGraph(fname)
}

// makes chain origins transaction from aux output
func (td *workflowTestData) makeChainOrigins(n int) {
	if n == 0 {
		return
	}
	rdr := td.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsLockedInAccount(td.addrAux.AccountID())
	require.NoError(td.t, err)
	require.EqualValues(td.t, 1, len(oDatas))

	td.auxOutput, err = oDatas[0].Parse()
	require.NoError(td.t, err)
	td.t.Logf("auxiliary output ID: %s", td.auxOutput.IDShort())

	txb := txbuilder.NewTransactionBuilder()
	_, _ = txb.ConsumeOutputWithID(td.auxOutput)
	txb.PutSignatureUnlock(0)

	amount := (td.auxOutput.Output.Amount() - tagAlongFee) / uint64(n)
	for i := 0; i < n; i++ {
		o := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(amount)
			o.WithLock(td.addrAux)
			_, _ = o.PushConstraint(ledger.NewChainOrigin().Bytes())
		})
		_, _ = txb.ProduceOutput(o)
	}
	tagAlongOut := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(tagAlongFee)
		o.WithLock(ledger.ChainLockFromChainID(td.bootstrapChainID))
	})
	_, _ = txb.ProduceOutput(tagAlongOut)

	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.TransactionData.Timestamp = td.auxOutput.Timestamp().AddTicks(ledger.TransactionPace())
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(td.privKeyAux)

	txBytes := txb.TransactionData.Bytes()
	td.chainOriginsTx, err = transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	require.NoError(td.t, err)
	td.chainOrigins = make([]*ledger.OutputWithChainID, n)
	td.chainOriginsTx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		if int(idx) >= n {
			return true
		}
		td.chainOrigins[idx] = &ledger.OutputWithChainID{
			OutputWithID: ledger.OutputWithID{
				ID:     *oid,
				Output: o,
			},
			ChainID: blake2b.Sum256(oid[:]),
		}
		td.t.Logf("chain origin %s : %s", oid.StringShort(), td.chainOrigins[idx].ChainID.String())
		return true
	})
}

func initWorkflowTestWithConflicts(t *testing.T, nConflicts int, nChains int, targetLockChain bool) *workflowTestData {
	ret := initWorkflowTest(t, nChains)

	ret.pkController = make([]ed25519.PrivateKey, nConflicts)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	err := attacher.EnsureLatestBranches(ret.wrk)
	require.NoError(t, err)
	t.Logf("%s", ret.wrk.Info())

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

	ret.txBytesConflicting = make([][]byte, nConflicts)

	ts := ret.forkOutput.Timestamp().AddTicks(ledger.TransactionPace())
	td := txbuilder.NewTransferData(ret.privKey, ret.addr, ts).
		MustWithInputs(ret.forkOutput)

	require.True(t, ledger.ValidTime(td.Timestamp))

	for i := 0; i < nConflicts; i++ {
		td.WithAmount(uint64(100_000 + i))
		if targetLockChain {
			td.WithTargetLock(ledger.ChainLockFromChainID(ret.bootstrapChainID))
		} else {
			td.WithTargetLock(ret.addr)
		}
		ret.txBytesConflicting[i], err = txbuilder.MakeTransferTransaction(td)
		require.NoError(t, err)
	}
	require.EqualValues(t, nConflicts, len(ret.txBytesConflicting))

	ret.conflictingOutputs = make([]*ledger.OutputWithID, nConflicts)
	for i := range ret.conflictingOutputs {
		tx, err := transaction.FromBytesMainChecksWithOpt(ret.txBytesConflicting[i])
		require.NoError(t, err)
		t.Logf("conflicting tx ts: %s", tx.Timestamp().String())
		ret.conflictingOutputs[i] = tx.MustProducedOutputWithIDAt(1)
		require.EqualValues(t, 100_000+i, int(ret.conflictingOutputs[i].Output.Amount()))
	}
	return ret
}

func (td *workflowTestData) stop() {
	td.env.Stop()
}

func (td *workflowTestData) waitStop(timeout ...time.Duration) {
	td.env.MustWaitAllWorkProcessesStop(timeout...)
}

func (td *workflowTestData) stopAndWait(timeout ...time.Duration) {
	td.env.Stop()
	td.env.MustWaitAllWorkProcessesStop(timeout...)
}

func (td *longConflictTestData) makeSeqBeginnings(withConflictingFees bool) {
	util.Assertf(len(td.chainOrigins) == len(td.conflictingOutputs), "td.chainOrigins)==len(td.conflictingOutputs)")
	td.seqChain = make([][]*transaction.Transaction, len(td.chainOrigins))
	var additionalIn []*ledger.OutputWithID
	for i, chainOrigin := range td.chainOrigins {
		var ts ledger.Time
		if withConflictingFees {
			additionalIn = []*ledger.OutputWithID{td.terminalOutputs[i]}
			ts = ledger.MaxTime(chainOrigin.Timestamp(), td.terminalOutputs[i].Timestamp())
		} else {
			additionalIn = nil
			ts = chainOrigin.Timestamp()
		}
		ts = ts.AddTicks(ledger.TransactionPaceSequencer())

		td.seqChain[i] = make([]*transaction.Transaction, 0)
		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:          "1",
			ChainInput:       chainOrigin,
			Timestamp:        ts,
			Endorsements:     []*ledger.TransactionID{&td.distributionBranchTxID},
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
			txBytesSeq, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:      fmt.Sprintf("seq%d", seqNr),
				ChainInput:   td.seqChain[seqNr][i].SequencerOutput().MustAsChainOutput(),
				Timestamp:    td.seqChain[seqNr][i].Timestamp().AddTicks(ledger.TransactionPaceSequencer()),
				Endorsements: util.List(endorse),
				PrivateKey:   td.privKeyAux,
			})
			require.NoError(td.t, err)
			tx, err := transaction.FromBytes(txBytesSeq, transaction.MainTxValidationOptions...)
			require.NoError(td.t, err)
			td.seqChain[seqNr] = append(td.seqChain[seqNr], tx)
		}
	}
}

func (td *longConflictTestData) makeSlotTransactions(howLongChain int, extendBegin []*transaction.Transaction) [][]*transaction.Transaction {
	ret := make([][]*transaction.Transaction, len(extendBegin))
	var extend *ledger.OutputWithChainID
	var endorse *ledger.TransactionID
	var ts ledger.Time

	for i := 0; i < howLongChain; i++ {
		for seqNr := range ret {
			if i == 0 {
				ret[seqNr] = make([]*transaction.Transaction, 0)
				extend = extendBegin[seqNr].SequencerOutput().MustAsChainOutput()
				endorseIdx := (seqNr + 1) % len(extendBegin)
				endorse = extendBegin[endorseIdx].ID()
			} else {
				extend = ret[seqNr][i-1].SequencerOutput().MustAsChainOutput()
				endorseIdx := (seqNr + 1) % len(extendBegin)
				endorse = ret[endorseIdx][i-1].ID()
			}
			ts = ledger.MaxTime(endorse.Timestamp(), extend.Timestamp()).AddTicks(ledger.TransactionPaceSequencer())

			txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:      fmt.Sprintf("seq%d", i),
				ChainInput:   extend,
				Timestamp:    ts,
				Endorsements: util.List(endorse),
				PrivateKey:   td.privKeyAux,
			})
			require.NoError(td.t, err)
			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			require.NoError(td.t, err)
			ret[seqNr] = append(ret[seqNr], tx)
			td.t.Logf("%3d  %s", seqNr, tx.IDShortString())
		}
	}

	return ret
}

func (td *longConflictTestData) makeSlotTransactionsWithTagAlong(howLongChain int, extendBegin []*transaction.Transaction, inflate ...bool) [][]*transaction.Transaction {
	ret := make([][]*transaction.Transaction, len(extendBegin))
	var extend *ledger.OutputWithChainID
	var endorse *ledger.TransactionID
	var ts ledger.Time

	if td.remainderOutput == nil {
		td.transferChain = make([]*transaction.Transaction, 0)
		td.remainderOutput = td.conflictingOutputs[0]
	}
	var txSpend *transaction.Transaction

	for i := 0; i < howLongChain; i++ {
		for seqNr := range ret {
			txSpend = td.spendToChain(td.remainderOutput, td.chainOrigins[seqNr].ChainID)
			td.transferChain = append(td.transferChain, txSpend)
			util.Assertf(txSpend.NumProducedOutputs() == 2, "txSpend.NumProducedOutputs() == 2")

			td.remainderOutput = txSpend.MustProducedOutputWithIDAt(0)
			transferOut := txSpend.MustProducedOutputWithIDAt(1)

			if i == 0 {
				ret[seqNr] = make([]*transaction.Transaction, 0)
				extend = extendBegin[seqNr].SequencerOutput().MustAsChainOutput()
				endorseIdx := (seqNr + 1) % len(extendBegin)
				endorse = extendBegin[endorseIdx].ID()
			} else {
				extend = ret[seqNr][i-1].SequencerOutput().MustAsChainOutput()
				endorseIdx := (seqNr + 1) % len(extendBegin)
				endorse = ret[endorseIdx][i-1].ID()
			}
			ts = ledger.MaxTime(endorse.Timestamp(), extend.Timestamp(), transferOut.Timestamp()).AddTicks(ledger.TransactionPaceSequencer())

			txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
				SeqName:          fmt.Sprintf("seq%d", i),
				ChainInput:       extend,
				AdditionalInputs: []*ledger.OutputWithID{transferOut},
				Timestamp:        ts,
				Endorsements:     util.List(endorse),
				PrivateKey:       td.privKeyAux,
			})
			require.NoError(td.t, err)
			tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
			require.NoError(td.t, err)
			ret[seqNr] = append(ret[seqNr], tx)
		}
	}

	return ret
}

func (td *longConflictTestData) makeBranch(extend *ledger.OutputWithChainID, prevBranch *transaction.Transaction) *transaction.Transaction {
	td.t.Logf("extendTS: %s, prevBranchTS: %s", extend.Timestamp().String(), prevBranch.Timestamp().String())
	require.True(td.t, extend.Timestamp().After(prevBranch.Timestamp()))

	txBytesBranch, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
		SeqName:    "seq0",
		ChainInput: extend,
		StemInput:  prevBranch.StemOutput(),
		Timestamp:  extend.Timestamp().NextSlotBoundary(),
		PrivateKey: td.privKeyAux,
	})
	require.NoError(td.t, err)
	tx, err := transaction.FromBytes(txBytesBranch, transaction.MainTxValidationOptions...)
	require.NoError(td.t, err)
	return tx
}

func (td *longConflictTestData) extendToNextSlot(prevSlot [][]*transaction.Transaction, branch *transaction.Transaction) []*transaction.Transaction {
	ret := make([]*transaction.Transaction, len(prevSlot))
	var extendOut *ledger.OutputWithChainID
	var endorse []*ledger.TransactionID

	branchChainID, _, ok := branch.SequencerOutput().ExtractChainID()
	require.True(td.t, ok)

	for i := range prevSlot {
		// FIXME
		extendOut = prevSlot[i][len(prevSlot[i])-1].SequencerOutput().MustAsChainOutput()
		endorse = []*ledger.TransactionID{branch.ID()}
		if extendOut.ChainID == branchChainID {
			extendOut = branch.SequencerOutput().MustAsChainOutput()
			endorse = nil
		}
		txBytes, err := txbuilder.MakeSequencerTransaction(txbuilder.MakeSequencerTransactionParams{
			SeqName:      "seq0",
			ChainInput:   extendOut,
			Timestamp:    branch.Timestamp().AddTicks(ledger.TransactionPaceSequencer()),
			Endorsements: endorse,
			PrivateKey:   td.privKeyAux,
		})
		require.NoError(td.t, err)
		ret[i], err = transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(td.t, err)
	}
	return ret
}

const transferAmount = 100

func (td *longConflictTestData) spendToChain(o *ledger.OutputWithID, chainID ledger.ChainID) *transaction.Transaction {
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(txbuilder.NewTransferData(td.privKey, td.addr, o.Timestamp().AddTicks(ledger.TransactionPace())).
		WithAmount(transferAmount).
		MustWithInputs(o).
		WithTargetLock(ledger.ChainLockFromChainID(chainID)))
	util.AssertNoError(err)
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	util.AssertNoError(err)

	return tx
}

func (td *workflowTestData) logDAGInfo(verbose ...bool) {
	td.t.Logf("MemDAG INFO:\n%s", td.wrk.Info(verbose...))
	slot := td.wrk.LatestBranchSlot()
	td.t.Logf("VERTICES in the latest slot %d\n%s", slot, td.wrk.LinesVerticesInSlotAndAfter(slot).String())
}

func initLongConflictTestData(t *testing.T, nConflicts int, nChains int, howLong int) *longConflictTestData {
	util.Assertf(nChains == 0 || nChains == nConflicts, "nChains == 0 || nChains == nConflicts")
	ret := &longConflictTestData{
		workflowTestData: *initWorkflowTestWithConflicts(t, nConflicts, nChains, false),
		txSequences:      make([][][]byte, nConflicts),
		terminalOutputs:  make([]*ledger.OutputWithID, nConflicts),
	}
	ret.makeChainOrigins(nChains)
	var prev *ledger.OutputWithID
	var err error

	td := &ret.workflowTestData

	for seqNr, originOut := range ret.conflictingOutputs {
		ret.txSequences[seqNr] = make([][]byte, howLong)
		for i := 0; i < howLong; i++ {
			if i == 0 {
				prev = originOut
			}
			ts := originOut.Timestamp().AddTicks(ledger.TransactionPace() * (i + 1))

			trd := txbuilder.NewTransferData(td.privKey, td.addr, ts)
			trd.WithAmount(originOut.Output.Amount())
			trd.MustWithInputs(prev)
			if i < howLong-1 {
				trd.WithTargetLock(td.addr)
			} else {
				if nChains == 0 {
					trd.WithTargetLock(ledger.ChainLockFromChainID(ret.bootstrapChainID))
				} else {
					trd.WithTargetLock(ledger.ChainLockFromChainID(ret.chainOrigins[seqNr%nChains].ChainID))
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
		_, err := td.wrk.TxBytesStore().PersistTxBytesWithMetadata(txBytes, nil)
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

func (td *longConflictTestData) startTraceTx(txs ...*transaction.Transaction) {
	for _, tx := range txs {
		td.env.StartTracingTx(*tx.ID())
	}
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
	_, err := td.txStore.PersistTxBytesWithMetadata(td.chainOriginsTx.Bytes(), nil)
	require.NoError(td.t, err)

	td.storeTxBytes(td.txBytesConflicting...)
	for _, txSeq := range td.txSequences {
		td.storeTxBytes(txSeq...)
	}
}

func (td *longConflictTestData) txBytesAttach() {
	_, err := attacher.AttachTransactionFromBytes(td.chainOriginsTx.Bytes(), td.wrk)
	require.NoError(td.t, err)

	td.attachTxBytes(td.txBytesConflicting...)
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

type spammerParams struct {
	t                 *testing.T
	privateKey        ed25519.PrivateKey
	remainder         *ledger.OutputWithID
	tagAlongSeqID     []ledger.ChainID
	target            ledger.Lock
	batchSize         int
	tagAlongLastOnly  bool
	pace              int
	maxBatches        int
	sendAmount        uint64
	tagAlongFee       uint64
	spammedTxIDs      []ledger.TransactionID
	numSpammedBatches int
	perChainID        map[ledger.ChainID]int
	traceTx           bool
}

func (td *workflowTestData) spamTransfers(par *spammerParams, ctx context.Context) {
	par.numSpammedBatches = 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(par.pace*par.batchSize) * ledger.TickDuration()):
		}
		txBytesSeq := makeTransfers(par)

		for _, txBytes := range txBytesSeq {
			var wg sync.WaitGroup
			wg.Add(1)
			txid, err := td.wrk.TxBytesIn(txBytes,
				workflow.WithSourceType(txmetadata.SourceTypeAPI),
				workflow.WithTxTraceFlag(par.traceTx),
			)
			require.NoError(td.t, err)
			par.spammedTxIDs = append(par.spammedTxIDs, *txid)
		}
		par.numSpammedBatches++
		if par.maxBatches != 0 && par.numSpammedBatches >= par.maxBatches {
			return
		}
	}
}

func makeTransfers(par *spammerParams) [][]byte {
	if par.perChainID == nil {
		par.perChainID = make(map[ledger.ChainID]int)
	}
	require.True(par.t, len(par.tagAlongSeqID) > 0)
	sourceAddr := ledger.AddressED25519FromPrivateKey(par.privateKey)
	var err error
	ret := make([][]byte, par.batchSize)

	for i := 0; i < par.batchSize; i++ {
		ts := ledger.MaxTime(ledger.TimeNow(), par.remainder.Timestamp().AddTicks(par.pace))
		if ts.IsSlotBoundary() {
			ts.AddTicks(1)
		}
		seqID := par.tagAlongSeqID[i%len(par.tagAlongSeqID)]
		par.perChainID[seqID] = par.perChainID[seqID] + 1

		tData := txbuilder.NewTransferData(par.privateKey, sourceAddr, ts).
			MustWithInputs(par.remainder).
			WithTargetLock(par.target).
			WithAmount(par.sendAmount)

		if !par.tagAlongLastOnly || i == par.batchSize-1 {
			tData.WithTagAlong(seqID, par.tagAlongFee)
		}

		ret[i], par.remainder, err = txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
		util.AssertNoError(err)

		tx, err := transaction.FromBytes(ret[i], transaction.MainTxValidationOptions...)
		require.NoError(par.t, err)
		tagAlongOuts := tx.ProducedOutputsWithTargetLock(seqID.AsChainLock())

		if !par.tagAlongLastOnly || i == par.batchSize-1 {
			require.EqualValues(par.t, 1, len(tagAlongOuts))
			lck := tagAlongOuts[0].Output.Lock()
			require.True(par.t, lck.Name() == ledger.ChainLockName)
			lckChain := lck.(ledger.ChainLock)
			require.EqualValues(par.t, lckChain.ChainID(), seqID)
			par.t.Logf("spamTransfers -> %s, tag along: %s", tx.IDShortString(), seqID.StringShort())
		} else {
			par.t.Logf("spamTransfers -> %s", tx.IDShortString())
		}

	}
	return ret
}

type spammerWithdrawCmdParams struct {
	seqID                   ledger.ChainID
	seqControllerPrivateKey ed25519.PrivateKey
	withdrawAmount          uint64
	pace                    int
	target                  ledger.AddressED25519
	remainder               *ledger.OutputWithID
	totalWithdrawn          uint64
}

func (td *workflowTestData) spamWithdrawCommands(par spammerWithdrawCmdParams, ctx context.Context) {
	srcAddr := ledger.AddressED25519FromPrivateKey(par.seqControllerPrivateKey)

	const withdrawAmount = factory.MinimumAmountToRequestFromSequencer

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(par.pace) * ledger.TickDuration()):
		}
		txb := txbuilder.NewTransactionBuilder()
		_, err := txb.ConsumeOutput(par.remainder.Output, par.remainder.ID)
		require.NoError(td.t, err)
		reminder := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.remainder.Output.Amount() - 500).WithLock(par.remainder.Output.Lock())
		})
		_, err = txb.ProduceOutput(reminder)
		require.NoError(td.t, err)
		cmdOut, err := factory.MakeSequencerWithdrawCmdOutput(factory.MakeSequencerWithdrawCmdOutputParams{
			SeqID:          par.seqID,
			ControllerAddr: srcAddr,
			TargetLock:     par.target,
			TagAlongFee:    500,
			Amount:         withdrawAmount,
		})
		_, err = txb.ProduceOutput(cmdOut)
		require.NoError(td.t, err)

		txb.TransactionData.Timestamp = ledger.TimeNow()
		txb.TransactionData.InputCommitment = txb.InputCommitment()
		txb.SignED25519(par.seqControllerPrivateKey)
		txBytes := txb.TransactionData.Bytes()

		tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
		require.NoError(td.t, err)

		par.remainder = tx.MustProducedOutputWithIDAt(1)

		_, err = td.wrk.TxBytesIn(txBytes)
		require.NoError(td.t, err)

		par.totalWithdrawn += withdrawAmount
	}
}

func (td *workflowTestData) startSequencersWithTimeout(maxSlots int, timeout ...time.Duration) {
	var ctx context.Context
	if len(timeout) > 0 {
		ctx, _ = context.WithTimeout(td.env.Ctx(), timeout[0])
	} else {
		ctx = td.env.Ctx()
	}

	td.sequencers = make([]*sequencer.Sequencer, len(td.chainOrigins))
	var err error
	for seqNr := range td.sequencers {
		td.sequencers[seqNr], err = sequencer.New(td.wrk, td.chainOrigins[seqNr].ChainID, td.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", seqNr)),
			sequencer.WithMaxTagAlongInputs(30),
			sequencer.WithPace(5),
			sequencer.WithMaxBranches(maxSlots),
		)
		require.NoError(td.t, err)
		td.sequencers[seqNr].Start()
	}
	go func() {
		<-ctx.Done()
		for _, seq := range td.sequencers {
			seq.Stop()
		}
		td.bootstrapSeq.Stop()
	}()
}
