package glb

import (
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
)

func InclusionScore(inclusion map[ledger.ChainID]tippool.TxInclusion) (totalBranches int, percOfTotal int, percOfDominating int) {
}
