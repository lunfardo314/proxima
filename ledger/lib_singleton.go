package ledger

import (
	"crypto/ed25519"
	"encoding/binary"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// LibraryCache holds cached library versions keyed by their upgrade slot.
// Libraries are lazily loaded from the DB partition on first access.
// Since upgrades are rare, no cache eviction is needed - node restart resets the cache.
type LibraryCache struct {
	mu    sync.RWMutex
	store common.Traversable  // DB store for loading library JSON blobs
	cache map[uint32]*Library // upgrade slot -> parsed library

	// Fast-path: cache latest library directly (most common case)
	latestLib         *Library
	latestUpgradeSlot uint32

	// Slot index loaded once from DB to avoid repeated traversal
	upgradeSlots []uint32          // sorted ascending
	slotToJSON   map[uint32][]byte // for lazy parsing
}

// ResolverFactory creates an embedded function resolver for a library.
// Each upgrade version has its own resolver factory.
type ResolverFactory func(lib *easyfl.Library[*EvalContext]) func(string) easyfl.EmbeddedFunction[*EvalContext]

var (
	libraryCache      *LibraryCache
	libraryCacheMutex sync.RWMutex
	// ledgerReset is set to true when ResetForTesting is called.
	// Background goroutines can check this to avoid accessing nil library cache.
	ledgerReset atomic.Bool

	// nextPendingUpgradeSlot tracks the next upgrade slot that might need UTXO injection.
	// This is an optimization to avoid scanning all upgrades on every branch commit.
	// Value meanings:
	// - 0: not initialized (will scan all upgrades)
	// - MaxSlot: no pending upgrades (all upgrade UTXOs are in state)
	// - other: the next upgrade slot that might need injection
	nextPendingUpgradeSlot atomic.Uint32
)

// L returns the library version applicable to the given slot.
// For the latest library version, use L(base.MaxSlot).
// Libraries are lazily loaded from DB and cached on first access.
// During test teardown (after ResetForTesting), L panics via runtime.Goexit
// so background goroutines terminate cleanly without crashing the test process.
func L(slot uint32) *Library {
	libraryCacheMutex.RLock()
	defer libraryCacheMutex.RUnlock()

	if libraryCache == nil || libraryCache.store == nil {
		if ledgerReset.Load() {
			// test teardown: terminate the calling goroutine silently
			runtime.Goexit()
		}
		util.Assertf(false, "ledger library cache not initialized")
	}

	return libraryCache.getOrLoad(slot)
}

// getOrLoad retrieves a library from cache or loads it from DB.
// Caller must hold at least a read lock on libraryCacheMutex.
func (lc *LibraryCache) getOrLoad(slot uint32) *Library {
	lc.mu.RLock()

	// Fast path: most common case is requesting current/latest library
	if lc.latestLib != nil && slot >= lc.latestUpgradeSlot {
		lib := lc.latestLib
		lc.mu.RUnlock()
		return lib
	}

	// Check if already cached
	upgradeSlot, prevUpgradeSlot := lc.findUpgradeSlotForSlot(slot)
	if lib, ok := lc.cache[upgradeSlot]; ok {
		lc.mu.RUnlock()
		return lib
	}
	lc.mu.RUnlock()

	// Not in cache, need to parse
	lc.mu.Lock()
	defer lc.mu.Unlock()

	// Double-check after acquiring write lock
	if lib, ok := lc.cache[upgradeSlot]; ok {
		return lib
	}

	// Parse the library
	jsonData := lc.slotToJSON[upgradeSlot]
	lib := lc.parseLibrary(jsonData)

	// Set the upgrade chain data
	chainData := &UpgradeChainData{
		UpgradeSlot:     upgradeSlot,
		LibraryHash:     lib.Library.LibraryHash(),
		PrevUpgradeSlot: prevUpgradeSlot,
	}

	if upgradeSlot == 0 {
		chainData.PrevLibraryHash = BaseLibraryHash()
	} else {
		// Get previous library's hash (release lock to avoid deadlock)
		lc.mu.Unlock()
		prevLib := lc.getOrLoad(prevUpgradeSlot)
		lc.mu.Lock()
		chainData.PrevLibraryHash = prevLib.Library.LibraryHash()
	}

	lib.SetUpgradeChainData(chainData)

	// Set upgrade index: ordinal position of this upgrade slot in the sorted list (0-based)
	for i, s := range lc.upgradeSlots {
		if s == upgradeSlot {
			lib.upgradeIndex = uint16(i)
			break
		}
	}

	lc.cache[upgradeSlot] = lib

	// Update latest library cache if this is the highest slot
	if upgradeSlot >= lc.latestUpgradeSlot {
		lc.latestUpgradeSlot = upgradeSlot
		lc.latestLib = lib
	}

	return lib
}

// loadUpgradeSlots loads all upgrade slots from DB once.
// Must be called with write lock held.
func (lc *LibraryCache) loadUpgradeSlots() {
	if lc.slotToJSON != nil {
		return // already loaded
	}

	lc.slotToJSON = make(map[uint32][]byte)
	lc.upgradeSlots = nil

	prefix := []byte{upgradeLibraryDBPartition}
	lc.store.Iterator(prefix).Iterate(func(k, v []byte) bool {
		if len(k) != 5 {
			return true
		}
		slot, err := base.SlotFromBytes(k[1:])
		util.AssertNoError(err)
		lc.upgradeSlots = append(lc.upgradeSlots, slot)
		lc.slotToJSON[slot] = v
		return true
	})

	sort.Slice(lc.upgradeSlots, func(i, j int) bool {
		return lc.upgradeSlots[i] < lc.upgradeSlots[j]
	})

	if len(lc.upgradeSlots) > 0 {
		lc.latestUpgradeSlot = lc.upgradeSlots[len(lc.upgradeSlots)-1]
	}
}

// findUpgradeSlotForSlot finds the applicable upgrade slot for a given slot.
// Returns upgradeSlot and prevUpgradeSlot. Must be called with at least read lock.
func (lc *LibraryCache) findUpgradeSlotForSlot(slot uint32) (upgradeSlot uint32, prevUpgradeSlot uint32) {
	// Linear search (upgrades are rare, list is tiny)
	upgradeSlot = lc.upgradeSlots[0]
	prevUpgradeSlot = base.MaxSlot // sentinel for base library

	for i, s := range lc.upgradeSlots {
		if s > slot {
			break
		}
		if i > 0 {
			prevUpgradeSlot = lc.upgradeSlots[i-1]
		}
		upgradeSlot = s
	}
	return
}

// upgradeLibraryDBPartition is the DB partition byte for upgrade libraries.
// This must match the constant in multistate/roots.go.
// Value: PartitionOther(2) + 1 + 1 + 1 + 1 = 6
const upgradeLibraryDBPartition = 0x06

// parseLibrary parses a library JSON blob using the unified embedded function resolver.
// The upgradeSlot parameter is used for caching purposes.
func (lc *LibraryCache) parseLibrary(jsonData []byte) *Library {
	lib, err := ParseLibraryFromJSON(jsonData, GetEmbeddedFunctionResolver)
	util.AssertNoError(err)

	result := newLibrary(lib, jsonData)
	result.Constants = ConstantsFromLibrary(lib)
	result.TxIntegrityValidatorPartialContextName, result.TxIntegrityValidatorFullContextName = VersionDataIntegrityValidatorNames(lib.VersionData)
	registerConstraints0(result)
	result.MustPreCompileTxIntegrityValidators()
	return result
}

// MustInitLibraryCache initializes the library cache with a state store.
// The store is used for lazy loading of library JSON blobs from the upgrade DB partition.
func MustInitLibraryCache(store common.Traversable) {
	var lib *Library

	// Scope for the lock - release before running inline tests to avoid deadlock
	// (inline tests call L() which needs to acquire the read lock)
	func() {
		libraryCacheMutex.Lock()
		defer libraryCacheMutex.Unlock()

		if libraryCache == nil {
			libraryCache = &LibraryCache{
				cache: make(map[uint32]*Library),
			}
		}
		util.Assertf(libraryCache.store == nil, "library cache already initialized")

		libraryCache.store = store
		libraryCache.loadUpgradeSlots()

		ledgerReset.Store(false)

		// Pre-load the genesis library (slot 0)
		lib = libraryCache.getOrLoad(0)
	}()

	// Run inline tests after releasing the lock to avoid deadlock
	runInlineTests(lib)
}

// MustInitLibraryCacheFromJSON initializes the library cache from raw JSON bytes.
// It creates a minimal in-memory store with a single library at slot 0.
// Use this when no persistent state store is available (CLI tools, testing).
// Unlike MustInitLibraryCache, this function allows re-initialization by resetting
// any existing cache first (safe for testing where multiple init calls may occur).
func MustInitLibraryCacheFromJSON(defJSON []byte) {
	MustInitLibraryCacheFromMap(map[uint32][]byte{0: defJSON})
}

// MustInitLibraryCacheFromMap initializes the library cache from a map of slot -> library JSON.
// Use this when the CLI needs to support multiple library versions (after upgrades).
func MustInitLibraryCacheFromMap(libraries map[uint32][]byte) {
	libraryCacheMutex.RLock()
	alreadyInit := libraryCache != nil && libraryCache.store != nil
	libraryCacheMutex.RUnlock()

	if alreadyInit {
		ResetForTesting()
	}

	store := &memLibraryStore{entries: libraries}
	MustInitLibraryCache(store)
}

// memLibraryStore is an in-memory Traversable store for library JSON blobs keyed by upgrade slot.
type memLibraryStore struct {
	entries map[uint32][]byte
}

func (s *memLibraryStore) Iterator(_ []byte) common.KVIterator {
	slots := make([]uint32, 0, len(s.entries))
	for slot := range s.entries {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	return &memLibraryIterator{store: s, slots: slots}
}

type memLibraryIterator struct {
	store *memLibraryStore
	slots []uint32
}

func (it *memLibraryIterator) Iterate(fun func(k, v []byte) bool) {
	for _, slot := range it.slots {
		key := make([]byte, 5)
		key[0] = upgradeLibraryDBPartition
		binary.BigEndian.PutUint32(key[1:], slot)
		if !fun(key, it.store.entries[slot]) {
			return
		}
	}
}

func (it *memLibraryIterator) IterateKeys(fun func(k []byte) bool) {
	for _, slot := range it.slots {
		key := make([]byte, 5)
		key[0] = upgradeLibraryDBPartition
		binary.BigEndian.PutUint32(key[1:], slot)
		if !fun(key) {
			return
		}
	}
}

// GetAllUpgradeSlots returns all upgrade slots up to and including maxSlot.
// The slots are returned in ascending order.
func GetAllUpgradeSlots(maxSlot uint32) []uint32 {
	libraryCacheMutex.RLock()
	defer libraryCacheMutex.RUnlock()

	if libraryCache == nil || libraryCache.store == nil {
		return nil
	}

	var slots []uint32
	prefix := []byte{upgradeLibraryDBPartition}
	libraryCache.store.Iterator(prefix).Iterate(func(k, v []byte) bool {
		if len(k) != 5 {
			return true // skip malformed entries
		}
		slot, err := base.SlotFromBytes(k[1:])
		util.AssertNoError(err)

		if slot <= maxSlot {
			slots = append(slots, slot)
		}
		return true
	})

	sort.Slice(slots, func(i, j int) bool {
		return slots[i] < slots[j]
	})

	return slots
}

// HasPendingUpgradeForSlot checks if there might be a pending upgrade that needs
// injection at or before the given slot. This is an optimization to avoid
// scanning all upgrades on every branch commit.
//
// Returns true if:
// - nextPendingUpgradeSlot is 0 (not initialized, need to check)
// - branchSlot >= nextPendingUpgradeSlot (might have pending upgrades)
func HasPendingUpgradeForSlot(branchSlot uint32) bool {
	pending := nextPendingUpgradeSlot.Load()
	if pending == 0 {
		// Not initialized, need to check all upgrades
		return true
	}
	return branchSlot >= pending
}

// UpdateNextPendingUpgradeSlot updates the tracking after upgrade UTXOs have been
// injected up to and including afterSlot. Sets the next pending slot to the first
// upgrade slot > afterSlot, or MaxSlot if none exists.
func UpdateNextPendingUpgradeSlot(afterSlot uint32) {
	allSlots := GetAllUpgradeSlots(base.MaxSlot)
	for _, slot := range allSlots {
		if slot > afterSlot {
			nextPendingUpgradeSlot.Store(slot)
			return
		}
	}
	// No more pending upgrades
	nextPendingUpgradeSlot.Store(base.MaxSlot)
}

// InitNextPendingUpgradeSlot initializes the pending upgrade tracking at startup.
// It checks which upgrade UTXOs are missing from the state and sets the next
// pending slot accordingly.
//
// This function should be called after MustInitLibraryCache and with access to
// the latest state reader.
func InitNextPendingUpgradeSlot(hasUTXO func(base.OutputID) bool) {
	allSlots := GetAllUpgradeSlots(base.MaxSlot)
	for _, slot := range allSlots {
		oid := base.UpgradeOutputID(slot)
		if !hasUTXO(oid) {
			// Found the first missing upgrade UTXO
			nextPendingUpgradeSlot.Store(slot)
			return
		}
	}
	// All upgrade UTXOs exist in state
	nextPendingUpgradeSlot.Store(base.MaxSlot)
}

// ResetForTesting clears the ledger singleton to allow re-initialization.
// This is only for testing purposes to get fresh genesis timestamps per test.
// DO NOT use in production code.
func ResetForTesting() {
	ledgerReset.Store(true)
	libraryCacheMutex.Lock()
	defer libraryCacheMutex.Unlock()
	libraryCache = nil
	nextPendingUpgradeSlot.Store(0)
}

// IsReset returns true if the ledger has been reset via ResetForTesting.
// Background goroutines can check this to avoid accessing nil library cache during shutdown.
func IsReset() bool {
	return ledgerReset.Load()
}

// InitWithTestingLedgerData for testing

type ParametersOption func(par *InitParameters)

func InitWithTestingLedgerData(opts ...ParametersOption) ed25519.PrivateKey {
	params, pk := GetTestingLedgerParams(31415926535)
	for _, opt := range opts {
		opt(&params)
	}
	lib := LibraryFromParameters(params)
	lib.MustPreCompileTxIntegrityValidators()
	MustInitLibraryCacheFromJSON(easyfl.ToJSON(lib.Library, true, false))
	return pk
}

func WithTickDuration(duration time.Duration) ParametersOption {
	return func(par *InitParameters) {
		par.TickDuration = duration
	}
}

// WithMineDifficulty sets the fair-launch mine-chain difficulty parameters.
// Tests use a low base difficulty so a valid proof-of-work can be found in a
// handful of attempts.
func WithMineDifficulty(base, floor, minPace int) ParametersOption {
	return func(par *InitParameters) {
		par.MineBaseDifficulty = base
		par.MineFloorDifficulty = floor
		par.MineMinPace = minPace
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

func WithAttachmentCostBudget(depth int) ParametersOption {
	return func(par *InitParameters) {
		par.AttachmentCostBudget = depth
	}
}

// WithTxIDStateTTLSlots overrides the non-branch txID retention (testing only — lets a short run
// cross the GC horizon). See claude/txid_ttl_tiered.md.
func WithTxIDStateTTLSlots(slots int) ParametersOption {
	return func(par *InitParameters) {
		par.TxIDStateTTLSlots = slots
	}
}

// WithBranchTxIDStateTTLSlots overrides the branch txID retention (testing only — lets a short run
// cross the branch GC horizon to exercise branch + RootRecord pruning). See claude/txid_ttl_tiered.md.
func WithBranchTxIDStateTTLSlots(slots int) ParametersOption {
	return func(par *InitParameters) {
		par.BranchTxIDStateTTLSlots = slots
	}
}

func WithCoverageContributionBounds(lower, upper uint64) ParametersOption {
	return func(par *InitParameters) {
		par.CoverageContributionLowerBound = lower
		par.CoverageContributionUpperBound = upper
		par.SetCoverageContributionBounds = true
	}
}

// WithEnforceCoverageDeltaMonotonicity toggles the per-milestone coverageDelta
// enforcement (on-chain within-slot strict-increase rule + attacher
// computed-vs-declared cross-check). Production keeps it true; specific attacher
// tests that hand-build sequencer milestones (and cannot declare the
// attacher-computed coverage) set it false.
func WithEnforceCoverageDeltaMonotonicity(enforce bool) ParametersOption {
	return func(par *InitParameters) {
		par.EnforceCoverageDeltaMonotonicity = enforce
	}
}

// WithHealthyCoverageFraction sets the on-chain healthy-branch coverage
// fraction (numerator/denominator). The default in production is 7/12.
// Tests with synthetic small-coverage branches typically set numerator=0
// (predicate becomes `0 < covDelta * den`, accepting any positive coverage),
// matching the WithCoverageContributionBounds(0,0) convention for relaxing the
// other coverage checks.
func WithHealthyCoverageFraction(numerator, denominator uint64) ParametersOption {
	return func(par *InitParameters) {
		par.HealthyCoverageNumerator = numerator
		par.HealthyCoverageDenominator = denominator
	}
}
