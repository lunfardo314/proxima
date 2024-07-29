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

	// DefaultSyncToleranceThresholdSlots specifies threshold of diff between current slot and latest slot in DB
	// when start pulling sync portions
	DefaultSyncToleranceThresholdSlots = 50
)

func init() {
	util.Assertf(MaxSyncPortionSlots <= math.MaxUint16, "MaxSyncPortionSlots <= math.MaxUint16")
}
