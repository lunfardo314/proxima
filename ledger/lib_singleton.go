package ledger

import (
	"crypto/ed25519"
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

var (
	libraryGlobal      *Library
	libraryGlobalMutex sync.RWMutex
)

func L() *Library {
	libraryGlobalMutex.RLock()
	defer libraryGlobalMutex.RUnlock()

	common.Assert(libraryGlobal != nil, "ledger constraint library not initialized")
	return libraryGlobal
}

func Init(id *IdentityData) {
	libraryGlobalMutex.Lock()
	defer libraryGlobalMutex.Unlock()

	util.Assertf(libraryGlobal == nil, "global library already initialized")

	libraryGlobal = newLibrary()
	fmt.Printf("------ Base EasyFL library:\n")
	libraryGlobal.PrintLibraryStats()
	defer func() {
		fmt.Printf("------ Extended EasyFL library:\n")
		libraryGlobal.PrintLibraryStats()
	}()

	libraryGlobal.initNoTxConstraints(id)
	libraryGlobal.extendWithConstraints()
}

// InitWithTestingLedgerIDData for testing
func InitWithTestingLedgerIDData(seed ...int) ed25519.PrivateKey {
	id, pk := GetTestingIdentityData(seed...)
	Init(id)
	return pk
}

func GenesisSlot() Slot {
	return L().Const().GenesisSlot()
}

func TicksPerSlot() byte {
	return L().Const().TicksPerSlot()
}
