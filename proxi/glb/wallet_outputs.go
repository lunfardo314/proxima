package glb

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// proxi never produces a chainLock output. Tokens locked to a chain are lost
// for good once that chain is deleted, and nothing stops the controller from
// deleting it. A transfer to a chain is therefore a tag-along output instead:
// the chain can take it inside the tag-along window, and the wallet that sent
// it reclaims it afterwards (proxi node compact). Outputs whose lock cannot be
// a tag-along, such as chain origins, accept a wallet target only.

// BuildLockOutput composes an output of `amount` base tokens locked to the
// wallet target. A chain target is refused.
func BuildLockOutput(lib *txbuildercore.Library[any], amount uint64, target ledger.Lock) (*txbuildercore.Output, error) {
	sig, ok := target.(ledger.SigLock)
	if !ok {
		return nil, fmt.Errorf("target must be a wallet address (sigLock), got %s: proxi does not produce chainLock outputs", target.Name())
	}
	return txbuildercore.NewSigLockOutput(lib, amount, base.HolderID(sig))
}

// BuildTransferOutput composes the output of a transfer of `amount` base
// tokens from the wallet identified by `sender` to the target: a sigLock
// output for a wallet target, a tag-along output for a chain target.
func BuildTransferOutput(lib *txbuildercore.Library[any], amount uint64, target ledger.Controller, sender base.HolderID) (*txbuildercore.Output, error) {
	switch c := target.(type) {
	case ledger.SigLock:
		return txbuildercore.NewSigLockOutput(lib, amount, base.HolderID(c))
	case ledger.ChainLock:
		var chainID base.ChainID
		copy(chainID[:], c)
		return txbuildercore.NewTagAlongOutput(lib, amount, chainID, sender)
	default:
		return nil, fmt.Errorf("BuildTransferOutput: unsupported target %s", target.Name())
	}
}
