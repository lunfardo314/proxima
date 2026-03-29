package workflow

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func init() {
	ledger.InitWithTestingLedgerData()
}

type workflowDummyEnvironment struct {
	*global.Global
	stateStore   global.Store
	txBytesStore global.TxBytesStore
}

func (d *workflowDummyEnvironment) StateStore() global.Store {
	return d.stateStore
}

func (d *workflowDummyEnvironment) TxBytesStore() global.TxBytesStore {
	return d.txBytesStore
}

func (d *workflowDummyEnvironment) SyncServerDisabled() bool {
	return true
}

func (d *workflowDummyEnvironment) PullFromPeers(_ base.TransactionID) int {
	panic("not implemented")
}

// GetOwnSequencerID returns nil in test environment.
// This disables the nonseq_attach sequencer target filter, letting all non-pulled non-seq txs pass.
func (d *workflowDummyEnvironment) GetOwnSequencerID() *base.ChainID {
	return nil
}

func (d *workflowDummyEnvironment) EvidenceNumberOfTxDependencies(_ int) {}

func (d *workflowDummyEnvironment) EvidencePastConeSize(_ int) {}

func (d *workflowDummyEnvironment) EvidenceBranchMutations(_, _ int) {}

func (d *workflowDummyEnvironment) SnapshotBranchID() base.TransactionID {
	return base.GenesisTransactionID()
}

func (d *workflowDummyEnvironment) DurationSinceLastMessageFromPeer() time.Duration {
	return 0
}

func (d *workflowDummyEnvironment) SelfPeerID() peer.ID {
	return "self"
}

func (d *workflowDummyEnvironment) EvidenceTxValidationStats(took time.Duration, numIn, numOut int) {
	panic("implement me")
}

func (d *workflowDummyEnvironment) LatestReliableState() (multistate.SugaredStateReader, error) {
	panic("implement me")
}

func (d *workflowDummyEnvironment) EvidenceBranchInflationBonus(ib uint64) {
	panic("implement me")
}

func (d *workflowDummyEnvironment) GetLatestReliableBranch() (ret *multistate.BranchData) {
	panic("implement me")
}

func (d *workflowDummyEnvironment) CheckTxSenderConfig() (checkSeq, checkNonSeq bool) {
	return true, false
}

func newWorkflowDummyEnvironment() *workflowDummyEnvironment {
	stateStore := common.NewInMemoryKVStore()
	multistate.InitStateStoreFromGlobals(stateStore)
	return &workflowDummyEnvironment{
		Global:       global.NewDefault(),
		stateStore:   stateStore,
		txBytesStore: txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore()),
	}
}

func TestBasic(t *testing.T) {
	env := newWorkflowDummyEnvironment()
	peers := peering.NewPeersDummy()

	w := Start(env, peers, OptionDisableMemDAGGC)

	_, err := w.TxBytesInForTests(nil)
	require.Error(t, err)

	_, err = w.TxBytesInForTests([]byte("dummy data"))
	require.Error(t, err)

	env.Stop()
	env.WaitAllWorkProcessesStop()
}
