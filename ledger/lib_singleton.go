package ledger

import (
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/util"
)

var (
	libraryGlobal      *Library
	libraryGlobalMutex sync.RWMutex
)

func L() *Library {
	libraryGlobalMutex.RLock()
	defer libraryGlobalMutex.RUnlock()

	util.Assertf(libraryGlobal != nil, "ledger constraint library not initialized")
	return libraryGlobal
}

func MustInitSingleton(identityData []byte) {
	libraryGlobalMutex.Lock()

	util.Assertf(libraryGlobal == nil, "ledger is already initialized")

	lib, idParams, err := ParseLedgerIdYAML(identityData, GetEmbeddedFunctionResolver)
	util.AssertNoError(err)

	libraryGlobal = newLibrary(lib, idParams, identityData)
	libraryGlobal.registerConstraints()

	libraryGlobalMutex.Unlock()

	libraryGlobal.runInlineTests()
}

// InitWithTestingLedgerIDData for testing
func InitWithTestingLedgerIDData(opts ...func(data *IdentityParameters)) ed25519.PrivateKey {
	id, pk := GetTestingIdentityData(31415926535)
	for _, opt := range opts {
		opt(id)
	}
	lib := LibraryFromIdentityParameters(id)
	MustInitSingleton(lib.ToYAML(true))
	return pk
}

func WithTickDuration(d time.Duration) func(id *IdentityParameters) {
	return func(id *IdentityParameters) {
		id.SetTickDuration(d)
	}
}

func WithTransactionPace(ticks byte) func(id *IdentityParameters) {
	return func(id *IdentityParameters) {
		id.TransactionPace = ticks
	}
}

func WithSequencerPace(ticks byte) func(id *IdentityParameters) {
	return func(id *IdentityParameters) {
		id.TransactionPaceSequencer = ticks
	}
}

func TransactionPace() int {
	return int(L().ID.TransactionPace)
}

func TransactionPaceSequencer() int {
	return int(L().ID.TransactionPaceSequencer)
}
