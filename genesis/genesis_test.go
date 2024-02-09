package genesis

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
	gOut := InitialSupplyOutput(supply, addr, genesisTimeSlot)
	t.Logf("Genesis: suppy = %d, genesis slot = %d:\n", supply, genesisTimeSlot)
	t.Logf("   Genesis outputID: %s", gOut.ID.String())
	t.Logf("   Genesis chain ID: %s", gOut.ChainID.String())
	t.Logf("   Genesis output constraints:\n%s", gOut.Output.ToString("        "))

	sOut := StemOutput(genesisTimeSlot)
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
	bootstrapSeqID, genesisRoot := InitLedgerState(*id, store)

	rootData := multistate.FetchAllRootRecords(store)
	require.EqualValues(t, 1, len(rootData))

	branchData := multistate.FetchBranchDataByRoot(store, rootData[0])
	require.EqualValues(t, bootstrapSeqID, branchData.SequencerID)
	require.True(t, ledger.CommitmentModel.EqualCommitments(genesisRoot, branchData.Root))

	rdr := multistate.MustNewSugaredReadableState(store, genesisRoot)

	stemBack := rdr.GetStemOutput()
	require.EqualValues(t, ledger.StemOutputID(id.GenesisSlot), stemBack.ID)

	initSupplyOut, err := rdr.GetChainOutput(&bootstrapSeqID)
	require.NoError(t, err)
	require.EqualValues(t, ledger.InitialSupplyOutputID(id.GenesisSlot), initSupplyOut.ID)

	require.EqualValues(t, id.Bytes(), rdr.MustLedgerIdentityBytes())
}

func TestYAML(t *testing.T) {
	privateKey := testutil.GetTestingPrivateKey()
	id := ledger.DefaultIdentityData(privateKey)
	yamlableStr := id.YAMLAble().YAML()
	t.Logf("\n" + string(yamlableStr))

	idBack, err := ledger.StateIdentityDataFromYAML(yamlableStr)
	require.NoError(t, err)
	require.EqualValues(t, id.Bytes(), idBack.Bytes())
}
