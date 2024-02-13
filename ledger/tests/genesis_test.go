package tests

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOriginBase(t *testing.T) {
	const supply = 10_000_000_000
	addr := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey())
	genesisTimeSlot := ledger.Slot(1337)
	gOut := multistate.InitialSupplyOutput(supply, addr)
	t.Logf("Genesis: suppy = %d, genesis slot = %d:\n", supply, genesisTimeSlot)
	t.Logf("   Genesis outputID: %s", gOut.ID.String())
	t.Logf("   Genesis chain ID: %s", gOut.ChainID.String())
	t.Logf("   Genesis output constraints:\n%s", gOut.Output.ToString("        "))

	sOut := multistate.GenesisStemOutput()
	t.Logf("   Stem outputID: %s", sOut.ID.String())
	t.Logf("   Stem output constraints:\n%s", sOut.Output.ToString("        "))

	privateKey := testutil.GetTestingPrivateKey(100)
	id := ledger.DefaultIdentityData(privateKey)
	pubKey := privateKey.Public().(ed25519.PublicKey)
	require.True(t, pubKey.Equal(id.GenesisControllerPublicKey))
	t.Logf("Identity data:\n%s", id.String())
}

func TestInitOrigin(t *testing.T) {
	privateKey := testutil.GetTestingPrivateKey()
	id := ledger.DefaultIdentityData(privateKey)
	store := common.NewInMemoryKVStore()
	bootstrapSeqID, genesisRoot := multistate.InitStateStore(*id, store)

	rootData := multistate.FetchAllRootRecords(store)
	require.EqualValues(t, 1, len(rootData))

	branchData := multistate.FetchBranchDataByRoot(store, rootData[0])
	require.EqualValues(t, bootstrapSeqID, branchData.SequencerID)
	require.True(t, ledger.CommitmentModel.EqualCommitments(genesisRoot, branchData.Root))

	rdr := multistate.MustNewSugaredReadableState(store, genesisRoot)

	stemBack := rdr.GetStemOutput()
	require.EqualValues(t, ledger.GenesisStemOutputID(), stemBack.ID)

	initSupplyOut, err := rdr.GetChainOutput(&bootstrapSeqID)
	require.NoError(t, err)
	require.EqualValues(t, ledger.GenesisOutputID(), initSupplyOut.ID)

	require.EqualValues(t, id.Bytes(), rdr.MustLedgerIdentityBytes())
}
