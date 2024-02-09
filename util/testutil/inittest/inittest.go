package inittest

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
)

const (
	InitSupply = genesis.DefaultInitialSupply
)

func GenesisParamsWithPreDistributionOld(n int, initBalance uint64) ([]ledger.LockBalance, []ed25519.PrivateKey, []ledger.AddressED25519) {
	privateKeys := testutil.GetTestingPrivateKeys(n)
	addresses := ledger.AddressesED25519FromPrivateKeys(privateKeys)
	distrib := make([]ledger.LockBalance, len(addresses))
	for i := range addresses {
		distrib[i] = ledger.LockBalance{
			Lock:    addresses[i],
			Balance: initBalance,
		}
	}
	return distrib, privateKeys, addresses
}

const minimumAmountToDistribute = 1_000_000

func GenesisParamsWithPreDistribution(initBalance ...uint64) ([]ledger.LockBalance, []ed25519.PrivateKey, []ledger.AddressED25519) {
	util.Assertf(len(initBalance) > 0, "len(initBalance)>0")
	privateKeys := testutil.GetTestingPrivateKeys(len(initBalance))
	addresses := ledger.AddressesED25519FromPrivateKeys(privateKeys)
	distrib := make([]ledger.LockBalance, len(addresses))
	for i := range addresses {
		util.Assertf(initBalance[i] >= minimumAmountToDistribute, "GenesisParamsWithPreDistribution: amount must be >= %d", minimumAmountToDistribute)
		distrib[i] = ledger.LockBalance{
			Lock:    addresses[i],
			Balance: initBalance[i],
		}
	}
	return distrib, privateKeys, addresses
}
