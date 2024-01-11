package core

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
)

func TestBasic(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	peers := peering.NewPeersDummy()
	workflow.New(stateStore, txBytesStore, peers, context.Background())
}
