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

	// PullSyncPortionThresholdSlots specifies threshold of diff between current slot and latest slot in DB
	// when start pulling sync portions
	PullSyncPortionThresholdSlots = 10
)

func init() {
	util.Assertf(MaxSyncPortionInSlots <= math.MaxUint16, "MaxSyncPortionInSlots <= math.MaxUint16")
}
