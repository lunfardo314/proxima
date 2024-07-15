package global

import (
	"math"

	"github.com/lunfardo314/proxima/util"
)

const (
	MultiStateDBName     = "proximadb"
	TxStoreDBName        = "proximadb.txstore"
	ConfigKeyTxStoreType = "txstore.type"

	// MaxSyncPortionInSlots max number of slots in the sync portion
	MaxSyncPortionInSlots = 100
)

func init() {
	util.Assertf(MaxSyncPortionInSlots <= math.MaxUint16, "MaxSyncPortionInSlots <= math.MaxUint16")
}
