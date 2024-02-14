package workflow

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func init() {
	ledger.InitWithTestingLedgerIDData()
}

type workflowDummyEnvironment struct {
	global.Logging
	global.StateStore
	global.TxBytesStore
}

func newWorkflowDummyEnvironment() *workflowDummyEnvironment {
	return &workflowDummyEnvironment{
		Logging:      global.NewDefaultLogging("wrk", zapcore.DebugLevel, nil),
		StateStore:   common.NewInMemoryKVStore(),
		TxBytesStore: txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore()),
	}
}

func TestBasic(t *testing.T) {
	env := newWorkflowDummyEnvironment()
	peers := peering.NewPeersDummy()

	w := New(env, peers, OptionDoNotStartPruner)
	ctx, stop := context.WithCancel(context.Background())
	w.Start(ctx)

	_, err := w.TxBytesIn(nil)
	require.Error(t, err)

	_, err = w.TxBytesIn([]byte("dummy data"))
	require.Error(t, err)

	stop()
	w.WaitStop()
}
