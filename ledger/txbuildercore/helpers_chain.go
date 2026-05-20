package txbuildercore

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/proxima/ledger/base"
)

// ChainConstraintName is the canonical symbol for the 7-arg chain
// constraint at output element index 2 of any chain-style output.
// Mirrors ledger.ChainConstraintName.
const ChainConstraintName = "chain"

// chainConstraintTemplate* are the two on-the-wire source forms.
// They mirror ledger/chain.go's chainConstraintTemplateOrigin /
// chainConstraintTemplateTransition literally — the wallet must
// emit bytes the server's parser accepts, so the source strings
// have to match byte-for-byte.
const (
	chainConstraintTemplateOrigin     = ChainConstraintName + "(0x%s, 0x%s, z32/%d, 0x, 0x, 0x, 0x)"
	chainConstraintTemplateTransition = ChainConstraintName + "(0x%s, 0x%s, z32/%d, z64/%d, z64/%d, z64/%d, z32/%d)"
)

// NewChainOrigin emits the bytecode for a chain-origin constraint:
//
//	chain(0x<NilChainID>, 0x, z32/<startSlot>, 0x, 0x, 0x, 0x)
//
// The empty predecessor reference (`0x`) is the origin sentinel.
// At origin the chain ID is unknown (it's derived from the output ID
// after the tx settles), so NilChainID — all zeros — goes in.
func (l *Library) NewChainOrigin(startSlot uint32) ([]byte, error) {
	src := fmt.Sprintf(chainConstraintTemplateOrigin,
		hex.EncodeToString(base.NilChainID[:]),
		"", // empty predRefHex — origin sentinel
		startSlot,
	)
	return l.CompileExpression(src)
}

// NewChainTransition emits the bytecode for a chain transition
// constraint with all 7 typed args:
//
//	chain(0x<chainID>, 0x<predIdx>, z32/<originSlot>,
//	      z64/<cumChainInflation>, z64/<cumBranchBonus>,
//	      z64/<txCounter>, z32/<branchCounter>)
//
// predIdx is the 1-byte input index of the consumed predecessor
// chain output.
func (l *Library) NewChainTransition(
	chainID base.ChainID,
	predInputIndex byte,
	originSlot uint32,
	cumChainInflation uint64,
	cumBranchBonus uint64,
	transitionCounter uint64,
	branchCounter uint32,
) ([]byte, error) {
	src := fmt.Sprintf(chainConstraintTemplateTransition,
		hex.EncodeToString(chainID[:]),
		hex.EncodeToString([]byte{predInputIndex}),
		originSlot,
		cumChainInflation,
		cumBranchBonus,
		transitionCounter,
		branchCounter,
	)
	return l.CompileExpression(src)
}

// ChainUnlockParams returns the canonical 1-byte unlock-params
// payload for a chain-constraint-locked input: a single byte naming
// the successor's output index. For "discontinue chain" use
// FinishChainUnlockParams.
func ChainUnlockParams(successorOutputIdx byte) []byte {
	return []byte{successorOutputIdx}
}

// FinishChainUnlockParams is the empty-byte unlock-params value that
// terminates a chain (signals "no successor"). Mirrors
// ledger.FinishChainUnlockParams.
var FinishChainUnlockParams = []byte{}

// ChainLockUnlockParams returns the canonical 1-byte unlock-params
// payload for an input locked by a chainLock; the byte is the input
// index of the consumed chain output that authorises the unlock.
// Mirrors ledger.NewChainLockUnlockParams.
func ChainLockUnlockParams(predChainInputIdx byte) []byte {
	return []byte{predChainInputIdx}
}
