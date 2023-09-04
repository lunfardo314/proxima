package state

import (
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util/testutil"
)

func TestOrigin(t *testing.T) {
	const supply = 10_000_000_000
	addr := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey())
	genesisEpoch := core.TimeSlot(1337)
	gOut := GenesisOutput(supply, addr, genesisEpoch)
	t.Logf("Genesis: suppy = %d, genesis epoch = %d:\n", supply, genesisEpoch)
	t.Logf("   Genesis outputID: %s", gOut.ID.String())
	t.Logf("   Genesis chain ID: %s", gOut.ChainID.String())
	t.Logf("   Genesis output constraints:\n%s", gOut.Output.ToString("        "))

	sOut := GenesisStemOutput(supply, genesisEpoch)
	t.Logf("   Stem outputID: %s", sOut.ID.String())
	t.Logf("   Stem output constraints:\n%s", sOut.Output.ToString("        "))
}
