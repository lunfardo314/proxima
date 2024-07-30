package workflow

import (
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
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
	stateStore   global.StateStore
	txBytesStore global.TxBytesStore
}

func (d *workflowDummyEnvironment) StateStore() global.StateStore {
	return d.stateStore
}

func (d *workflowDummyEnvironment) TxBytesStore() global.TxBytesStore {
	return d.txBytesStore
}

func (d *workflowDummyEnvironment) SyncServerDisabled() bool {
	return true
}

func newWorkflowDummyEnvironment() *workflowDummyEnvironment {
	return &workflowDummyEnvironment{
		Global:       global.NewDefault(),
		stateStore:   common.NewInMemoryKVStore(),
		txBytesStore: txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore()),
	}
}

func TestBasic(t *testing.T) {
	env := newWorkflowDummyEnvironment()
	peers := peering.NewPeersDummy()

	w := New(env, peers, OptionDoNotStartPruner)
	w.Start()

	_, err := w.TxBytesIn(nil)
	require.Error(t, err)

	_, err = w.TxBytesIn([]byte("dummy data"))
	require.Error(t, err)

	env.Stop()
	env.MustWaitAllWorkProcessesStop()
}
