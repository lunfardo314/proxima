package tests

// Safeguard test: a chain cannot be duplicated within one transaction. A second successor with
// the same, otherwise correct chain constraint (same chain ID, same predecessor index) is refused,
// because the predecessor references only one successor (chain.easyfl, _validChainProduced).
// Relied on by: one mine successor per tx (CheckIssuanceBound), a delegation not frozen twice,
// CheckFrozenCoverageBound (keyed by predecessor index). This is the only check; removing it
// lets the duplication through. The balance is split so only the chain rule can refuse.

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

func TestChainCannotBeDuplicated(t *testing.T) {
	e := newChainTestEnv(t, 1_000_000_000)
	chainOut := e.createChainOrigin(t, 200_000_000)
	chainIn := e.getChainOutput(t, chainOut.ChainID)

	_, txb := e.buildChainTransition(t, chainIn, chainOut,
		func(txb *exhelp.Builder, predIdx byte, succIdx *byte) {
			first := txb.ProducedOutputs[*succIdx]
			half := first.TokenBalance() / 2
			// first successor with half the amount
			txb.ReplaceProducedOutput(*succIdx, first.Clone(func(o *ledger.OutputBuilder) {
				o.PutAmounts(int64(first.TokenBalance() - half))
			}))
			// second successor: same chain constraint, the other half
			_, err := txb.ProduceOutput(first.Clone(func(o *ledger.OutputBuilder) {
				o.PutAmounts(int64(half))
			}))
			require.NoError(t, err)
		},
	)
	_, _, _, err := txbtest.BuildAndValidate(txb)
	require.Error(t, err, "two successors of one chain in one transaction must be refused")
	require.NoError(t, util.MustErrorWith(err, "predecessor reference crosscheck failed"), "%v", err)
}
