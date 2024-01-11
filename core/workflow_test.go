package core

import (
	"context"
	"testing"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/unitrie/common"
)

func TestBasic(t *testing.T) {
	stateStore := common.NewInMemoryKVStore()
	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
	workflow.New(stateStore, txBytesStore, context.Background())
}
