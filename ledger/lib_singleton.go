package ledger

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

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
	func() {
		libraryGlobalMutex.Lock()
		defer libraryGlobalMutex.Unlock()

		util.Assertf(libraryGlobal == nil, "global library already initialized")

		libraryGlobal = newLibrary()

		fmt.Printf("------ Base EasyFL library:\n")
		libraryGlobal.PrintLibraryStats()

		libraryGlobal.initNoTxConstraints(id)
		libraryGlobal.extendWithConstraints()

		fmt.Printf("------ Extended EasyFL library:\n")
		libraryGlobal.PrintLibraryStats()
	}()

	libraryGlobal.runInlineTests()
}

// InitWithTestingLedgerIDData for testing
func InitWithTestingLedgerIDData(opts ...func(data *IdentityData)) ed25519.PrivateKey {
	id, pk := GetTestingIdentityData(31415926535)
	for _, opt := range opts {
		opt(id)
	}
	Init(id)
	return pk
}

func WithTickDuration(d time.Duration) func(id *IdentityData) {
	return func(id *IdentityData) {
		id.SetTickDuration(d)
	}
}

func WithTransactionPace(ticks byte) func(id *IdentityData) {
	return func(id *IdentityData) {
		id.TransactionPace = ticks
	}
}

func WithSequencerPace(ticks byte) func(id *IdentityData) {
	return func(id *IdentityData) {
		id.TransactionPaceSequencer = ticks
	}
}

func TicksPerSlot() byte {
	return L().Const().TicksPerSlot()
}

func TransactionPace() int {
	return int(L().ID.TransactionPace)
}

func TransactionPaceSequencer() int {
	return int(L().ID.TransactionPaceSequencer)
}
