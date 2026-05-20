package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// BuildLockOutput composes an output of `amount` PRXI locked to the
// given controller (sigLock or chainLock). Returns an error if the
// controller is of an unsupported lock type. Shared across proxi
// sites that need to produce a basic locked output.
func BuildLockOutput(lib *txbuildercore.Library, amount uint64, lock ledger.Lock) (*txbuildercore.Output, error) {
	switch c := lock.(type) {
	case ledger.SigLock:
		return txbuildercore.NewSigLockOutput(lib, amount, base.HolderID(c))
	case ledger.ChainLock:
		var chainID base.ChainID
		copy(chainID[:], c)
		return txbuildercore.NewChainLockOutput(lib, amount, chainID)
	default:
		return nil, fmt.Errorf("BuildLockOutput: unsupported lock type %s", lock.Name())
	}
}
