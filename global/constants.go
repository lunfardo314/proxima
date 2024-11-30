package global

import (
	"math"

	"github.com/lunfardo314/proxima/util"
)

const (
	MultiStateDBName     = "proximadb"
	TxStoreDBName        = "proximadb.txstore"
	ConfigKeyTxStoreType = "txstore.type"

	// MaxSyncPortionSlots max number of slots in the sync portion
	MaxSyncPortionSlots = 100
)

func init() {
	util.Assertf(MaxSyncPortionSlots <= math.MaxUint16, "MaxSyncPortionSlots <= math.MaxUint16")
}
