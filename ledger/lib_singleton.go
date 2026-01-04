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

	lib, err := ParseLibraryFromYAML(identityData, GetEmbeddedFunctionResolver)
	util.AssertNoError(err)

	libraryGlobal = newLibrary(lib, identityData)
	libraryGlobal.registerConstraints()

	libraryGlobalMutex.Unlock()

	initConstantsSingleton(libraryGlobal.Library)

	libraryGlobal.runInlineTests()

}

// ResetForTesting clears the ledger singleton to allow re-initialization.
// This is only for testing purposes to get fresh genesis timestamps per test.
// DO NOT use in production code.
func ResetForTesting() {
	libraryGlobalMutex.Lock()
	defer libraryGlobalMutex.Unlock()
	libraryGlobal = nil
	Const = nil
}

// InitWithTestingLedgerIDData for testing

type ParametersOption func(par *InitParameters)

func InitWithTestingLedgerIDData(opts ...ParametersOption) ed25519.PrivateKey {
	params, pk := GetTestingLedgerParams(31415926535)
	for _, opt := range opts {
		opt(&params)
	}
	lib := LibraryFromParameters(params)
	MustInitSingleton(lib.ToYAML(true))
	return pk
}

func WithTickDuration(duration time.Duration) ParametersOption {
	return func(par *InitParameters) {
		par.TickDuration = duration
	}
}

func WithTransactionPace(transactionPace int) ParametersOption {
	return func(par *InitParameters) {
		par.TransactionPaceTicks = transactionPace
	}
}

func WithTransactionPaceSequencer(transactionPace int) ParametersOption {
	return func(par *InitParameters) {
		par.TransactionPaceSequencerTicks = transactionPace
	}
}
