package genesis

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestOrigin(t *testing.T) {
	const supply = 10_000_000_000
	addr := core.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey())
	genesisTimeSlot := core.TimeSlot(1337)
	gOut := InitialSupplyOutput(supply, addr, genesisTimeSlot)
	t.Logf("Genesis: suppy = %d, genesis slot = %d:\n", supply, genesisTimeSlot)
	t.Logf("   Genesis outputID: %s", gOut.ID.String())
	t.Logf("   Genesis chain ID: %s", gOut.ChainID.String())
	t.Logf("   Genesis output constraints:\n%s", gOut.Output.ToString("        "))

	sOut := StemOutput(supply, genesisTimeSlot)
	t.Logf("   Stem outputID: %s", sOut.ID.String())
	t.Logf("   Stem output constraints:\n%s", sOut.Output.ToString("        "))

	privateKey := testutil.GetTestingPrivateKey(100)
	id := DefaultIdentityData(privateKey)
	pubKey := privateKey.Public().(ed25519.PublicKey)
	require.True(t, pubKey.Equal(id.GenesisControllerPublicKey))
	t.Logf("Identity data:\n%s", id.String())
}
