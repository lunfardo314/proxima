package node_cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// A compaction scan answers "what can this account consume right now", and the
// answer comes from two places that do not overlap in how they are found:
//
//   - outputs INDEXED under the account — its own sigLock outputs and the
//     conditional locks it has a role in. Found by index lookup, cheap, exact.
//   - outputs abandoned by ANYBODY that have decayed into the public window of
//     their conditional lock. Nobody's in particular, so nothing indexes them
//     under this account; they are found by walking old state, and any signer
//     including this one may take them. This is what `proxi node utxo-cleanup`
//     sweeps.
//
// Reporting only the first is what makes a wallet look empty while a cleaner
// is busy consuming thousands of outputs, so the scan reports both and says
// where the boundary is.

// compactCategory is one bucket of the account's own UTXOs. The buckets are
// disjoint, so the counts sum to the account's whole indexed set, and the
// compactable ones are ordered by urgency: what is lost first comes first.
//
// The taxonomy is not invented here — ClassifySpendable and ClassifyLock
// already distinguish every case; this only names the buckets and orders them.
type compactCategory int

const (
	// Compactable: claimable by this account with a plain signature unlock,
	// producing no extra output. These are what a sweep consumes.

	// catSWDAccept — the account is the sigLock target of a sendWithDeadline
	// and the accept window is still open. The only category whose window
	// CLOSES: missing it forfeits the payment to the master.
	catSWDAccept compactCategory = iota
	// catTagAlongCleanup — the account's own prepaid fee, never taken by the
	// target sequencer, now past the reclaim window and claimable by anyone.
	// Still the account's own output, so a sweep keeps claiming it, but a
	// cleaner may get there first.
	catTagAlongCleanup
	// catTagAlongReclaim — same, while the claim is still exclusive.
	catTagAlongReclaim
	// catSWDReclaim — the account is master of a sendWithDeadline the target
	// never accepted. Stays claimable forever.
	catSWDReclaim
	// catSigLock — plain sigLock outputs. Pure UTXO-count reduction.
	catSigLock

	numCompactableCategories

	// Report-only: counted so the totals describe the account rather than
	// the sweep, never swept.

	// catNeedsReturn — a sendWithDeadline accepted as target carrying
	// returnToSender. Claiming it obliges a return receipt to the master in
	// the same transaction, which the compact builder does not produce, so
	// these are lost when their window closes unless claimed by other means.
	catNeedsReturn
	// catUnknown — a lock-level claim exists but the output carries
	// constraints the wallet does not recognise. Refused, not consumed.
	catUnknown
	// catPending — the account has a role but its window has not opened yet.
	catPending
	// catChained — chain outputs (delegations, foundries, sequencer chains).
	// Never compactable: a chain transition is a different transaction shape.
	catChained
	// catNoClaim — the account appears in the index but has no claim: a
	// tag-along target side, a chainLock-target sendWithDeadline, an accept
	// window that already closed.
	catNoClaim

	numCategories
)

// compactCategoryNames are the CLI-facing names of the categories, and the
// order they are reported in.
var compactCategoryNames = [numCategories]string{
	catSWDAccept:       "swd-accept",
	catTagAlongCleanup: "tagalong-cleanup",
	catTagAlongReclaim: "tagalong-reclaim",
	catSWDReclaim:      "swd-reclaim",
	catSigLock:         "siglock",
	catNeedsReturn:     "needs-return",
	catUnknown:         "unknown",
	catPending:         "pending",
	catChained:         "on chains",
	catNoClaim:         "no claim",
}

func (c compactCategory) String() string { return compactCategoryNames[c] }

// compactCategoryHelp is the one-line description of what compacting a single
// category would sweep. Only the compactable categories have one.
var compactCategoryHelp = [numCompactableCategories]string{
	catSWDAccept:       "accept sendWithDeadline payments to this wallet before their window closes",
	catTagAlongCleanup: "reclaim own tag-along fees that fell into the public window",
	catTagAlongReclaim: "reclaim own tag-along fees the target sequencer never took",
	catSWDReclaim:      "reclaim own sendWithDeadline outputs the target never accepted",
	catSigLock:         "sweep plain sigLock outputs — pure UTXO-count reduction",
}

// scanEntry is one of the account's UTXOs as the scan sees it.
type scanEntry struct {
	id     base.OutputID
	amount uint64
	// windowSlots is the number of slots until this output's window changes
	// state, and is only set where that is meaningful: for catSWDAccept the
	// slots left before the accept window shuts, for catPending the slots
	// until the claim opens. Zero elsewhere.
	windowSlots uint32
	// public marks an output whose conditional lock has decayed all the way
	// into its public window. Still the account's own to claim, but no longer
	// exclusively — it is simultaneously part of the pool any cleaner works
	// through, which is why it is also the overlap between the two halves of
	// this report.
	public bool
}

// dustScan is the global pool of publicly-claimable outputs: abandoned by
// whoever created them, consumable by anybody, and identical no matter which
// account is being scanned.
type dustScan struct {
	// byLock is the tally keyed by lock symbol, as the node reports it.
	byLock map[string]api.CleanableTally
	// needsReturn counts dust that is public but carries returnToSender, so
	// taking it owes the master a receipt a plain sweep cannot build.
	needsReturn int
	// complete is false when the walk stopped before reaching the oldest
	// state, making every number here a lower bound.
	complete   bool
	stoppedAt  uint32
	roundsUsed int
}

func (d *dustScan) totals() (count int, amount uint64) {
	for _, t := range d.byLock {
		count += t.Count
		amount += t.Amount
	}
	return
}

// compactScan is the result of classifying an account's UTXOs for compaction.
type compactScan struct {
	entries [numCategories][]scanEntry
	amounts [numCategories]uint64
	// numScanned is every indexed UTXO looked at, including the ones no
	// bucket reports individually.
	numScanned int
	// limitExceeded is set when the node stopped iterating before the
	// account was exhausted, making the per-category counts a lower bound.
	limitExceeded bool
	targetSlot    uint32
	dust          *dustScan
}

// scanForCompaction sorts an account's indexed UTXOs into the categories.
//
// Purely wallet-side: every classifier is a byte parse against the wallet
// library, and none needs a private key — only the account's holder ID. So any
// account can be scanned, not just the wallet's own.
//
// targetSlot is when a compacting transaction would land, and is what every Δ
// window is measured against. It is deliberately independent of which state
// snapshot outs was read from: a settled snapshot still has to be judged
// against the slot the transaction will actually be validated in.
func scanForCompaction(
	lib *txbuildercore.Library[any],
	consts *txbuildercore.Constants,
	outs []*ledger.OutputWithID,
	accountHID base.HolderID,
	targetSlot uint32,
) *compactScan {
	ret := &compactScan{numScanned: len(outs), targetSlot: targetSlot}

	for _, o := range outs {
		utxoBytes := o.Output.Bytes()
		createSlot := o.ID.Slot()
		e := scanEntry{id: o.ID, amount: o.Output.TokenBalance()}

		// Chain outputs first: a delegation or foundry carries a sigLock the
		// account matches, so the spendable classifier would call it Unknown
		// and bury the account's chains in an error-shaped bucket.
		if chainBin, err := o.Output.ConstraintAt(ledger.ConstraintIndexChain); err == nil && len(chainBin) > 0 {
			ret.add(catChained, e)
			continue
		}

		cls, err := txbuildercore.ClassifySpendable(lib, utxoBytes, createSlot, accountHID, targetSlot, consts.TagAlongSlots)
		glb.AssertNoError(err)
		lockKind, err := lib.ClassifyLock(utxoBytes, accountHID)
		glb.AssertNoError(err)

		// The same question the cleaner asks, asked of the account's own
		// outputs: has this lock decayed into its public window? True for any
		// conditional lock, not just tag-along, so a public sendWithDeadline
		// is flagged as readily as a public fee.
		if cleanCls, err := txbuildercore.ClassifyCleanable(lib, utxoBytes, createSlot, targetSlot, consts.TagAlongReclaimSlots); err == nil {
			e.public = cleanCls == txbuildercore.CleanSimple || cleanCls == txbuildercore.CleanNeedsReturn
		}

		switch cls {
		case txbuildercore.SpendNeedsReturn:
			ret.add(catNeedsReturn, e)
			continue
		case txbuildercore.SpendUnknown:
			ret.add(catUnknown, e)
			continue
		case txbuildercore.SpendNotForAccount:
			if slots, pending := opensIn(lib, consts, utxoBytes, lockKind, createSlot, targetSlot); pending {
				e.windowSlots = slots
				ret.add(catPending, e)
			} else {
				ret.add(catNoClaim, e)
			}
			continue
		}

		// SpendSimple. The two classifiers read the same bytes, so the lock
		// kind is one of the four claimable shapes; anything else is a bug in
		// one of them rather than a property of the output.
		switch lockKind {
		case txbuildercore.LockKindSig:
			ret.add(catSigLock, e)
		case txbuildercore.LockKindSWDMaster:
			ret.add(catSWDReclaim, e)
		case txbuildercore.LockKindSWDTargetSig:
			if acceptance, ok := txbuildercore.SWDAcceptanceSlots(lib, utxoBytes); ok {
				e.windowSlots = acceptance - (targetSlot - createSlot)
			}
			ret.add(catSWDAccept, e)
		case txbuildercore.LockKindTagAlongSender:
			if targetSlot-createSlot >= consts.TagAlongReclaimSlots {
				ret.add(catTagAlongCleanup, e)
			} else {
				ret.add(catTagAlongReclaim, e)
			}
		default:
			glb.Fatalf("compaction scan: output %s is simply claimable but its lock is unclassified", o.ID.StringShort())
		}
	}
	return ret
}

func (s *compactScan) add(c compactCategory, e scanEntry) {
	s.entries[c] = append(s.entries[c], e)
	s.amounts[c] += e.amount
}

func (s *compactScan) count(c compactCategory) int { return len(s.entries[c]) }

// compactableCount is how many of the account's own UTXOs a sweep could
// consume right now.
func (s *compactScan) compactableCount() (count int, amount uint64) {
	for c := compactCategory(0); c < numCompactableCategories; c++ {
		count += len(s.entries[c])
		amount += s.amounts[c]
	}
	return
}

// ownPublic counts the account's own outputs that have decayed into a public
// window. They are the overlap between the two halves of the report: claimable
// by this account because it has a role, and simultaneously part of the pool
// every cleaner works through.
func (s *compactScan) ownPublic() (count int, amount uint64) {
	for c := compactCategory(0); c < numCategories; c++ {
		for _, e := range s.entries[c] {
			if e.public {
				count++
				amount += e.amount
			}
		}
	}
	return
}

// atRisk reports whether anything is in a category that loses tokens if left
// alone — a closing accept window, or an output in a public window.
func (s *compactScan) atRisk() bool {
	if s.count(catSWDAccept) > 0 {
		return true
	}
	n, _ := s.ownPublic()
	return n > 0
}

// minWindowSlots is the tightest window in a category, i.e. the deadline that
// arrives first. Meaningful only where scanEntry.windowSlots is set.
func (s *compactScan) minWindowSlots(c compactCategory) uint32 {
	first := true
	var ret uint32
	for _, e := range s.entries[c] {
		if first || e.windowSlots < ret {
			ret, first = e.windowSlots, false
		}
	}
	return ret
}

// opensIn reports whether an output the account cannot claim yet will become
// claimable later, and in how many slots. Both windows are Δ ≥ threshold, so
// waiting is all it takes. An accept window that already closed is not pending:
// it never reopens.
func opensIn(
	lib *txbuildercore.Library[any],
	consts *txbuildercore.Constants,
	utxoBytes []byte,
	lockKind txbuildercore.LockKind,
	createSlot, targetSlot uint32,
) (uint32, bool) {
	if targetSlot < createSlot {
		return 0, false
	}
	delta := targetSlot - createSlot
	switch lockKind {
	case txbuildercore.LockKindTagAlongSender:
		if delta < consts.TagAlongSlots {
			return consts.TagAlongSlots - delta, true
		}
	case txbuildercore.LockKindSWDMaster:
		if acceptance, ok := txbuildercore.SWDAcceptanceSlots(lib, utxoBytes); ok && delta < acceptance {
			return acceptance - delta, true
		}
	}
	return 0, false
}

// scanPublicDust totals the publicly-claimable pool by walking old state in
// count-only mode.
//
// Counting cannot be done by paging the ordinary cleanable scan: it has no
// within-chunk cursor, so a caller that only reads gets the same batch back
// forever. A cleaner escapes that by consuming what it was handed; a report
// cannot, which is why the node tallies server-side instead.
//
// maxRounds bounds the walk; 0 means "until the oldest state". Whatever it does
// not reach is reported as a lower bound rather than silently dropped.
func scanPublicDust(clnt *client.APIClient, maxRounds int) *dustScan {
	ret := &dustScan{byLock: make(map[string]api.CleanableTally)}
	var par client.CleanableOutputsParams
	par.CountOnly = true

	for maxRounds <= 0 || ret.roundsUsed < maxRounds {
		res, err := clnt.GetCleanableOutputs(par)
		glb.AssertNoError(err)
		ret.roundsUsed++
		ret.needsReturn += res.NeedsReturn
		for sym, t := range res.Tally {
			acc := ret.byLock[sym]
			acc.Count += t.Count
			acc.Amount += t.Amount
			ret.byLock[sym] = acc
		}
		if res.Exhausted {
			ret.complete = true
			return ret
		}
		// A count-only scan walks its whole chunk budget rather than cutting
		// on what it finds, so the cursor always moves. Stopping on a stalled
		// cursor anyway keeps a server-side change from turning into a spin.
		if par.FromChunkSet && res.NextChunk >= par.FromChunk {
			break
		}
		par.FromChunk, par.FromChunkSet = res.NextChunk, true
		ret.stoppedAt = res.NextChunk
	}
	return ret
}

// displayCompactScan renders a scan. maxInputsPerTx only scales the closing
// estimate of how many transactions a full drain would take.
func displayCompactScan(s *compactScan, account ledger.Controller, branchID base.TransactionID, lrbDepth, maxInputsPerTx int) {
	glb.Infof("\nCOMPACTION SCAN of %s", account.String())
	depthNote := "the LRB"
	if lrbDepth > 0 {
		depthNote = fmt.Sprintf("%d branch(es) back from the LRB", lrbDepth)
	}
	glb.Infof("    state read on %s: %s", depthNote, branchID.StringShort())
	glb.Infof("    windows evaluated at slot %d; %d UTXO(s) indexed under this account", s.targetSlot, s.numScanned)
	if s.limitExceeded {
		glb.Infof("\n    NOTE: the node stopped iterating before this account was exhausted.")
		glb.Infof("          The per-category counts are a LOWER BOUND. Compaction still")
		glb.Infof("          converges: each round rescans, it just takes more rounds.")
	}

	line := func(name string, count int, amount uint64, note string) {
		glb.Infof("      %-18s %7d %20s   %s", name, count, util.Th(amount), note)
	}
	cat := func(c compactCategory, note string) {
		if s.count(c) == 0 {
			return
		}
		line(c.String(), s.count(c), s.amounts[c], note)
	}
	rule := func() {
		glb.Infof("      %-18s %7s %20s", "", strings.Repeat("-", 7), strings.Repeat("-", 20))
	}

	ownCount, ownAmount := s.compactableCount()
	glb.Infof("\n    YOURS, COMPACTABLE   count               tokens")
	if ownCount == 0 {
		glb.Infof("      (nothing)")
	}
	cat(catSWDAccept, fmt.Sprintf("accept window closes in %d slot(s) — claim these first",
		s.minWindowSlots(catSWDAccept)))
	cat(catTagAlongCleanup, "public — anyone may claim these")
	cat(catTagAlongReclaim, "")
	cat(catSWDReclaim, "")
	cat(catSigLock, "")
	if ownCount > 0 {
		rule()
		line("subtotal", ownCount, ownAmount, "")
	}

	if s.numScanned > ownCount {
		glb.Infof("\n    YOURS, NOT COMPACTABLE")
		cat(catNeedsReturn, "URGENT: accept window closing, needs a return receipt")
		cat(catUnknown, "unrecognised structure — refused, not consumed")
		cat(catPending, fmt.Sprintf("claimable later; the first opens in %d slot(s)",
			s.minWindowSlots(catPending)))
		cat(catChained, "delegations, foundries, sequencer chains")
		cat(catNoClaim, "this account has no claim on them")
	}

	dustCount, dustAmount := 0, uint64(0)
	if s.dust != nil {
		dustCount, dustAmount = s.dust.totals()
		glb.Infof("\n    PUBLIC, ABANDONED BY ANYBODY — any signer may claim these, including you")
		syms := make([]string, 0, len(s.dust.byLock))
		for sym := range s.dust.byLock {
			syms = append(syms, sym)
		}
		sort.Strings(syms)
		for _, sym := range syms {
			line(sym, s.dust.byLock[sym].Count, s.dust.byLock[sym].Amount, "")
		}
		if dustCount == 0 {
			glb.Infof("      (nothing)")
		} else {
			rule()
			line("subtotal", dustCount, dustAmount, "")
		}
		if s.dust.needsReturn > 0 {
			glb.Infof("      %d further output(s) are public but carry returnToSender — taking one owes",
				s.dust.needsReturn)
			glb.Infof("      the master a receipt, so they are left alone and not counted above.")
		}
		if s.dust.complete {
			glb.Infof("      scan reached the oldest state: this pool is complete.")
		} else {
			glb.Infof("      LOWER BOUND: the walk stopped at chunk %d after %d round(s). Raise",
				s.dust.stoppedAt, s.dust.roundsUsed)
			glb.Infof("      --public-rounds (0 = no limit) to count the rest.")
		}
		glb.Infof("      This pool is not specific to the account: it is the same for everybody,")
		glb.Infof("      it is a race, and 'proxi node utxo-cleanup' is what sweeps it.")
	}

	overlapCount, overlapAmount := s.ownPublic()
	if s.dust != nil {
		glb.Infof("\n    TOTAL CONSUMABLE     count               tokens")
		line("total", ownCount+dustCount-overlapCount, ownAmount+dustAmount-overlapAmount, "")
		if overlapCount > 0 {
			glb.Infof("      %d of your own output(s) (%s) sit in a public window and are counted in",
				overlapCount, util.Th(overlapAmount))
			glb.Infof("      both sections above; the total counts them once.")
		}
	}

	if ownCount >= 2 {
		numTx := (ownCount + maxInputsPerTx - 1) / maxInputsPerTx
		glb.Infof("\n    compacting your own would be roughly %d transaction(s) at %d inputs each,",
			numTx, maxInputsPerTx)
		glb.Infof("    each paying one tag-along fee")
	}

	if glb.IsVerbose() {
		for c := compactCategory(0); c < numCategories; c++ {
			if s.count(c) == 0 {
				continue
			}
			glb.Verbosef("\n    %s (%d):", c, s.count(c))
			for _, e := range s.entries[c] {
				suffix := ""
				if e.windowSlots > 0 {
					suffix = fmt.Sprintf("  window %d slot(s)", e.windowSlots)
				}
				if e.public {
					suffix += "  [public]"
				}
				glb.Verbosef("      %s  %s%s", e.id.StringShort(), util.Th(e.amount), suffix)
			}
		}
	}
}

var (
	scanLRBDepth     int
	scanPublicRounds int
	scanNoPublic     bool
)

func initCompactScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scan",
		Aliases: []string{"stats"},
		Short:   `report everything this account can consume, by category`,
		Long: `Report what the account could consume right now, by category and in
urgency order, building nothing.

Two pools are counted, because they are found in two different ways:

  YOURS   — outputs indexed under the account: plain sigLock outputs and the
            conditional locks it has a role in (sendWithDeadline it can accept
            or reclaim, tag-along fees it prepaid and can take back). These are
            what 'proxi node compact' sweeps.

  PUBLIC  — outputs abandoned by anybody whose conditional lock has decayed
            into its public window, where any signer may consume them. Nothing
            indexes these under an account, so they are found by walking old
            state. The same pool for everybody, and a race. This is what
            'proxi node utxo-cleanup' sweeps.

An output of the account's own that has fallen into a public window belongs to
both pools; the grand total counts it once.

Read-only, and the account is whatever --target names, so any sigLock account
can be scanned, not only the wallet's own.`,
		Args: cobra.NoArgs,
		Run:  runCompactScanCmd,
	}
	glb.AddFlagTarget(cmd)
	cmd.PersistentFlags().IntVar(&scanLRBDepth, "lrb-depth", 0,
		"read state N branches back from the LRB (0 = the LRB itself)")
	cmd.PersistentFlags().IntVar(&scanPublicRounds, "public-rounds", 0,
		"cap the walk over old state for public dust (0 = until the oldest state)")
	cmd.PersistentFlags().BoolVar(&scanNoPublic, "no-public", false,
		"skip the public pool and report only what is indexed under the account")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runCompactScanCmd(_ *cobra.Command, _ []string) {
	accountable := glb.MustGetTarget()
	glb.Assertf(scanLRBDepth >= 0, "--lrb-depth must be >= 0")

	controllerID := accountable.ControllerID()
	glb.Assertf(len(controllerID) == len(base.HolderID{}),
		"a compaction scan needs a sigLock account (a/...); %s is controlled by a %d-byte ID",
		accountable.String(), len(controllerID))
	var accountHID base.HolderID
	copy(accountHID[:], controllerID)

	res, err := glb.GetClient().GetOutputsForControllerID(controllerID, client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		MaxOutputs: api.GetOutputsIterationCap,
		LRBDepth:   scanLRBDepth,
	})
	glb.AssertNoError(err)

	// Windows are judged at the current slot rather than at the slot of the
	// state that was read: they decide what a transaction issued NOW could
	// claim, and such a transaction is validated at its own timestamp.
	scan := scanForCompaction(
		glb.GetTxLibrary(),
		glb.GetLedgerConstants(),
		res.Outputs,
		accountHID,
		glb.GetLedgerTimeNow().Slot,
	)
	scan.limitExceeded = res.LimitExceeded
	if !scanNoPublic {
		scan.dust = scanPublicDust(glb.GetClient(), scanPublicRounds)
	}
	displayCompactScan(scan, accountable, res.LRBID, res.LRBDepth, defaultMaxNumberOfInputs)
}
