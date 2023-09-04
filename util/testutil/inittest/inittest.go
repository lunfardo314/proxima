package inittest

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util/testutil"
)

const (
	InitSupply = 100_000_000_000
)

func GenesisParamsWithPreDistribution(n int, initBalance uint64, slot ...core.TimeSlot) ([]txbuilder.LockBalance, []ed25519.PrivateKey, []core.AddressED25519) {
	privateKeys := testutil.GetTestingPrivateKeys(n)
	addresses := core.AddressesED25519FromPrivateKeys(privateKeys)
	distrib := make([]txbuilder.LockBalance, len(addresses))
	for i := range addresses {
		distrib[i] = txbuilder.LockBalance{
			Lock:    addresses[i],
			Balance: initBalance,
		}
	}
	return distrib, privateKeys, addresses
}
