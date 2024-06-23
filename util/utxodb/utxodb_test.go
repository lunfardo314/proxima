package utxodb

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestUTXODB(t *testing.T) {
	initFaucetBalance := ledger.L().ID.InitialSupply / 2
	t.Run("origin", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		t.Logf("genesis addr: %s, balance: %s", u.GenesisControllerAddress().String(), util.Th(u.Balance(u.GenesisControllerAddress())))
		t.Logf("faucet addr: %s, balance: %s", u.FaucetAddress().String(), util.Th(u.Balance(u.FaucetAddress())))
		controlledByChain, onChain, err := u.BalanceOnChain(*u.GenesisChainID())
		require.NoError(t, err)

		genesisOutputID := ledger.GenesisOutputID()
		genesisStemOutputID := ledger.GenesisStemOutputID()
		t.Logf("bootstrap chainID: %s, on-chain balance: %s, controlled by chain: %s", u.GenesisChainID().String(), util.Th(onChain), util.Th(controlledByChain))
		t.Logf("origin output: %s\n%s", genesisOutputID.String(), u.genesisOutput.ToString("   "))
		t.Logf("origin stem output: %s\n%s", genesisStemOutputID.String(), u.genesisStemOutput.ToString("   "))

		t.Logf("\nUTXODB origin distribution transaction:\n%s", u.OriginDistributionTransactionString())
		require.EqualValues(t, int(initFaucetBalance), int(u.Balance(u.FaucetAddress())))
		require.EqualValues(t, int(ledger.L().ID.InitialSupply-initFaucetBalance), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, ledger.L().ID.InitialSupply-initFaucetBalance, onChain)
		require.EqualValues(t, 0, controlledByChain)
		t.Logf("State identity:\n%s", u.StateIdentityData().String())
	})
	t.Run("from faucet", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		addr := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(100))
		err := u.TokensFromFaucet(addr, 1337)
		require.NoError(t, err)
		require.EqualValues(t, 1337, int(u.Balance(addr)))
		require.EqualValues(t, initFaucetBalance-1337, u.Balance(u.FaucetAddress()))
	})
	t.Run("from faucet multi", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		_, _, addrs := u.GenerateAddressesWithFaucetAmount(100, 10, 1337)
		require.EqualValues(t, 10, len(addrs))
		for _, a := range addrs {
			require.EqualValues(t, 1337, u.Balance(a))
		}
	})
}
