package tests

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/attacher"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// testSequencer is the interface that the sequencer satisfies for testing purposes.
type testSequencer interface {
	Start()
	Stop()
	SequencerID() base.ChainID
	OnMilestoneSubmittedVID(func(ms *vertex.WrappedTx))
	OnExitOnce(func())
}

// newTestSequencer creates a sequencer instance.
func newTestSequencer(env *workflow.Workflow, seqID base.ChainID, controllerKey ed25519.PrivateKey, opts ...sequencer.ConfigOption) (testSequencer, error) {
	// disable throttle in tests to prevent budget cuts under test-suite CPU load
	opts = append(opts, sequencer.WithDisableThrottle)
	return sequencer.New(env, seqID, controllerKey, opts...)
}

type workflowDummyEnvironment struct {
	*global.Global
	stateStore   global.Store
	txBytesStore global.TxBytesStore
	root         common.VCommitment
}

func (w *workflowDummyEnvironment) StateStore() global.Store {
	return w.stateStore
}

func (w *workflowDummyEnvironment) TxBytesStore() global.TxBytesStore {
	return w.txBytesStore
}

func (w *workflowDummyEnvironment) PullFromPeers(txid base.TransactionID) int {
	w.Log().Warnf(">>>>>> PullFromPeers not implemented: %s", txid.StringShort())
	return 0
}

func (w *workflowDummyEnvironment) EvidencePastConeSize(_ int) {}

func (w *workflowDummyEnvironment) EvidenceBranchMutations(_, _ int) {}

func (w *workflowDummyEnvironment) EvidenceNumberOfTxDependencies(_ int) {}

// GetOwnSequencerID returns nil in test environment.
// This disables the nonseq_attach sequencer target filter, letting all non-pulled non-seq txs pass.
func (w *workflowDummyEnvironment) GetOwnSequencerID() *base.ChainID {
	return nil
}

func (w *workflowDummyEnvironment) SnapshotBranchID() base.TransactionID {
	return base.GenesisTransactionID()
}

func (w *workflowDummyEnvironment) GetSnapshotBranchID() base.TransactionID {
	return base.GenesisTransactionID()
}

func (w *workflowDummyEnvironment) GetSnapshotFilePath() (string, error) {
	return "", fmt.Errorf("not available in test environment")
}

func (w *workflowDummyEnvironment) DurationSinceLastMessageFromPeer() time.Duration {
	return 0
}

func (w *workflowDummyEnvironment) IsConnectedToNetwork() bool {
	return true
}

func (w *workflowDummyEnvironment) SelfPeerID() peer.ID {
	return "self"
}

func (wrk *workflowDummyEnvironment) GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble {
	return nil
}

func (p *workflowDummyEnvironment) GetLatestReliableBranch() (ret *multistate.BranchData) {
	return nil
}

func (p *workflowDummyEnvironment) GetNodeInfo() *global.NodeInfo {
	return nil
}

func (p *workflowDummyEnvironment) GetPeersInfo() *api.PeersInfo {
	return nil
}

func (p *workflowDummyEnvironment) GetSyncInfo() *api.SyncInfo {
	return nil
}

func (p *workflowDummyEnvironment) LatestReliableState() (multistate.SugaredStateReader, error) {
	return multistate.MakeSugared(multistate.MustNewReadable(p.stateStore, p.root, 0)), nil
}

func (p *workflowDummyEnvironment) CheckTransactionInLRB(_ base.TransactionID, _ int) (lrbid base.TransactionID, foundAtDepth int) {
	panic("not implemented")
}

func (p *workflowDummyEnvironment) QueryTxIDStatusJSONAble(_ *base.TransactionID) vertex.TxIDStatusJSONAble {
	return vertex.TxIDStatusJSONAble{}
}

func (p *workflowDummyEnvironment) SubmitTxBytesFromAPI(_ []byte) {
}

func (p *workflowDummyEnvironment) EvidenceTxValidationStats(_ time.Duration, _, _ int) {
}

func (p *workflowDummyEnvironment) EvidenceBranchInflationBonus(_ uint64) {
}

func (p *workflowDummyEnvironment) CheckTxSenderConfig() (checkSeq, checkNonSeq bool) {
	return false, true
}

func (p *workflowDummyEnvironment) IsVertexReferencedBySequencer(_ *vertex.WrappedTx) bool {
	return false
}

func (p *workflowDummyEnvironment) DiagAllPendingBranches() []map[string]any {
	return nil
}

func (p *workflowDummyEnvironment) DiagCompareReaders(_ base.TransactionID, _ base.OutputID) map[string]any {
	return nil
}

func (p *workflowDummyEnvironment) DiagListBranchesAtSlot(_ uint32) []map[string]any {
	return nil
}

// TxLog methods for api/server environment interface
func (p *workflowDummyEnvironment) TxLogEnable(_ global.TxLogLevel) {}

func (p *workflowDummyEnvironment) TxLogGet(_ []byte, _ ...int) ([]global.TxLogRecord, error) {
	return nil, nil
}

func (p *workflowDummyEnvironment) TxLogIterate(_ time.Time, _ func(rec global.TxLogRecord)) error {
	return nil
}

func (p *workflowDummyEnvironment) TxLogIsEnabled() bool {
	return false
}

func (p *workflowDummyEnvironment) TxLogLevel() global.TxLogLevel {
	return global.TxLogLevelOff
}

func (p *workflowDummyEnvironment) TxLogOnOffAPIEnabled() bool {
	return false
}

func newWorkflowDummyEnvironment(stateStore global.Store, txStore global.TxBytesStore) *workflowDummyEnvironment {
	ret := &workflowDummyEnvironment{
		Global:       global.NewDefault(),
		stateStore:   stateStore,
		txBytesStore: txStore,
	}
	return ret
}

type workflowTestData struct {
	t                      *testing.T
	env                    *workflowDummyEnvironment
	wrk                    *workflow.Workflow
	txStore                global.TxBytesStore
	bootstrapChainID       base.ChainID
	originBranchTxid       base.TransactionID
	distributionBranchTxID base.TransactionID
	distributionBranchTx   *transaction.Transaction
	privKey                ed25519.PrivateKey
	addr                   ledger.SigLock
	privKeyAux             ed25519.PrivateKey
	addrAux                ledger.SigLock
	privKeyFaucet          ed25519.PrivateKey
	addrFaucet             ledger.SigLock
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
	bootstrapSeq           testSequencer
	sequencers             []testSequencer
}

type longConflictTestData struct {
	workflowTestData
	txSequences     [][][]byte
	txs             [][]*transaction.Transaction
	terminalOutputs []*ledger.OutputWithID
}

const (
	initBalance = 10_000_000_000_000
	tagAlongFee = 500
)

func initWorkflowTest(t *testing.T, nChains int, startPruner ...bool) *workflowTestData {
	util.Assertf(nChains > 0, "nChains > 0")
	t.Logf("genesis state id: %s", ledger.L(0).String())

	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistribution(initBalance, uint64(nChains*initBalance+tagAlongFee), initBalance)
	ret := &workflowTestData{
		t:             t,
		privKey:       privKeys[0],
		addr:          addrs[0],
		privKeyAux:    privKeys[1],
		addrAux:       addrs[1],
		privKeyFaucet: privKeys[2],
		addrFaucet:    addrs[2],
	}
	t.Logf("genesis addr: %s", ledger.SigLockFromED25519PrivateKey(genesisPrivateKey).String())
	t.Logf("priv key addr: %s", ret.addr.String())
	t.Logf("aux key addr: %s", ret.addrAux.String())
	t.Logf("faucet addr: %s", ret.addrFaucet.String())

	require.True(t, ledger.SigLockMatchesED25519PrivateKey(ret.addr, ret.privKey))

	stateStore := common.NewInMemoryKVStore()
	ret.txStore = txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

	var genesisRoot common.VCommitment
	ret.bootstrapChainID, genesisRoot = multistate.InitStateStoreFromGlobals(stateStore)
	txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, genesisPrivateKey, distrib)
	require.NoError(t, err)
	_, err = ret.txStore.PersistTxBytesWithMetadata(txBytes, nil)
	require.NoError(t, err)

	ret.distributionBranchTx, err = transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	ret.distributionBranchTxID = ret.distributionBranchTx.ID()
	t.Logf("distribution txID: %s", ret.distributionBranchTxID.StringShort())

	ret.faucetOutput = ret.distributionBranchTx.MustProducedOutputWithIDAt(2)
	const printDistributionTx = false
	if printDistributionTx {
		//tx, err := transaction.Parse(txBytes, transaction.MainTxValidationOptions...)
		//require.NoError(t, err)
		genesisState := multistate.MustNewReadable(stateStore, genesisRoot)
		err = ret.distributionBranchTx.SetFullContextWithFetch(genesisState.GetUTXO)
		require.NoError(t, err)
		t.Logf("--------------- distribution tx:\n%s\n--------------", ret.distributionBranchTx.String())
		t.Logf("--------------- faucet output\n%s\n--------------", ret.faucetOutput)
	}

	ret.env = newWorkflowDummyEnvironment(stateStore, ret.txStore)
	if len(startPruner) > 0 && startPruner[0] {
		ret.wrk = workflow.Start(ret.env, peering.NewPeersDummy(), workflow.OptionMaxConcurrentAttachers(200))
	} else {
		ret.wrk = workflow.Start(ret.env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC, workflow.OptionMaxConcurrentAttachers(200))
	}

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	t.Logf("origin branch txid: %s", ret.originBranchTxid.StringShort())

	for i := range distrib {
		t.Logf("distributed %s -> %s", util.Th(distrib[i].Balance), distrib[i].Lock.String())
	}
	return ret
}

// initWorkflowTestWithAuxBalance is like initWorkflowTest but with an explicit auxiliary address balance.
// Used when chain origins need non-uniform amounts that don't fit the standard nChains*initBalance formula.
func initWorkflowTestWithAuxBalance(t *testing.T, auxBalance uint64, startPruner ...bool) *workflowTestData {
	t.Logf("genesis state id: %s", ledger.L(0).String())

	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistribution(initBalance, auxBalance, initBalance)
	ret := &workflowTestData{
		t:             t,
		privKey:       privKeys[0],
		addr:          addrs[0],
		privKeyAux:    privKeys[1],
		addrAux:       addrs[1],
		privKeyFaucet: privKeys[2],
		addrFaucet:    addrs[2],
	}
	t.Logf("genesis addr: %s", ledger.SigLockFromED25519PrivateKey(genesisPrivateKey).String())
	t.Logf("aux key addr: %s (balance: %s)", ret.addrAux.String(), util.Th(auxBalance))

	require.True(t, ledger.SigLockMatchesED25519PrivateKey(ret.addr, ret.privKey))

	stateStore := common.NewInMemoryKVStore()
	ret.txStore = txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())

	var genesisRoot common.VCommitment
	ret.bootstrapChainID, genesisRoot = multistate.InitStateStoreFromGlobals(stateStore)
	txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, genesisPrivateKey, distrib)
	require.NoError(t, err)
	_, err = ret.txStore.PersistTxBytesWithMetadata(txBytes, nil)
	require.NoError(t, err)

	ret.distributionBranchTx, err = transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	ret.distributionBranchTxID = ret.distributionBranchTx.ID()
	t.Logf("distribution txID: %s", ret.distributionBranchTxID.StringShort())

	ret.faucetOutput = ret.distributionBranchTx.MustProducedOutputWithIDAt(2)

	ret.env = newWorkflowDummyEnvironment(stateStore, ret.txStore)
	_ = genesisRoot
	if len(startPruner) > 0 && startPruner[0] {
		ret.wrk = workflow.Start(ret.env, peering.NewPeersDummy(), workflow.OptionMaxConcurrentAttachers(200))
	} else {
		ret.wrk = workflow.Start(ret.env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC, workflow.OptionMaxConcurrentAttachers(200))
	}

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	for i := range distrib {
		t.Logf("distributed %s -> %s", util.Th(distrib[i].Balance), distrib[i].Lock.String())
	}
	return ret
}

// makes chain origins transaction from aux output
func (td *workflowTestData) makeChainOrigins(n int) {
	if n == 0 {
		return
	}
	rdr := td.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsForController(td.addrAux.ControllerID())
	require.NoError(td.t, err)
	require.EqualValues(td.t, 1, len(oDatas))

	td.auxOutput, err = oDatas[0].Parse()
	require.NoError(td.t, err)
	td.t.Logf("auxiliary output id: %s", td.auxOutput.IDShort())

	txb := txbuilder.New()
	_, _, err = txb.ConsumeOutputsUnlock(td.auxOutput)
	require.NoError(td.t, err)

	ts := td.auxOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	amount := (td.auxOutput.Output.TokenBalance() - tagAlongFee) / uint64(n)
	for i := 0; i < n; i++ {
		o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount))
			o.WithLock(td.addrAux)
			o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot).Bytes())
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(td.t, err)
	}
	tagAlongOut := ledger.NewTagAlongOutput(tagAlongFee, td.bootstrapChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(td.privKeyAux)))
	_, err = txb.ProduceOutput(tagAlongOut)
	require.NoError(td.t, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(td.privKeyAux)

	txBytes := txb.TransactionData.Bytes()
	td.chainOriginsTx, err = transaction.ParseWithPartialValidation(txBytes)
	require.NoError(td.t, err)

	const printChainOriginsTx = false
	if printChainOriginsTx {
		td.t.Logf("chain origins transaction:\n%s", td.chainOriginsTx.String())
	}

	td.chainOrigins = make([]*ledger.OutputWithChainID, n)
	td.chainOriginsTx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		if int(idx) >= n {
			return true
		}
		otmp := ledger.OutputWithID{
			ID:     oid,
			Output: o,
		}
		ochain, err := otmp.AsChainOutput()
		require.NoError(td.t, err)
		td.chainOrigins[idx] = ochain
		td.t.Logf("chain origin %s : %s, lock: %s", oid.StringShort(), td.chainOrigins[idx].ChainID.String(), td.chainOrigins[idx].Output.Lock().String())
		return true
	})
}

// makeChainOriginsWithAmounts creates chain origins from aux output with specified amounts per chain.
// The aux output balance must equal sum(amounts) + tagAlongFee.
func (td *workflowTestData) makeChainOriginsWithAmounts(amounts []uint64) {
	n := len(amounts)
	if n == 0 {
		return
	}
	rdr := td.wrk.HeaviestStateForLatestTimeSlot()
	oDatas, err := rdr.GetUTXOsForController(td.addrAux.ControllerID())
	require.NoError(td.t, err)
	require.EqualValues(td.t, 1, len(oDatas))

	td.auxOutput, err = oDatas[0].Parse()
	require.NoError(td.t, err)
	td.t.Logf("auxiliary output id: %s, balance: %s", td.auxOutput.IDShort(), util.Th(td.auxOutput.Output.TokenBalance()))

	txb := txbuilder.New()
	_, _, err = txb.ConsumeOutputsUnlock(td.auxOutput)
	require.NoError(td.t, err)

	ts := td.auxOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	for i := 0; i < n; i++ {
		o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amounts[i]))
			o.WithLock(td.addrAux)
			o.MustPushConstraint(ledger.NewChainOrigin(ts.Slot).Bytes())
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(td.t, err)
	}
	tagAlongOut := ledger.NewTagAlongOutput(tagAlongFee, td.bootstrapChainID, base.HolderID(ledger.SigLockFromED25519PrivateKey(td.privKeyAux)))
	_, err = txb.ProduceOutput(tagAlongOut)
	require.NoError(td.t, err)

	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.TransactionData.Timestamp = ts
	txb.SignED25519(td.privKeyAux)

	txBytes := txb.TransactionData.Bytes()
	td.chainOriginsTx, err = transaction.ParseWithPartialValidation(txBytes)
	require.NoError(td.t, err)

	td.chainOrigins = make([]*ledger.OutputWithChainID, n)
	td.chainOriginsTx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		if int(idx) >= n {
			return true
		}
		otmp := ledger.OutputWithID{
			ID:     oid,
			Output: o,
		}
		ochain, err := otmp.AsChainOutput()
		require.NoError(td.t, err)
		td.chainOrigins[idx] = ochain
		td.t.Logf("chain origin %s : %s, amount: %s, lock: %s",
			oid.StringShort(), td.chainOrigins[idx].ChainID.String(),
			util.Th(amounts[idx]), td.chainOrigins[idx].Output.Lock().String())
		return true
	})
}

func initWorkflowTestWithConflicts(t *testing.T, nConflicts int, nChains int, targetLockChain bool) *workflowTestData {
	ret := initWorkflowTest(t, nChains)

	ret.pkController = make([]ed25519.PrivateKey, nConflicts)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	err := ret.wrk.EnsureLatestBranches()
	require.NoError(t, err)
	t.Logf("%s", ret.wrk.Info())

	rdr := ret.wrk.HeaviestStateForLatestTimeSlot()
	bal, _ := multistate.BalanceOnLock(rdr, ret.addr)
	require.EqualValues(t, initBalance, int(bal))

	oDatas, err := rdr.GetUTXOsForController(ret.addr.ControllerID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.forkOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.TokenBalance()))
	t.Logf("forked output:\n%s", ret.forkOutput.LinesSource("      ").String())

	oDatas, err = rdr.GetUTXOsForController(ret.addrAux.ControllerID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.auxOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.TokenBalance()))
	t.Logf("auxiliary output id: %s", ret.forkOutput.IDShort())

	ret.txBytesConflicting = make([][]byte, nConflicts)

	ts := ret.forkOutput.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	td := txbuilder.NewTransferData(ret.privKey, ret.addr, ts).
		MustWithInputs(ret.forkOutput)

	require.True(t, base.ValidTime(td.Timestamp))

	for i := 0; i < nConflicts; i++ {
		td.WithAmount(uint64(10_000_000_000 + i))
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
		tx, err := transaction.ParseWithPartialValidation(ret.txBytesConflicting[i])
		require.NoError(t, err)
		t.Logf("conflicting tx ts: %s", tx.Timestamp().String())
		ret.conflictingOutputs[i] = tx.MustProducedOutputWithIDAt(1)
		require.EqualValues(t, 10_000_000_000+i, int(ret.conflictingOutputs[i].Output.TokenBalance()))
	}
	return ret
}

func (td *workflowTestData) stop() {
	td.env.Stop()
}

func (td *workflowTestData) waitStop(timeout ...time.Duration) {
	td.env.WaitAllWorkProcessesStop(timeout...)
}

func (td *workflowTestData) stopAndWait(timeout ...time.Duration) bool {
	td.env.Stop()
	return td.env.WaitAllWorkProcessesStop(timeout...)
}

func (td *longConflictTestData) makeSeqBeginnings(withConflictingFees bool) {
	util.Assertf(len(td.chainOrigins) == len(td.conflictingOutputs), "td.chainOrigins)==len(td.conflictingOutputs)")
	td.seqChain = make([][]*transaction.Transaction, len(td.chainOrigins))
	var additionalIn []*ledger.OutputWithID
	for i, chainOrigin := range td.chainOrigins {
		var ts base.LedgerTime
		if withConflictingFees {
			additionalIn = []*ledger.OutputWithID{td.terminalOutputs[i]}
			ts = base.MaximumTime(chainOrigin.Timestamp(), td.terminalOutputs[i].Timestamp())
		} else {
			additionalIn = nil
			ts = chainOrigin.Timestamp()
		}
		ts = ts.AddTicks(int(ledger.L(0).TransactionPaceSequencer))

		td.seqChain[i] = make([]*transaction.Transaction, 0)
		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:          "1",
			ChainInput:       chainOrigin,
			Timestamp:        ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts),
			Endorsements:     []base.TransactionID{td.distributionBranchTxID},
			SignatureType:    base.SignatureTypeED25519,
			PrivateKey:       td.privKeyAux,
			PublicKey:        td.privKeyAux.Public().(ed25519.PublicKey),
			AdditionalInputs: additionalIn,
		})
		require.NoError(td.t, err)
		tx, err := transaction.ParseWithPartialValidation(txBytes)
		require.NoError(td.t, err)
		td.seqChain[i] = append(td.seqChain[i], tx)
	}
}

func (td *longConflictTestData) makeSeqChains(howLong int) {
	for i := 0; i < howLong; i++ {
		for seqNr := range td.seqChain {
			endorsedSeqNr := (seqNr + 1) % len(td.seqChain)
			endorse := td.seqChain[endorsedSeqNr][i].ID()
			txBytesSeq, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
				SeqName:       fmt.Sprintf("seq%d", seqNr),
				ChainInput:    td.seqChain[seqNr][i].SequencerOutput().MustAsChainOutput(),
				Timestamp:     td.seqChain[seqNr][i].Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer)),
				Endorsements:  util.List(endorse),
				SignatureType: base.SignatureTypeED25519,
				PrivateKey:    td.privKeyAux,
				PublicKey:     td.privKeyAux.Public().(ed25519.PublicKey),
			})
			require.NoError(td.t, err)
			tx, err := transaction.ParseWithPartialValidation(txBytesSeq)
			require.NoError(td.t, err)
			td.seqChain[seqNr] = append(td.seqChain[seqNr], tx)
		}
	}
}

func (td *longConflictTestData) makeSlotTransactions(howLongChain int, extendBegin []*transaction.Transaction) [][]*transaction.Transaction {
	ret := make([][]*transaction.Transaction, len(extendBegin))
	var extend *ledger.OutputWithChainID
	var endorse base.TransactionID
	var ts base.LedgerTime

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
			ts = base.MaximumTime(endorse.Timestamp(), extend.Timestamp()).AddTicks(int(ledger.L(0).TransactionPaceSequencer))

			txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
				SeqName:       fmt.Sprintf("seq%d", i),
				ChainInput:    extend,
				Timestamp:     ts,
				Endorsements:  util.List(endorse),
				SignatureType: base.SignatureTypeED25519,
				PrivateKey:    td.privKeyAux,
				PublicKey:     td.privKeyAux.Public().(ed25519.PublicKey),
			})
			require.NoError(td.t, err)
			tx, err := transaction.ParseWithPartialValidation(txBytes)
			require.NoError(td.t, err)
			ret[seqNr] = append(ret[seqNr], tx)
			td.t.Logf("%3d  %s", seqNr, tx.IDShortString())
		}
	}

	return ret
}

func (td *longConflictTestData) makeSlotTransactionsWithTagAlong(howLongChain int, extendBegin []*transaction.Transaction, _ ...bool) [][]*transaction.Transaction {
	ret := make([][]*transaction.Transaction, len(extendBegin))
	var extend *ledger.OutputWithChainID
	var endorse base.TransactionID
	var ts base.LedgerTime

	if td.remainderOutput == nil {
		td.transferChain = make([]*transaction.Transaction, 0)
		td.remainderOutput = td.conflictingOutputs[0]
	}
	var txSpend *transaction.Transaction

	for i := 0; i < howLongChain; i++ {
		for seqNr := range ret {
			txSpend = td.spendToChain(td.remainderOutput, td.chainOrigins[seqNr].ChainID)
			util.Assertf(txSpend.NumProducedOutputs() == 2, "txSpend.NumProducedOutputs() == 2")
			td.transferChain = append(td.transferChain, txSpend)

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
			ts = base.MaximumTime(endorse.Timestamp(), extend.Timestamp(), transferOut.Timestamp()).AddTicks(int(ledger.L(0).TransactionPaceSequencer))

			txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
				SeqName:          fmt.Sprintf("seq%d", i),
				ChainInput:       extend,
				AdditionalInputs: []*ledger.OutputWithID{transferOut},
				Timestamp:        ts,
				Endorsements:     util.List(endorse),
				SignatureType:    base.SignatureTypeED25519,
				PrivateKey:       td.privKeyAux,
				PublicKey:        td.privKeyAux.Public().(ed25519.PublicKey),
			})
			require.NoError(td.t, err)
			tx, err := transaction.ParseWithPartialValidation(txBytes)
			require.NoError(td.t, err)
			ret[seqNr] = append(ret[seqNr], tx)
		}
	}

	return ret
}

func (td *longConflictTestData) makeBranch(extend *ledger.OutputWithChainID, prevBranch *transaction.Transaction) *transaction.Transaction {
	td.t.Logf("extendTS: %s, prevBranchTS: %s", extend.Timestamp().String(), prevBranch.Timestamp().String())
	require.True(td.t, extend.Timestamp().After(prevBranch.Timestamp()))

	txBytesBranch, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
		SeqName:       "seq0",
		ChainInput:    extend,
		StemInput:     prevBranch.StemOutput(),
		Timestamp:     extend.Timestamp().NextSlotBoundary(),
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    td.privKeyAux,
		PublicKey:     td.privKeyAux.Public().(ed25519.PublicKey),
	})
	require.NoError(td.t, err)
	tx, err := transaction.ParseWithPartialValidation(txBytesBranch)
	require.NoError(td.t, err)
	return tx
}

func (td *longConflictTestData) extendToNextSlot(prevSlot [][]*transaction.Transaction, branch *transaction.Transaction) []*transaction.Transaction {
	ret := make([]*transaction.Transaction, len(prevSlot))
	var extendOut *ledger.OutputWithChainID
	var endorse []base.TransactionID

	branchChainID, ok := branch.SequencerOutput().ExtractChainID()
	require.True(td.t, ok)

	for i := range prevSlot {
		// FIXME
		extendOut = prevSlot[i][len(prevSlot[i])-1].SequencerOutput().MustAsChainOutput()
		endorse = []base.TransactionID{branch.ID()}
		if extendOut.ChainID == branchChainID {
			extendOut = branch.SequencerOutput().MustAsChainOutput()
			endorse = nil
		}
		ts := branch.Timestamp().AddTicks(int(ledger.L(0).TransactionPaceSequencer))
		ts = ledger.L(0).EnsurePostBranchConsolidationConstraintTimestamp(ts)

		txBytes, err := txbuilder_seq.MakeSimpleSequencerTransaction(txbuilder_seq.MakeSimpleSequencerTransactionParams{
			SeqName:       "seq0",
			ChainInput:    extendOut,
			Timestamp:     ts,
			Endorsements:  endorse,
			SignatureType: base.SignatureTypeED25519,
			PrivateKey:    td.privKeyAux,
			PublicKey:     td.privKeyAux.Public().(ed25519.PublicKey),
		})
		require.NoError(td.t, err)
		ret[i], err = transaction.ParseWithPartialValidation(txBytes)
		require.NoError(td.t, err)
	}
	return ret
}

const transferAmount = 50_000_000

func (td *longConflictTestData) spendToChain(o *ledger.OutputWithID, chainID base.ChainID) *transaction.Transaction {
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(txbuilder.NewTransferData(td.privKey, td.addr, o.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))).
		WithAmount(transferAmount).
		MustWithInputs(o).
		WithTargetLock(ledger.ChainLockFromChainID(chainID)))
	util.AssertNoError(err)
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	util.AssertNoError(err)

	return tx
}

func (td *workflowTestData) logDAGInfo(verbose ...bool) {
	td.t.Logf("MemDAG INFO:\n%s", td.wrk.Info(verbose...))
	slot, _, _ := td.wrk.LatestBranchSlots()
	td.t.Logf("VERTICES in the latest slot %d\n%s", slot, td.wrk.LinesVerticesInSlotAndAfter(slot).String())
}

func initLongConflictTestData(t *testing.T, nConflicts int, nChains int, howLong int, chainTipToGenesisPrivKey ...bool) *longConflictTestData {
	util.Assertf(nChains == 0 || nChains == nConflicts, "nChains == 0 || nChains == nConflicts")
	ret := &longConflictTestData{
		workflowTestData: *initWorkflowTestWithConflicts(t, nConflicts, nChains, false),
		txSequences:      make([][][]byte, nConflicts),
		txs:              make([][]*transaction.Transaction, nConflicts),
		terminalOutputs:  make([]*ledger.OutputWithID, nConflicts),
	}
	ret.makeChainOrigins(nChains)
	var prev *ledger.OutputWithID
	var err error

	td := &ret.workflowTestData

	for seqNr, originOut := range ret.conflictingOutputs {
		ret.txSequences[seqNr] = make([][]byte, howLong)
		ret.txs[seqNr] = make([]*transaction.Transaction, howLong)
		for i := 0; i < howLong; i++ {
			if i == 0 {
				prev = originOut
			}
			ts := originOut.Timestamp().AddTicks(int(ledger.L(0).TransactionPace) * (i + 1))

			trd := txbuilder.NewTransferData(td.privKey, td.addr, ts)
			trd.WithAmount(originOut.Output.TokenBalance())
			trd.MustWithInputs(prev)
			if i < howLong-1 {
				trd.WithTargetLock(td.addr)
			} else {
				if nChains == 0 {
					trd.WithTargetLock(ledger.ChainLockFromChainID(ret.bootstrapChainID))
				} else {
					if i == howLong-1 && len(chainTipToGenesisPrivKey) > 0 && chainTipToGenesisPrivKey[0] {
						trd.WithTargetLock(ledger.SigLockFromED25519PrivateKey(genesisPrivateKey))
					} else {
						trd.WithTargetLock(ledger.ChainLockFromChainID(ret.chainOrigins[seqNr%nChains].ChainID))
					}
				}
			}
			ret.txSequences[seqNr][i], err = txbuilder.MakeSimpleTransferTransaction(trd)
			require.NoError(t, err)

			tx, err := transaction.ParseWithPartialValidation(ret.txSequences[seqNr][i])
			require.NoError(t, err)
			ret.txs[seqNr][i] = tx
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
		td.t.Logf("%2d: conflicting chain start:\n%s\n%s", i, o.ID.StringShort(), o.Output.Lines("  ").String())
		for j := range td.txs[i] {
			td.t.Logf("      %2d :%s", j, td.txs[i][j].IDShortString())
		}
	}
	td.t.Logf("-------------- Sequencer chains-----------")
	for i, seqChain := range td.seqChain {
		td.t.Logf("seq chain #%d, len = %d", i, len(seqChain))
		for _, tx := range seqChain {
			o := tx.MustProducedOutputAt(0)
			td.t.Logf("       %s\n%s", tx.IDShortString(), o.Lines("  ").String())
		}
	}
}

type spammerParams struct {
	t                 *testing.T
	privateKey        ed25519.PrivateKey
	remainder         *ledger.OutputWithID
	tagAlongSeqID     []base.ChainID
	target            ledger.Lock
	batchSize         int
	pace              int
	maxBatches        int
	sendAmount        uint64
	tagAlongFee       uint64
	spammedTxIDs      []base.TransactionID
	numSpammedBatches int
	perChainID        map[base.ChainID]int
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
			txid, err := td.wrk.TxBytesInForTests(txBytes)
			require.NoError(td.t, err)
			par.spammedTxIDs = append(par.spammedTxIDs, txid)
		}
		par.numSpammedBatches++
		if par.maxBatches != 0 && par.numSpammedBatches >= par.maxBatches {
			return
		}
	}
}

func makeTransfers(par *spammerParams) [][]byte {
	if par.perChainID == nil {
		par.perChainID = make(map[base.ChainID]int)
	}
	require.True(par.t, len(par.tagAlongSeqID) > 0)
	sourceAddr := ledger.SigLockFromED25519PrivateKey(par.privateKey)
	var err error
	ret := make([][]byte, par.batchSize)

	for i := 0; i < par.batchSize; i++ {
		ts := base.MaximumTime(ledger.TimeNow(), par.remainder.Timestamp().AddTicks(par.pace))
		if ts.IsSlotBoundary() {
			ts.AddTicks(1)
		}
		seqID := par.tagAlongSeqID[i%len(par.tagAlongSeqID)]
		par.perChainID[seqID] = par.perChainID[seqID] + 1

		tData := txbuilder.NewTransferData(par.privateKey, sourceAddr, ts).
			MustWithInputs(par.remainder).
			WithTargetLock(par.target).
			WithAmount(par.sendAmount)

		if i == par.batchSize-1 {
			tData.WithTagAlong(seqID, par.tagAlongFee)
		}

		ret[i], par.remainder, err = txbuilder.MakeSimpleTransferTransactionWithRemainder(tData)
		util.AssertNoError(err)

		tx, err := transaction.ParseWithPartialValidation(ret[i])
		require.NoError(par.t, err)
		tagAlongOuts := tx.ProducedTagAlongOutputs()

		if i == par.batchSize-1 {
			require.EqualValues(par.t, 1, len(tagAlongOuts))
			require.EqualValues(par.t, tagAlongOuts[0].TargetSequencerID, seqID)
			par.t.Logf("spamTransfers -> %s, tag along: %s", tx.IDShortString(), seqID.StringShort())
		} else {
			par.t.Logf("spamTransfers -> %s", tx.IDShortString())
		}

	}
	return ret
}

type spammerWithdrawCmdParams struct {
	seqID                   base.ChainID
	seqControllerPrivateKey ed25519.PrivateKey
	withdrawAmount          uint64
	pace                    int
	target                  ledger.SigLock
	remainder               *ledger.OutputWithID
	totalWithdrawn          uint64
}

func (td *workflowTestData) startSequencersWithTimeout(maxSlots int, timeout ...time.Duration) {
	var ctx context.Context
	if len(timeout) > 0 {
		ctx, _ = context.WithTimeout(td.env.Ctx(), timeout[0])
	} else {
		ctx = td.env.Ctx()
	}

	td.sequencers = make([]testSequencer, len(td.chainOrigins))
	var err error
	for seqNr := range td.sequencers {
		td.sequencers[seqNr], err = newTestSequencer(td.wrk, td.chainOrigins[seqNr].ChainID, td.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", seqNr)),
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

func StartTestEnv() (*workflowDummyEnvironment, *base.TransactionID, error) {
	privKey := genesisPrivateKey
	addr1 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(1))
	addr2 := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(2))
	// Use amounts above minimum storage deposit
	distrib := []ledger.LockBalance{
		{Lock: addr1, Balance: 100_000_000, ChainOrigin: false},
		{Lock: addr2, Balance: 100_000_000, ChainOrigin: false},
		{Lock: addr2, Balance: 100_000_000, ChainOrigin: true},
	}

	stateStore := common.NewInMemoryKVStore()
	_, root := multistate.InitStateStoreFromGlobals(stateStore)
	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	env := newWorkflowDummyEnvironment(stateStore, txBytesStore)
	env.root = root

	workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDisableMemDAGGC, workflow.OptionMaxConcurrentAttachers(200))

	txBytes, err := txbuilder_seq.DistributeInitialSupply(stateStore, privKey, distrib)
	if err != nil {
		return nil, nil, err
	}
	txid, err := txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
	if err != nil {
		return nil, nil, err
	}

	return env, &txid, err
}
