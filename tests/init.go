package tests

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	// The test genesis folds the release's upgrades, so tests run on the current definitions. A
	// test about an upgrade starts from a genesis without them: withUnfoldedGenesis.
	initTestLedger()
}

// extraLedgerOptions are added to the test ledger's parameters by a test that needs other
// ones, such as a low mine difficulty, for every ledger it (re)initializes
var extraLedgerOptions []ledger.ParametersOption

// initTestLedger initializes the ledger for testing. Called once in init().
func initTestLedger() {
	genesisPrivateKey = initLedger(append([]ledger.ParametersOption{
		ledger.WithTickDuration(8 * time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithCoverageContributionUpperBound(2 * ledger.DefaultInitialSupply),
	}, extraLedgerOptions...)...)
	lib := ledger.L(base.MaxSlot)
	fmt.Printf(`
>>> ledger parameters for the test <<<
     tick duration        : %v
     slot duration        : %v
     transaction pace     : %d ticks
     sequencer pace       : %d ticks
     attachment cost budget: %d
`,
		lib.TickDuration,
		lib.SlotDuration(),
		lib.TransactionPace,
		lib.TransactionPaceSequencer,
		lib.AttachmentCostBudget,
	)
}

// unfoldedGenesis is set while a test of upgrades runs: every ledger it (re)initializes is
// a genesis of upgrade 0 alone, not one folding the test's own upgrade list.
var unfoldedGenesis bool

// initLedger initializes the test ledger with the options, folding the listed upgrades into
// the genesis unless a test of upgrades runs.
func initLedger(opts ...ledger.ParametersOption) ed25519.PrivateKey {
	if unfoldedGenesis {
		opts = append(opts, ledger.WithoutFoldedUpgrades())
	}
	return ledger.InitWithTestingLedgerData(opts...)
}

// withUnfoldedGenesis starts a test of upgrades from a genesis of upgrade 0 alone, lists the
// test's upgrades, and restores the release's list and the package ledger afterwards.
func withUnfoldedGenesis(t *testing.T, upgrades []*ledger.UpgradeDefinition) {
	t.Helper()
	saved := ledger.Upgrades
	unfoldedGenesis = true
	ledger.ResetForTesting()
	initTestLedger()
	ledger.Upgrades = upgrades
	t.Cleanup(func() {
		unfoldedGenesis = false
		ledger.Upgrades = saved
		reinitTestLedger()
	})
}

// reinitTestLedger resets and re-initializes the ledger with a fresh genesis timestamp.
// Use this at the start of tests that depend on fresh genesis timing.
func reinitTestLedger() {
	ledger.ResetForTesting()
	initTestLedger()
}

// reinitTestLedgerWithBudget resets and re-initializes the ledger with a custom attachment cost budget.
// Use this to test budget-exceeded scenarios with a lower budget.
// Returns a cleanup function that restores the original budget.
func reinitTestLedgerWithBudget(budget int) func() {
	ledger.ResetForTesting()
	genesisPrivateKey = initLedger(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(budget),
		ledger.WithCoverageContributionUpperBound(2*ledger.DefaultInitialSupply),
	)
	return func() {
		// Restore default budget
		ledger.ResetForTesting()
		initTestLedger()
	}
}

// reinitTestLedgerNoCoverageMonotonicity resets and re-initializes the ledger
// with the per-milestone coverageDelta enforcement turned OFF. Used by the
// hand-built attacher tests that assemble sequencer milestones directly and
// cannot declare the attacher-computed full-past-cone coverageDelta. Returns a
// cleanup function that restores defaults (enforcement ON). Extra options are added last.
func reinitTestLedgerNoCoverageMonotonicity(extra ...ledger.ParametersOption) func() {
	ledger.ResetForTesting()
	genesisPrivateKey = initLedger(append([]ledger.ParametersOption{
		ledger.WithTickDuration(8 * time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithCoverageContributionUpperBound(2 * ledger.DefaultInitialSupply),
		ledger.WithEnforceCoverageDeltaMonotonicity(false),
	}, extra...)...)
	return func() {
		ledger.ResetForTesting()
		initTestLedger()
	}
}

// reinitTestLedgerWithCoverageUpperBound resets and re-initializes the ledger with a custom
// coverage-contribution upper bound. Used to test that sequencers with coverage above it
// cannot produce branches. Returns a cleanup function that restores defaults.
func reinitTestLedgerWithCoverageUpperBound(upperBound uint64) func() {
	ledger.ResetForTesting()
	genesisPrivateKey = initLedger(
		ledger.WithTickDuration(8*time.Millisecond),
		ledger.WithTransactionPace(3),
		ledger.WithTransactionPaceSequencer(3),
		ledger.WithAttachmentCostBudget(600),
		ledger.WithCoverageContributionUpperBound(upperBound),
	)
	return func() {
		ledger.ResetForTesting()
		initTestLedger()
	}
}
