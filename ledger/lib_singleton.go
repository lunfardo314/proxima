package ledger

import (
	"crypto/ed25519"
	"fmt"
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

func InitLocally(id *IdentityData, verbose ...bool) *Library {
	ret := newBaseLibrary()
	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Base EasyFL library:\n")
		ret.PrintLibraryStats()
	}

	ret.upgrade0(id)

	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Extended EasyFL library:\n")
		ret.PrintLibraryStats()
	}
	return ret
}

func Init(id *IdentityData, verbose ...bool) {
	libraryGlobalMutex.Lock()

	util.Assertf(libraryGlobal == nil, "ledger is already initialized")
	libraryGlobal = InitLocally(id, verbose...)

	libraryGlobalMutex.Unlock()

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

func TransactionPace() int {
	return int(L().ID.TransactionPace)
}

func TransactionPaceSequencer() int {
	return int(L().ID.TransactionPaceSequencer)
}
