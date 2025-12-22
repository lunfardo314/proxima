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
	ledger.InitWithTestingLedgerIDData()
}

type workflowDummyEnvironment struct {
	*global.Global
	stateStore   multistate.StateStore
	txBytesStore global.TxBytesStore
}

func (d *workflowDummyEnvironment) StateStore() multistate.StateStore {
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

func (d *workflowDummyEnvironment) GetOwnSequencerID() *base.ChainID {
	panic("not implemented")
}

func (d *workflowDummyEnvironment) EvidenceNumberOfTxDependencies(_ int) {}

func (d *workflowDummyEnvironment) EvidencePastConeSize(_ int) {}

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

	_, err := w.TxBytesIn(nil)
	require.Error(t, err)

	_, err = w.TxBytesIn([]byte("dummy data"))
	require.Error(t, err)

	env.Stop()
	env.WaitAllWorkProcessesStop()
}
