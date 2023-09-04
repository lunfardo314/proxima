package inittest

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util/testutil"
)

const (
	InitSupply = 100_000_000_000
)

func GenesisParams(epoch ...core.TimeSlot) (proxima.StateIdentityData, ed25519.PrivateKey) {
	privKey := testutil.GetTestingPrivateKey()
	// creating origin 1 epoch before now. More convenient for the workflow tests
	var e core.TimeSlot
	if len(epoch) > 0 {
		e = epoch[0]
	} else {
		e = core.LogicalTimeNow().TimeSlot()
	}
	retState := proxima.StateIdentityData{
		Description:              "test state",
		InitialSupply:            InitSupply,
		GenesisControllerAddress: core.AddressED25519FromPrivateKey(privKey),
		GenesisEpoch:             e,
	}
	return retState, privKey
}

func GenesisParamsWithPreDistribution(n int, initBalance uint64, epoch ...core.TimeSlot) (proxima.StateIdentityData, ed25519.PrivateKey, []txbuilder.LockBalance, []ed25519.PrivateKey, []core.AddressED25519) {
	sPar, originPrivKey := GenesisParams(epoch...)
	privateKeys := testutil.GetTestingPrivateKeys(n)
	addresses := core.AddressesED25519FromPrivateKeys(privateKeys)
	distrib := make([]txbuilder.LockBalance, len(addresses))
	for i := range addresses {
		distrib[i] = txbuilder.LockBalance{
			Lock:    addresses[i],
			Balance: initBalance,
		}
	}
	return sPar, originPrivKey, distrib, privateKeys, addresses
}
