package genesis

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOriginBase(t *testing.T) {
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

func TestInitOrigin(t *testing.T) {
	privateKey := testutil.GetTestingPrivateKey()
	id := DefaultIdentityData(privateKey)
	store := common.NewInMemoryKVStore()
	bootstrapSeqID, genesisRoot := InitLedgerState(*id, store)

	branches := multistate.FetchBranchData(store)
	require.EqualValues(t, 1, len(branches))
	require.EqualValues(t, bootstrapSeqID, branches[0].SequencerID)
	require.True(t, core.CommitmentModel.EqualCommitments(genesisRoot, branches[0].Root))

	rdr := multistate.MustNewSugaredReadableState(store, genesisRoot)

	stemBack := rdr.GetStemOutput()
	require.EqualValues(t, StemOutputID(id.GenesisTimeSlot), stemBack.ID)

	initSupplyOut, err := rdr.GetChainOutput(&bootstrapSeqID)
	require.NoError(t, err)
	require.EqualValues(t, InitialSupplyOutputID(id.GenesisTimeSlot), initSupplyOut.ID)

	require.EqualValues(t, id.Bytes(), rdr.StateIdentityBytes())
}
