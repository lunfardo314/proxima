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
	global.StateStore
	global.TxBytesStore
}

func newWorkflowDummyEnvironment() *workflowDummyEnvironment {
	return &workflowDummyEnvironment{
		Global:       global.New(),
		StateStore:   common.NewInMemoryKVStore(),
		TxBytesStore: txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore()),
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
	env.MustWaitStop()
}
