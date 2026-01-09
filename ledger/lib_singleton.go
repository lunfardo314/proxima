package ledger

import (
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/util"
)

var (
	libraryGlobal      *Library
	libraryGlobalMutex sync.RWMutex
	// ledgerReset is set to true when ResetForTesting is called.
	// Background goroutines can check this to avoid accessing nil Const.
	ledgerReset atomic.Bool
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

	lib, err := ParseLibraryFromYAML(identityData, GetEmbeddedFunctionResolverUpgrade0)
	util.AssertNoError(err)

	libraryGlobal = newLibrary(lib, identityData)
	libraryGlobal.registerConstraints()

	libraryGlobalMutex.Unlock()

	initConstantsSingleton(libraryGlobal.Library)

	ledgerReset.Store(false)

	libraryGlobal.runInlineTests()

}

// ResetForTesting clears the ledger singleton to allow re-initialization.
// This is only for testing purposes to get fresh genesis timestamps per test.
// DO NOT use in production code.
func ResetForTesting() {
	ledgerReset.Store(true)
	libraryGlobalMutex.Lock()
	defer libraryGlobalMutex.Unlock()
	libraryGlobal = nil
	Const = nil
}

// IsReset returns true if the ledger has been reset via ResetForTesting.
// Background goroutines can check this to avoid accessing nil Const during shutdown.
func IsReset() bool {
	return ledgerReset.Load()
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
