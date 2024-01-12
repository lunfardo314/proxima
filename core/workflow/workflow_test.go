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

	_, err := w.TransactionIn(nil)
	require.Error(t, err)

	_, err = w.TransactionIn([]byte("dummy data"))
	require.Error(t, err)

	stop()
	w.WaitStop()
}
