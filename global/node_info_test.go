package global

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

func init() {
	ledger.InitWithTestingLedgerIDData()
	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration    : %v
     transaction pace : %d ticks
     sequencer pace   : %d ticks
`,
		ledger.TickDuration(), ledger.TransactionPace(), ledger.TransactionPaceSequencer(),
	)
}

func randomPeerID() peer.ID {
	privateKey := testutil.GetTestingPrivateKey(101)

	pklpp, err := crypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)

	ret, err := peer.IDFromPrivateKey(pklpp)
	util.AssertNoError(err)
	return ret
}

func TestPeerInfo(t *testing.T) {
	t.Run("1", func(t *testing.T) {
		pi := &NodeInfo{
			Name:            "peerName",
			ID:              randomPeerID(),
			NumStaticAlive:  5,
			NumDynamicAlive: 3,
		}
		jsonData, err := json.MarshalIndent(pi, "", "  ")
		require.NoError(t, err)
		t.Logf("json string:\n%s", string(jsonData))

		var piBack NodeInfo
		err = json.Unmarshal(jsonData, &piBack)
		require.NoError(t, err)
		require.EqualValues(t, pi.Name, piBack.Name)
		require.EqualValues(t, pi.ID, piBack.ID)
		require.EqualValues(t, pi.NumStaticAlive, piBack.NumStaticAlive)
		require.EqualValues(t, pi.NumDynamicAlive, piBack.NumDynamicAlive)

		require.True(t, util.EqualSlices(pi.Sequencers, piBack.Sequencers))
		require.True(t, util.EqualSlices(pi.Branches, piBack.Branches))
	})
	t.Run("2", func(t *testing.T) {
		branches := []ledger.TransactionID{
			ledger.RandomTransactionID(true),
			ledger.RandomTransactionID(true),
			ledger.RandomTransactionID(true),
		}
		sequencers := []ledger.ChainID{ledger.RandomChainID()}
		pi := &NodeInfo{
			Name:            "peerName",
			ID:              randomPeerID(),
			NumStaticAlive:  5,
			NumDynamicAlive: 3,
			Sequencers:      sequencers,
			Branches:        branches,
		}
		jsonData, err := json.MarshalIndent(pi, "", "  ")
		require.NoError(t, err)
		t.Logf("json string:\n%s", string(jsonData))

		var piBack NodeInfo
		err = json.Unmarshal(jsonData, &piBack)
		require.NoError(t, err)
		require.EqualValues(t, pi.Name, piBack.Name)
		require.EqualValues(t, pi.ID, piBack.ID)
		require.EqualValues(t, pi.NumStaticAlive, piBack.NumStaticAlive)
		require.EqualValues(t, pi.NumDynamicAlive, piBack.NumDynamicAlive)

		require.True(t, util.EqualSlices(pi.Sequencers, piBack.Sequencers))
		require.True(t, util.EqualSlices(pi.Branches, piBack.Branches))
	})
}
