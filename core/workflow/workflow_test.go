package workflow

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func TestBasic(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	peers := peering.NewPeersDummy()
	w := New(stateStore, txBytesStore, peers, WithLogLevel(zapcore.DebugLevel))
	ctx, stop := context.WithCancel(context.Background())
	w.Start(ctx)

	err := w.TxBytesIn(nil)
	require.Error(t, err)

	err = w.TxBytesIn([]byte("dummy data"))
	require.Error(t, err)

	stop()
	w.WaitStop()
}
