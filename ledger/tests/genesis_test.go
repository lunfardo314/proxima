package tests

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOriginBase(t *testing.T) {
	gtxid := base.GenesisTransactionID()
	fmt.Printf("hex: %s\n", gtxid.StringHex())
	fmt.Printf("full: %s\n", gtxid.String())
	rndtxid := base.RandomTransactionID(false, 1, base.T(1337, 50))
	fmt.Printf("raw hexadecimal (non-sequencer): %s\n", rndtxid.StringHex())
	fmt.Printf("full human readable (non-sequencer): %s\n", rndtxid.String())
	fmt.Printf("short (trimmed) human readable (non-sequencer): %s\n", rndtxid.StringShort())

	rndtxid = base.RandomTransactionID(true, 1, base.T(1337, 0))
	fmt.Printf("raw hexadecimal branch transaction ChainID %s\n", rndtxid.StringHex())
	fmt.Printf("full human readable branch transaction ChainID: %s\n", rndtxid.String())
	fmt.Printf("short (trimmed) human readable branch transaction ChainID: %s\n", rndtxid.StringShort())
	rndtxid = base.RandomTransactionID(true, 1, base.T(1337, 50))
	fmt.Printf("short (trimmed) human readable non-branch sequencer transaction ChainID: %s\n", rndtxid.StringShort())

	const supply = 10_000_000_000
	addr := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey())
	genesisTimeSlot := 1337
	gOut := ledger.GenesisOutput(supply, addr)
	t.Logf("Genesis: suppy = %d, genesis slot = %d:\n", supply, genesisTimeSlot)
	t.Logf("   Genesis outputID: %s", gOut.ID.String())
	t.Logf("   Genesis chain id: %s", gOut.ChainID.String())
	t.Logf("   Genesis output constraints:\n%s", gOut.Output.ToString("        "))

	sOut := ledger.GenesisStemOutput()
	t.Logf("   Stem outputID: %s", sOut.ID.String())
	t.Logf("   Stem output constraints:\n%s", sOut.Output.ToString("        "))

	privateKey := testutil.GetTestingPrivateKey(100)
	id := ledger.DefaultParameters(privateKey, uint32(time.Now().Unix()))
	pubKey := privateKey.Public().(ed25519.PublicKey)
	require.True(t, pubKey.Equal(id.GenesisControllerPublicKey))
}

func TestInitOrigin(t *testing.T) {
	store := common.NewInMemoryKVStore()
	bootstrapSeqID, genesisRoot := multistate.InitStateStoreFromGlobals(store)

	rootData := multistate.FetchAllRootRecords(store)
	require.EqualValues(t, 1, len(rootData))

	branchData := multistate.FetchBranchDataByRoot(store, rootData[0])
	require.EqualValues(t, bootstrapSeqID, branchData.SequencerID)
	require.True(t, ledger.CommitmentModel.EqualCommitments(genesisRoot, branchData.Root))

	snapshotBranchID := multistate.FetchSnapshotBranchID(store)
	require.EqualValues(t, base.GenesisTransactionID(), snapshotBranchID)

	rdr := multistate.MustNewSugaredReadableState(store, genesisRoot)

	stemBack := rdr.GetStemOutput()
	require.EqualValues(t, base.GenesisStemOutputID(), stemBack.ID)

	initSupplyOut, err := rdr.GetChainOutputWithID(bootstrapSeqID)
	require.NoError(t, err)
	require.EqualValues(t, base.GenesisOutputID(), initSupplyOut.ID)

	require.EqualValues(t, 0, multistate.FetchLatestCommittedSlot(store))
	require.EqualValues(t, 0, multistate.FetchEarliestSlot(store))
}

func TestBoostrapSequencerID(t *testing.T) {
	t.Logf("bootstrap sequencer id: %s", base.BoostrapSequencerID.String())
	t.Logf("bootstrap sequencer id hex: %s", ledger.BoostrapSequencerIDHex)
}
