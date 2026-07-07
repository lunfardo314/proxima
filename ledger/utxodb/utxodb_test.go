package utxodb

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
	)
}

func TestUTXODB(t *testing.T) {
	initFaucetBalance := ledger.L(0).InitialSupply / 2
	t.Run("origin", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		t.Logf("genesis addr: %s, balance: %s", u.GenesisControllerAddress().String(), util.Th(u.Balance(u.GenesisControllerAddress())))
		t.Logf("faucet addr: %s, balance: %s", u.FaucetAddress().String(), util.Th(u.Balance(u.FaucetAddress())))
		controlledByChain, onChain, err := u.BalanceOnChain(*u.GenesisChainID())
		require.NoError(t, err)

		genesisOutputID := base.GenesisOutputID()
		genesisStemOutputID := base.GenesisStemOutputID()
		t.Logf("bootstrap chainID: %s, on-chain balance: %s, controlled by chain: %s", u.GenesisChainID().String(), util.Th(onChain), util.Th(controlledByChain))
		t.Logf("origin output: %s\n%s", genesisOutputID.String(), u.genesisOutput.ToString("   "))
		t.Logf("origin stem output: %s\n%s", genesisStemOutputID.String(), u.genesisStemOutput.ToString("   "))

		t.Logf("\nUTXODB origin distribution transaction:\n%s", u.OriginDistributionTransactionString())
		require.EqualValues(t, int(initFaucetBalance), int(u.Balance(u.FaucetAddress())))
		// Genesis output has initialSupply-1 tokens (1 token is in the controller mote output)
		// After distribution, on-chain balance is initialSupply-1-initFaucetBalance
		// Controller's wallet balance includes the mote output (1 token)
		// Supply() includes branch inflation from the distribution transaction
		// mine chain dust (index 3 genesis output) is carved out of the bootstrap chain
		require.EqualValues(t, int(u.Supply()-ledger.GenesisMineChainDust-initFaucetBalance), int(u.Balance(u.GenesisControllerAddress())))
		require.EqualValues(t, u.Supply()-1-ledger.GenesisMineChainDust-initFaucetBalance, onChain)
		require.EqualValues(t, 0, controlledByChain)
	})
	t.Run("from faucet", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		addr := ledger.SigLockFromED25519PrivateKey(testutil.GetTestingPrivateKey(100))
		// Use amount above minimum storage deposit
		const testAmount = 100_000_000
		err := u.TokensFromFaucet(addr, testAmount)
		require.NoError(t, err)
		require.EqualValues(t, testAmount, int(u.Balance(addr)))
		require.EqualValues(t, initFaucetBalance-testAmount, u.Balance(u.FaucetAddress()))
	})
	t.Run("from faucet multi", func(t *testing.T) {
		u := NewUTXODB(genesisPrivateKey)
		// Use amount above minimum storage deposit
		const testAmount = 100_000_000
		_, _, addrs := u.GenerateAddressesWithFaucetAmount(100, 10, testAmount)
		require.EqualValues(t, 10, len(addrs))
		for _, a := range addrs {
			require.EqualValues(t, testAmount, u.Balance(a))
		}
	})
}
