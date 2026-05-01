package txstore

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/proxi/glb"
	proxitxstore "github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/adaptors/badger_adaptor"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/cobra"
)

// txstore audit flags
var (
	auditValidate bool
	auditOutput   string
	auditMeta     bool
)

const (
	auditWriteBatchSize       = 1_000
	auditProgressInterval     = 100   // emit one stats line every N completed slots
	auditPhase1FallbackSearch = 1_024 // slots to scan downward when <slot from> has no branches
	auditMaxMissingSamples    = 5     // referrers recorded per missing dep
	auditMaxFailureSamples    = 20    // validation failures retained verbatim in the report
)

func initAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit <slot from> [<slot back to, default 0>]",
		Short: "walk past cone of all branches in <slot from>, audit completeness, and optionally validate or copy to a new txstore",
		Args:  cobra.RangeArgs(1, 2),
		Run:   runAuditCmd,
	}
	// shorthand is upper-case -V because lower-case -v is the global --verbose flag.
	cmd.PersistentFlags().BoolVarP(&auditValidate, "validate", "V", false, "run full-context validation on every visited transaction (requires multistate DB). Note: shorthand is upper-case -V; lower-case -v is the global --verbose flag")
	cmd.PersistentFlags().StringVarP(&auditOutput, "output", "o", "", "write visited transactions to a new txstore at this path (refuses if path exists)")
	cmd.PersistentFlags().BoolVarP(&auditMeta, "meta", "m", false, "preserve per-transaction metadata in --output (default: write empty metadata)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// txStoreIter is the read surface the audit needs: id-keyed fetch plus prefix
// iteration. *txstore.SimpleTxBytesStore satisfies it. Local to keep audit
// logic decoupled from the underlying KV adapter.
type txStoreIter interface {
	global.TxBytesGet
	Iterator(prefix []byte) common.KVIterator
}

// categoryCounts buckets txids by branch / sequencer-non-branch / non-sequencer
// purely from the txid bits — never requires loading the tx bytes.
type categoryCounts struct {
	Branch, SeqNonBranch, NonSeq int
}

func (c *categoryCounts) Add(txid base.TransactionID) {
	switch {
	case txid.IsBranchTransaction():
		c.Branch++
	case txid.IsSequencerTransaction():
		c.SeqNonBranch++
	default:
		c.NonSeq++
	}
}

func (c categoryCounts) Total() int { return c.Branch + c.SeqNonBranch + c.NonSeq }

func (c categoryCounts) Sub(o categoryCounts) categoryCounts {
	return categoryCounts{Branch: c.Branch - o.Branch, SeqNonBranch: c.SeqNonBranch - o.SeqNonBranch, NonSeq: c.NonSeq - o.NonSeq}
}

func (c categoryCounts) AddCC(o categoryCounts) categoryCounts {
	return categoryCounts{Branch: c.Branch + o.Branch, SeqNonBranch: c.SeqNonBranch + o.SeqNonBranch, NonSeq: c.NonSeq + o.NonSeq}
}

// frontier is the working set used both for `C` (unprocessed) and `visited`
// (recently-processed, kept only as long as some future C entry might
// reference it). Indexed by txid for O(1) membership and by slot for fast
// max-slot lookup and bulk pruning.
type frontier struct {
	txs    map[base.TransactionID]*transaction.Transaction
	bySlot map[uint32]map[base.TransactionID]struct{}
}

func newFrontier() *frontier {
	return &frontier{
		txs:    make(map[base.TransactionID]*transaction.Transaction),
		bySlot: make(map[uint32]map[base.TransactionID]struct{}),
	}
}

func (f *frontier) Add(tx *transaction.Transaction) bool {
	id := tx.ID()
	if _, ok := f.txs[id]; ok {
		return false
	}
	f.txs[id] = tx
	s := tx.Timestamp().Slot
	bucket, ok := f.bySlot[s]
	if !ok {
		bucket = make(map[base.TransactionID]struct{})
		f.bySlot[s] = bucket
	}
	bucket[id] = struct{}{}
	return true
}

func (f *frontier) Remove(id base.TransactionID) bool {
	tx, ok := f.txs[id]
	if !ok {
		return false
	}
	delete(f.txs, id)
	s := tx.Timestamp().Slot
	delete(f.bySlot[s], id)
	if len(f.bySlot[s]) == 0 {
		delete(f.bySlot, s)
	}
	return true
}

func (f *frontier) Get(id base.TransactionID) (*transaction.Transaction, bool) {
	tx, ok := f.txs[id]
	return tx, ok
}

func (f *frontier) Has(id base.TransactionID) bool {
	_, ok := f.txs[id]
	return ok
}

func (f *frontier) Len() int { return len(f.txs) }

// MaxSlot returns the highest slot present and whether the frontier is non-empty.
// Cost is O(|distinct slots|) — bounded by DAG thickness, ≪ |txs|.
func (f *frontier) MaxSlot() (uint32, bool) {
	var maxS uint32
	found := false
	for s := range f.bySlot {
		if !found || s > maxS {
			maxS = s
			found = true
		}
	}
	return maxS, found
}

// AnyAtSlot returns an arbitrary tx at the given slot, or (nil, false) if none.
func (f *frontier) AnyAtSlot(s uint32) (*transaction.Transaction, bool) {
	bucket, ok := f.bySlot[s]
	if !ok {
		return nil, false
	}
	for id := range bucket {
		return f.txs[id], true
	}
	return nil, false
}

// DeleteWhereSlotGreaterThan drops all entries with slot > threshold. Used to
// prune `visited` once the max slot still pending in `C` has dropped past
// them — those entries can no longer be needed as input producers for any
// remaining C entry (deps go strictly older as we walk backward).
func (f *frontier) DeleteWhereSlotGreaterThan(threshold uint32) int {
	deleted := 0
	for s, bucket := range f.bySlot {
		if s <= threshold {
			continue
		}
		for id := range bucket {
			delete(f.txs, id)
			deleted++
		}
		delete(f.bySlot, s)
	}
	return deleted
}

type validationFailure struct {
	txid base.TransactionID
	err  string
}

type auditState struct {
	src   txStoreIter
	dst   *proxitxstore.SimpleTxBytesStore
	floor uint32

	C           *frontier
	visited     *frontier
	branchesInC map[uint32]int // slot -> count of branch txs currently in C

	startSlot           uint32
	branchesInStartSlot int
	earliestReached     uint32
	latestReached       uint32

	// Aggregates over the whole run
	visitedTotal  categoryCounts
	seenInDBTotal categoryCounts // sum of per-slot in-DB counts for all completed slots
	missingDeps   map[base.TransactionID][]base.TransactionID
	parseErrors   int

	// Output bookkeeping
	writeBatch     map[base.TransactionID][]byte
	bytesWritten   int64
	recordsWritten int

	// Validation
	valTimesNs   []int64
	valTotalNs   int64
	valUTXOs     int64
	valSucceeded int
	valFailed    int
	valSkipped   int
	valFailures  []validationFailure

	// Slot-completion progress
	completedSlots         int
	progressEmitAt         int            // value of completedSlots at last progress emit
	progressVisitedAtEmit  categoryCounts // snapshot of visitedTotal at last emit
	progressSeenAtEmit     categoryCounts // snapshot of seenInDBTotal at last emit
	progressValTotalNsAtEm int64
	progressValTimesAtEm   int
	progressValUTXOsAtEm   int64
	progressLastSlot       uint32 // most recent slot for which we emitted
	progressFirstSlot      uint32 // earliest slot covered by the next-pending window
}

func runAuditCmd(_ *cobra.Command, args []string) {
	slotFrom64, err := strconv.ParseUint(args[0], 10, 32)
	glb.AssertNoError(err)
	glb.Assertf(slotFrom64 <= base.MaxSlot, "<slot from> out of range")
	slotFrom := uint32(slotFrom64)

	var floor uint32 = 0
	if len(args) >= 2 {
		sb, err := strconv.ParseUint(args[1], 10, 32)
		glb.AssertNoError(err)
		glb.Assertf(uint32(sb) <= slotFrom, "<slot back to> must be ≤ <slot from>")
		floor = uint32(sb)
	}

	// Source store always; multistate / ledger library only if --validate.
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()
	if auditValidate {
		glb.InitLedgerFromDB()
	}

	src, ok := glb.TxBytesStore().(*proxitxstore.SimpleTxBytesStore)
	glb.Assertf(ok, "txstore is not *SimpleTxBytesStore")

	// Optional output store. Refuse to overwrite an existing path.
	var dst *proxitxstore.SimpleTxBytesStore
	var dstDB *badger.DB
	if auditOutput != "" {
		if _, err := os.Stat(auditOutput); err == nil {
			glb.Fatalf("--output path %q already exists, refusing to overwrite", auditOutput)
		} else if !os.IsNotExist(err) {
			glb.Fatalf("cannot stat --output path %q: %v", auditOutput, err)
		}
		opts := badger.DefaultOptions(auditOutput)
		opts.BlockCacheSize = 64 << 20
		opts.IndexCacheSize = 32 << 20
		opts.NumCompactors = 2
		dstDB = badger_adaptor.MustCreateOrOpenBadgerDB(auditOutput, opts)
		defer func() { _ = dstDB.Close() }()
		dst = proxitxstore.NewSimpleTxBytesStore(badger_adaptor.New(dstDB))
		glb.Infof("output txstore: %s (preserve metadata = %v)", auditOutput, auditMeta)
	}

	// Phase 1: locate branches in <slot from>, with a fallback if empty.
	branches := findBranches(src, slotFrom)
	if len(branches) == 0 {
		alt, found := findEarlierSlotWithBranches(src, slotFrom, floor)
		if !found {
			glb.Fatalf("no branches found in slot %d nor in the previous %d slots", slotFrom, auditPhase1FallbackSearch)
		}
		glb.Infof("no branches in slot %d. Earlier slot with branches: %d", slotFrom, alt)
		if !glb.YesNoPrompt(fmt.Sprintf("use slot %d as <slot from>?", alt), false) {
			glb.Infof("aborted by user")
			return
		}
		slotFrom = alt
		branches = findBranches(src, slotFrom)
	}

	st := &auditState{
		src:                 src,
		dst:                 dst,
		floor:               floor,
		C:                   newFrontier(),
		visited:             newFrontier(),
		branchesInC:         make(map[uint32]int),
		startSlot:           slotFrom,
		branchesInStartSlot: len(branches),
		earliestReached:     slotFrom,
		latestReached:       slotFrom,
		missingDeps:         make(map[base.TransactionID][]base.TransactionID),
		writeBatch:          make(map[base.TransactionID][]byte, auditWriteBatchSize),
		progressFirstSlot:   slotFrom,
	}

	// Seed C with branches at <slot from>.
	for _, branchID := range branches {
		tx, ok := st.loadAndParse(branchID)
		if !ok {
			continue
		}
		st.addToC(tx)
	}

	glb.Infof("starting from slot %d (%d branches), floor = %d", slotFrom, len(branches), floor)

	// ---- Main frontier loop ----
	for {
		maxC, ok := st.C.MaxSlot()
		if !ok {
			break // C empty
		}
		if maxC < floor {
			break // remaining work is below the audit window
		}

		t, _ := st.C.AnyAtSlot(maxC)
		st.processOne(t)

		// Pruning is tied to slot-completion (see onBranchSlotComplete) — the
		// max-C-slot watermark is only useful at those boundaries. Doing it on
		// every iteration would be a no-op until the bucket actually empties.
	}

	st.flushOutput()

	// ---- Final report ----
	st.printFinalReport()
}

// loadAndParse fetches tx bytes from the source store and parses them. Uses
// the library-aware parser when --validate is set (Phase 3 will reuse the
// parsed object), otherwise the library-agnostic parser.
func (st *auditState) loadAndParse(txid base.TransactionID) (*transaction.Transaction, bool) {
	txBytesWithMeta := st.src.GetTxBytesWithMetadata(&txid)
	if len(txBytesWithMeta) == 0 {
		return nil, false
	}
	// txstore stores raw bytes (no metadata prefix; metadata-refactor §7).
	txBytes := txBytesWithMeta
	var tx *transaction.Transaction
	var err error
	if auditValidate {
		tx, err = transaction.Parse(txBytes)
	} else {
		tx, err = transaction.ParseLibraryAgnostic(txBytes)
	}
	if err != nil {
		st.parseErrors++
		return nil, false
	}
	return tx, true
}

// processOne is the body of the main loop: discover deps, optionally validate,
// move T from C to visited, write to output, and on branch-slot completion
// kick off stats / pruning.
func (st *auditState) processOne(t *transaction.Transaction) {
	tID := t.ID()
	tSlot := t.Timestamp().Slot
	tIsBranch := t.IsBranchTransaction()

	st.discoverDeps(t)

	if auditValidate {
		st.validateOne(t)
	}

	// Move T to visited.
	st.C.Remove(tID)
	st.visited.Add(t)
	st.visitedTotal.Add(tID)
	if tSlot < st.earliestReached {
		st.earliestReached = tSlot
	}

	if st.dst != nil {
		st.queueWrite(t)
	}

	if tIsBranch {
		st.branchesInC[tSlot]--
		if st.branchesInC[tSlot] <= 0 {
			delete(st.branchesInC, tSlot)
			st.onBranchSlotComplete(tSlot)
		}
	}
}

// discoverDeps loads every input/endorsement/baseline of t into C if it isn't
// already in C ∪ visited and is at slot ≥ floor. Missing deps (referenced but
// absent from the source store) are reported and accumulated.
func (st *auditState) discoverDeps(t *transaction.Transaction) {
	tID := t.ID()

	walk := func(depID base.TransactionID) {
		if depID.Slot() < st.floor {
			return
		}
		if st.C.Has(depID) || st.visited.Has(depID) {
			return
		}
		dep, ok := st.loadAndParse(depID)
		if !ok {
			st.recordMissing(depID, tID)
			return
		}
		st.addToC(dep)
	}

	for i := 0; i < t.NumInputs(); i++ {
		oid := t.MustInputAt(byte(i))
		walk(oid.TransactionID())
	}
	for i := 0; i < t.NumEndorsements(); i++ {
		walk(t.MustEndorsementAt(byte(i)))
	}
	if baseline, ok := t.ExplicitBaseline(); ok {
		walk(baseline)
	}
}

// addToC inserts tx into C and bumps the per-slot branch counter when applicable.
func (st *auditState) addToC(tx *transaction.Transaction) {
	if !st.C.Add(tx) {
		return
	}
	if tx.IsBranchTransaction() {
		st.branchesInC[tx.Timestamp().Slot]++
	}
}

func (st *auditState) recordMissing(missing, referrer base.TransactionID) {
	refs, exists := st.missingDeps[missing]
	if !exists {
		st.missingDeps[missing] = []base.TransactionID{referrer}
		// Emit a streaming line so completeness issues are visible while the
		// audit runs, not just in the final report.
		glb.Infof("MISSING: %s referenced by %s", missing.StringShort(), referrer.StringShort())
		return
	}
	if len(refs) < auditMaxMissingSamples {
		st.missingDeps[missing] = append(refs, referrer)
	}
}

// validateOne runs SetFullContext + ValidateFullContext on t. The loader
// resolves OutputIDs in priority order:
//
//  1. C (already-frontier producer)  — most common path
//  2. visited                        — recently processed producer
//  3. silent fresh load from source  — for producers below floor (not
//     traversed) or otherwise outside the audit window. The fetched tx is
//     NOT added to C or visited and is therefore not counted as visited;
//     it's just providing output bytes for this single validation.
//
// VAL SKIP fires only when even the lazy load fails (producer not in DB
// at all). That's a real completeness gap, not a normal floor edge effect.
func (st *auditState) validateOne(t *transaction.Transaction) {
	nUTXO := int64(t.NumInputs() + t.NumProducedOutputs())

	loader := func(oid base.OutputID) ([]byte, bool) {
		producerID := oid.TransactionID()
		if producer, ok := st.C.Get(producerID); ok {
			if int(oid.Index()) >= producer.NumProducedOutputs() {
				return nil, false
			}
			return producer.MustOutputDataAt(oid.Index()), true
		}
		if producer, ok := st.visited.Get(producerID); ok {
			if int(oid.Index()) >= producer.NumProducedOutputs() {
				return nil, false
			}
			return producer.MustOutputDataAt(oid.Index()), true
		}
		// Silent fresh load — producer is outside the audit window (below
		// floor, typically) but we still need its output bytes to validate
		// the consumer. Don't add to any set.
		txBytesWithMeta := st.src.GetTxBytesWithMetadata(&producerID)
		if len(txBytesWithMeta) == 0 {
			return nil, false
		}
		// txstore stores raw bytes (no metadata prefix; metadata-refactor §7).
		producer, err := transaction.Parse(txBytesWithMeta)
		if err != nil {
			return nil, false
		}
		if int(oid.Index()) >= producer.NumProducedOutputs() {
			return nil, false
		}
		return producer.MustOutputDataAt(oid.Index()), true
	}

	start := time.Now()
	if err := t.SetFullContextWithFetch(loader); err != nil {
		st.valSkipped++
		glb.Infof("VAL SKIP: %s — %v", t.IDShortString(), err)
		return
	}
	if err := t.ValidateFullContext(); err != nil {
		elapsed := time.Since(start).Nanoseconds()
		st.valTotalNs += elapsed
		st.valTimesNs = append(st.valTimesNs, elapsed)
		st.valUTXOs += nUTXO
		st.valFailed++
		st.valFailures = append(st.valFailures, validationFailure{txid: t.ID(), err: err.Error()})
		return
	}
	elapsed := time.Since(start).Nanoseconds()
	st.valTotalNs += elapsed
	st.valTimesNs = append(st.valTimesNs, elapsed)
	st.valUTXOs += nUTXO
	st.valSucceeded++
}

// onBranchSlotComplete fires when the last branch at `slot` has been moved
// from C to visited. Does the per-slot source-DB scan (key-only — category
// is encoded in txid bits, so no tx bytes are loaded), prunes `visited` past
// the new max-C-slot watermark, and emits a compact stats line every
// auditProgressInterval completions.
func (st *auditState) onBranchSlotComplete(slot uint32) {
	st.completedSlots++

	// Per-slot in-DB category counts via key-only iteration.
	var inSlot categoryCounts
	st.src.Iterator(base.Slot2Bytes(slot)).IterateKeys(func(k []byte) bool {
		txid, err := base.TransactionIDFromBytes(k)
		if err != nil {
			return true
		}
		inSlot.Add(txid)
		return true
	})
	st.seenInDBTotal = st.seenInDBTotal.AddCC(inSlot)

	if slot < st.earliestReached {
		st.earliestReached = slot
	}

	// Prune `visited`: nothing newer than the current max-C-slot can ever be
	// referenced again as a future input producer.
	if maxC, ok := st.C.MaxSlot(); ok {
		st.visited.DeleteWhereSlotGreaterThan(maxC)
	} else {
		// C is empty → nothing left to reference any visited entry.
		st.visited = newFrontier()
	}

	// Compressed periodic progress emit.
	if st.completedSlots-st.progressEmitAt >= auditProgressInterval {
		st.emitProgress(slot)
	}
}

func (st *auditState) emitProgress(currentSlot uint32) {
	visitedDelta := st.visitedTotal.Sub(st.progressVisitedAtEmit)
	seenDelta := st.seenInDBTotal.Sub(st.progressSeenAtEmit)
	orphansDelta := seenDelta.Sub(visitedDelta)

	rangeFrom, rangeTo := currentSlot, st.progressFirstSlot
	if rangeFrom > rangeTo {
		rangeFrom, rangeTo = rangeTo, rangeFrom
	}

	line := fmt.Sprintf("slots %d..%d (Δ=%d): visited %s (br/seq/ns %s/%s/%s), in-DB %s, orphans %s, |C|=%d |V|=%d",
		rangeFrom, rangeTo,
		st.completedSlots-st.progressEmitAt,
		util.Th(visitedDelta.Total()),
		util.Th(visitedDelta.Branch), util.Th(visitedDelta.SeqNonBranch), util.Th(visitedDelta.NonSeq),
		util.Th(seenDelta.Total()), util.Th(orphansDelta.Total()),
		st.C.Len(), st.visited.Len())

	if auditValidate {
		valTimesDelta := len(st.valTimesNs) - st.progressValTimesAtEm
		valTotalNsDelta := st.valTotalNs - st.progressValTotalNsAtEm
		valUTXODelta := st.valUTXOs - st.progressValUTXOsAtEm
		if valTimesDelta > 0 && valTotalNsDelta > 0 {
			meanMs := float64(valTotalNsDelta) / float64(valTimesDelta) / 1e6
			perUTXOMs := 0.0
			if valUTXODelta > 0 {
				perUTXOMs = float64(valTotalNsDelta) / float64(valUTXODelta) / 1e6
			}
			line += fmt.Sprintf(" | val %.4f ms/tx %.4f ms/UTXO", meanMs, perUTXOMs)
		}
	}

	glb.Infof("%s", line)

	// Snapshot for next window.
	st.progressEmitAt = st.completedSlots
	st.progressVisitedAtEmit = st.visitedTotal
	st.progressSeenAtEmit = st.seenInDBTotal
	st.progressValTotalNsAtEm = st.valTotalNs
	st.progressValTimesAtEm = len(st.valTimesNs)
	st.progressValUTXOsAtEm = st.valUTXOs
	st.progressLastSlot = currentSlot
	st.progressFirstSlot = currentSlot
}

func (st *auditState) queueWrite(t *transaction.Transaction) {
	id := t.ID()
	// txstore stores raw bytes (no metadata prefix; metadata-refactor §7).
	value := t.Bytes()
	st.writeBatch[id] = value
	st.bytesWritten += int64(len(value))
	st.recordsWritten++
	if len(st.writeBatch) >= auditWriteBatchSize {
		st.flushOutput()
	}
}

func (st *auditState) flushOutput() {
	if st.dst == nil || len(st.writeBatch) == 0 {
		return
	}
	err := st.dst.PersistTxBytesBatch(st.writeBatch)
	glb.AssertNoError(err)
	st.writeBatch = make(map[base.TransactionID][]byte, auditWriteBatchSize)
}

func (st *auditState) printFinalReport() {
	ln := lines.New("  ")
	ln.Add("Past cone of slot %d (%d branches), floor = %d:", st.startSlot, st.branchesInStartSlot, st.floor)
	ln.Add("visited transactions   : %s", util.Th(st.visitedTotal.Total()))
	ln.Add("    branch             : %s", util.Th(st.visitedTotal.Branch))
	ln.Add("    seq non-branch     : %s", util.Th(st.visitedTotal.SeqNonBranch))
	ln.Add("    non-sequencer      : %s", util.Th(st.visitedTotal.NonSeq))
	ln.Add("earliest reached slot  : %d", st.earliestReached)
	ln.Add("latest reached slot    : %d", st.latestReached)
	ln.Add("completed slots        : %s", util.Th(st.completedSlots))
	ln.Add("missing dependencies   : %s referrers (unique missing: %s)", util.Th(referrerCount(st.missingDeps)), util.Th(len(st.missingDeps)))
	ln.Add("parse errors           : %s", util.Th(st.parseErrors))

	// Orphans in traversed slots — derived from accumulators.
	orphans := st.seenInDBTotal.Sub(st.visitedTotal)
	ln.Add("")
	ln.Add("In-DB across traversed slots:")
	ln.Add("total                  : %s", util.Th(st.seenInDBTotal.Total()))
	ln.Add("branch                 : %s", util.Th(st.seenInDBTotal.Branch))
	ln.Add("seq non-branch         : %s", util.Th(st.seenInDBTotal.SeqNonBranch))
	ln.Add("non-sequencer          : %s", util.Th(st.seenInDBTotal.NonSeq))
	ln.Add("")
	ln.Add("Orphans (in-DB minus visited):")
	rate := 0.0
	if st.seenInDBTotal.Total() > 0 {
		rate = 100 * float64(orphans.Total()) / float64(st.seenInDBTotal.Total())
	}
	ln.Add("total                  : %s   (%.2f%% of in-DB)", util.Th(orphans.Total()), rate)
	ln.Add("branch                 : %s", util.Th(orphans.Branch))
	ln.Add("seq non-branch         : %s", util.Th(orphans.SeqNonBranch))
	ln.Add("non-sequencer          : %s", util.Th(orphans.NonSeq))

	if auditOutput != "" {
		ln.Add("")
		ln.Add("Output:")
		ln.Add("written to             : %s", auditOutput)
		ln.Add("metadata               : %s", auditMetaLabel())
		ln.Add("records written        : %s", util.Th(st.recordsWritten))
		ln.Add("bytes written          : %s", util.Th(st.bytesWritten))
	}

	if auditValidate {
		ln.Add("")
		ln.Add("Validation:")
		ln.Add("attempted              : %s", util.Th(st.valSucceeded+st.valFailed+st.valSkipped))
		ln.Add("succeeded              : %s", util.Th(st.valSucceeded))
		ln.Add("failed                 : %s", util.Th(st.valFailed))
		ln.Add("skipped (missing inp.) : %s", util.Th(st.valSkipped))
		if len(st.valTimesNs) > 0 {
			meanNs := st.valTotalNs / int64(len(st.valTimesNs))
			p95Ns := percentileNs(st.valTimesNs, 0.95)
			var maxNs int64
			for _, v := range st.valTimesNs {
				if v > maxNs {
					maxNs = v
				}
			}
			meanPerUTXONs := int64(0)
			if st.valUTXOs > 0 {
				meanPerUTXONs = st.valTotalNs / st.valUTXOs
			}
			tps := float64(len(st.valTimesNs)) / (float64(st.valTotalNs) / 1e9)
			utxoPS := float64(st.valUTXOs) / (float64(st.valTotalNs) / 1e9)
			ln.Add("total time             : %s", time.Duration(st.valTotalNs))
			ln.Add("mean per tx            : %.4f ms", float64(meanNs)/1e6)
			ln.Add("p95 per tx             : %.4f ms", float64(p95Ns)/1e6)
			ln.Add("max per tx             : %.4f ms", float64(maxNs)/1e6)
			ln.Add("mean per consumed+produced UTXO : %.4f ms", float64(meanPerUTXONs)/1e6)
			ln.Add("throughput             : %.0f tx/s, %.0f UTXO/s", tps, utxoPS)
		}
	}

	if len(st.missingDeps) > 0 {
		ln.Add("")
		ln.Add("Missing dependencies (%d unique, sample):", len(st.missingDeps))
		i := 0
		for missing, referrers := range st.missingDeps {
			if i >= 20 {
				ln.Add("... and %d more (truncated)", len(st.missingDeps)-i)
				break
			}
			refStr := ""
			if len(referrers) > 0 {
				refStr = " ← " + referrers[0].StringShort()
				if len(referrers) > 1 {
					refStr += fmt.Sprintf(" (+%d more)", len(referrers)-1)
				}
			}
			ln.Add("%s%s", missing.StringShort(), refStr)
			i++
		}
	}

	if len(st.valFailures) > 0 {
		ln.Add("")
		ln.Add("Validation failures (%d):", len(st.valFailures))
		for i, f := range st.valFailures {
			if i >= auditMaxFailureSamples {
				ln.Add("... and %d more (truncated)", len(st.valFailures)-i)
				break
			}
			ln.Add("%s : %s", f.txid.StringShort(), f.err)
		}
	}

	glb.Infof("%s", ln.String())
}

// findBranches returns the txids of branch transactions in `slot`.
func findBranches(src txStoreIter, slot uint32) []base.TransactionID {
	var ret []base.TransactionID
	src.Iterator(base.Slot2Bytes(slot)).IterateKeys(func(k []byte) bool {
		txid, err := base.TransactionIDFromBytes(k)
		if err != nil {
			return true
		}
		if txid.IsBranchTransaction() {
			ret = append(ret, txid)
		}
		return true
	})
	return ret
}

// findEarlierSlotWithBranches scans `slot-1` down to `slot - auditPhase1FallbackSearch`
// (or `floor`, whichever is larger) for the first slot containing any branch.
func findEarlierSlotWithBranches(src txStoreIter, from, floor uint32) (uint32, bool) {
	low := uint32(0)
	if from > auditPhase1FallbackSearch {
		low = from - auditPhase1FallbackSearch
	}
	if low < floor {
		low = floor
	}
	for s := from - 1; s >= low; s-- {
		if len(findBranches(src, s)) > 0 {
			return s, true
		}
		if s == 0 {
			break
		}
	}
	return 0, false
}

func referrerCount(m map[base.TransactionID][]base.TransactionID) int {
	n := 0
	for _, refs := range m {
		n += len(refs)
	}
	return n
}

func auditMetaLabel() string {
	if auditMeta {
		return "preserved (--meta)"
	}
	return "stripped (default)"
}

func percentileNs(samples []int64, p float64) int64 {
	if len(samples) == 0 {
		return 0
	}
	cp := make([]int64, len(samples))
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * p)
	return cp[idx]
}
