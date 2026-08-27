package node_cmd

import (
	"fmt"
	"strings"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
)

// compactCategory is one bucket of a compaction scan. The buckets are
// disjoint, so the counts sum to the account's whole UTXO set, and the
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

// scanEntry is one UTXO as the scan sees it.
type scanEntry struct {
	id     base.OutputID
	amount uint64
	// windowSlots is the number of slots until this output's window changes
	// state, and is only set where that is meaningful: for catSWDAccept the
	// slots left before the accept window shuts, for catPending the slots
	// until the claim opens. Zero elsewhere.
	windowSlots uint32
}

// compactScan is the result of classifying an account's UTXOs for compaction.
type compactScan struct {
	entries [numCategories][]scanEntry
	amounts [numCategories]uint64
	// numScanned is every UTXO looked at, including the ones no bucket
	// reports individually.
	numScanned int
	// limitExceeded is set when the node stopped iterating before the
	// account was exhausted, making every count below a lower bound.
	limitExceeded bool
	targetSlot    uint32
}

// scanForCompaction sorts an account's UTXOs into the compaction categories.
//
// Purely wallet-side: both classifiers are byte parses against the wallet
// library, and neither needs a private key — only the account's holder ID. So
// any account can be scanned, not just the wallet's own.
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

// compactableCount is how many UTXOs a sweep could consume right now.
func (s *compactScan) compactableCount() (count int, amount uint64) {
	for c := compactCategory(0); c < numCompactableCategories; c++ {
		count += len(s.entries[c])
		amount += s.amounts[c]
	}
	return
}

// atRisk reports whether anything is in a category that loses tokens if left
// alone — a closing accept window, or a fee that fell into the public window.
func (s *compactScan) atRisk() bool {
	return s.count(catSWDAccept) > 0 || s.count(catTagAlongCleanup) > 0
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

// displayCompactScan renders a scan. maxInputsPerTx only scales the closing
// estimate of how many transactions a full drain would take.
func displayCompactScan(s *compactScan, account ledger.Controller, branchID base.TransactionID, lrbDepth, maxInputsPerTx int) {
	glb.Infof("\nCOMPACTION SCAN of %s", account.String())
	depthNote := "the LRB"
	if lrbDepth > 0 {
		depthNote = fmt.Sprintf("%d branch(es) back from the LRB", lrbDepth)
	}
	glb.Infof("    state read on %s: %s", depthNote, branchID.StringShort())
	glb.Infof("    windows evaluated at slot %d; %d UTXO(s) scanned", s.targetSlot, s.numScanned)
	if s.limitExceeded {
		glb.Infof("\n    NOTE: the node stopped iterating before this account was exhausted.")
		glb.Infof("          Every count below is a LOWER BOUND. Compaction still converges:")
		glb.Infof("          each round rescans, it just takes more rounds.")
	}

	line := func(c compactCategory, note string) {
		if s.count(c) == 0 {
			return
		}
		glb.Infof("      %-18s %7d %20s   %s", c, s.count(c), util.Th(s.amounts[c]), note)
	}

	count, amount := s.compactableCount()
	glb.Infof("\n    COMPACTABLE          count               tokens")
	if count == 0 {
		glb.Infof("      (nothing)")
	}
	line(catSWDAccept, fmt.Sprintf("accept window closes in %d slot(s) — claim these first",
		s.minWindowSlots(catSWDAccept)))
	line(catTagAlongCleanup, "public — anyone may claim these")
	line(catTagAlongReclaim, "")
	line(catSWDReclaim, "")
	line(catSigLock, "")
	if count > 0 {
		glb.Infof("      %-18s %7s %20s", "", strings.Repeat("-", 7), strings.Repeat("-", 20))
		glb.Infof("      %-18s %7d %20s", "total", count, util.Th(amount))
	}

	if s.numScanned > count {
		glb.Infof("\n    NOT COMPACTABLE      count               tokens")
		line(catNeedsReturn, "URGENT: accept window closing, needs a return receipt")
		line(catUnknown, "unrecognised structure — refused, not consumed")
		line(catPending, fmt.Sprintf("claimable later; the first opens in %d slot(s)",
			s.minWindowSlots(catPending)))
		line(catChained, "delegations, foundries, sequencer chains")
		line(catNoClaim, "this account has no claim on them")
	}

	if s.count(catNeedsReturn) > 0 {
		glb.Infof("\n    %d output(s) accepted as sendWithDeadline target carry returnToSender.", s.count(catNeedsReturn))
		glb.Infof("    Claiming one obliges a return receipt to the master in the same transaction,")
		glb.Infof("    which compact does not build. Their accept window closes like any other.")
	}

	if count >= 2 {
		numTx := (count + maxInputsPerTx - 1) / maxInputsPerTx
		glb.Infof("\n    a full drain is roughly %d transaction(s) at %d inputs each, each paying one tag-along fee",
			numTx, maxInputsPerTx)
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
				glb.Verbosef("      %s  %s%s", e.id.StringShort(), util.Th(e.amount), suffix)
			}
		}
	}
}
