package txstore

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/dgraph-io/badger/v4"
	lru "github.com/hashicorp/golang-lru"
	"github.com/lunfardo314/proxima/core/txmetadata"
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
	auditDefaultLRUSize       = 50_000
	auditWriteBatchSize       = 1_000
	auditProgressInterval     = 50_000
	auditPhase1FallbackSearch = 1_024 // slots to scan downward when <slot from> has no branches
	auditMaxMissingSamples    = 5     // max referrers recorded per missing dep
)

func initAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit <slot from> [<slot back to, default 0>]",
		Short: "walk past cone of all branches in <slot from>, audit completeness, and optionally validate or copy to a new txstore",
		Args:  cobra.RangeArgs(1, 2),
		Run:   runAuditCmd,
	}
	// no shorthand for --validate: -v is reserved for the global --verbose flag.
	cmd.PersistentFlags().BoolVar(&auditValidate, "validate", false, "run full-context validation on every visited transaction (requires multistate DB)")
	cmd.PersistentFlags().StringVarP(&auditOutput, "output", "o", "", "write visited transactions to a new txstore at this path (refuses if path exists)")
	cmd.PersistentFlags().BoolVarP(&auditMeta, "meta", "m", false, "preserve per-transaction metadata in --output (default: write empty metadata)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// txStoreIter is the read surface the audit needs from the source store: id-keyed
// fetch plus prefix iteration. Satisfied by *txstore.SimpleTxBytesStore. Declared
// locally so the audit logic doesn't need to know about the underlying KV adapter.
type txStoreIter interface {
	global.TxBytesGet
	Iterator(prefix []byte) common.KVIterator
}

type auditStats struct {
	floor               uint32
	startSlot           uint32
	branchesInStartSlot int
	visitedTotal        int
	earliestReached     uint32
	latestReached       uint32
	parseErrors         int
	missingDeps         map[base.TransactionID][]base.TransactionID
	missingDepsTotal    int

	// output
	recordsWritten int
	bytesWritten   int64

	// validation
	valAttempted int
	valSucceeded int
	valFailed    int
	valSkipped   int
	valTotalNs   int64
	valTimes     []int64
	valFailures  []validationFailure
	valUTXOs     int64

	// orphans
	orphanBranch       int
	orphanSeqNonBranch int
	orphanNonSeq       int
	orphanTotal        int
	sourceTotalKeys    int
}

type validationFailure struct {
	txid base.TransactionID
	err  string
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

	// Source txstore is always opened. Multistate is opened only if -v.
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()
	if auditValidate {
		glb.InitLedgerFromDB() // populates ledger.L(slot) cache from multistate
	}

	src, ok := glb.TxBytesStore().(*proxitxstore.SimpleTxBytesStore)
	glb.Assertf(ok, "txstore is not *SimpleTxBytesStore")

	// Optional: open output DB. Refuse if path already exists.
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

	st := &auditStats{
		floor:           floor,
		startSlot:       slotFrom,
		earliestReached: slotFrom,
		latestReached:   floor,
		missingDeps:     make(map[base.TransactionID][]base.TransactionID),
	}

	// ---- Phase 1: find branches in <slot from>
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
		st.startSlot = slotFrom
		st.earliestReached = slotFrom
		branches = findBranches(src, slotFrom)
	}
	st.branchesInStartSlot = len(branches)
	glb.Infof("starting from slot %d (%d branches), floor = %d", slotFrom, len(branches), floor)

	// ---- Phase 2: BFS past-cone traversal
	visited := traverse(src, dst, branches, floor, st)

	// ---- Phase 3: validation (only if -v)
	if auditValidate {
		validateAll(src, visited, st)
	}

	// ---- Phase 4: orphan stats
	collectOrphans(src, visited, floor, st)

	// ---- Phase 5: report
	printReport(st)
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

// findEarlierSlotWithBranches scans slots `from-1` down to `from-auditPhase1FallbackSearch`
// (or `floor`, whichever is larger) and returns the first one containing ≥1 branch.
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

// traverse performs the BFS past-cone walk from `seeds`. Skips links below
// `floor`. If dst != nil, every visited tx is also written to it (respecting
// auditMeta). Returns the visited set.
func traverse(src txStoreIter, dst *proxitxstore.SimpleTxBytesStore, seeds []base.TransactionID, floor uint32, st *auditStats) map[base.TransactionID]struct{} {
	visited := make(map[base.TransactionID]struct{}, 1024)
	queue := make([]base.TransactionID, 0, 1024)
	queue = append(queue, seeds...)

	writeBatch := make(map[base.TransactionID][]byte, auditWriteBatchSize)
	flushBatch := func() {
		if dst == nil || len(writeBatch) == 0 {
			return
		}
		err := dst.PersistTxBytesBatch(writeBatch)
		glb.AssertNoError(err)
		writeBatch = make(map[base.TransactionID][]byte, auditWriteBatchSize)
	}

	for len(queue) > 0 {
		txid := queue[len(queue)-1]
		queue = queue[:len(queue)-1]

		if txid.Slot() < floor {
			continue // out of audit window — not a missing dep, just out of scope
		}
		if _, seen := visited[txid]; seen {
			continue
		}

		txBytesWithMeta := src.GetTxBytesWithMetadata(&txid)
		if len(txBytesWithMeta) == 0 {
			recordMissing(st, txid, base.TransactionID{}) // referrer unknown at this granularity
			continue
		}

		visited[txid] = struct{}{}
		st.visitedTotal++
		if txid.Slot() < st.earliestReached {
			st.earliestReached = txid.Slot()
		}
		if txid.Slot() > st.latestReached {
			st.latestReached = txid.Slot()
		}

		// Library-agnostic parse: enough to extract inputs / endorsements / baseline.
		// Phase 3 will re-Parse with the library if --validate is set.
		txBytes, _, err := txmetadata.ParseTxMetadata(txBytesWithMeta)
		if err != nil {
			st.parseErrors++
			continue
		}
		tx, err := transaction.ParseLibraryAgnostic(txBytes)
		if err != nil {
			st.parseErrors++
			continue
		}

		// Output
		if dst != nil {
			var value []byte
			if auditMeta {
				value = txBytesWithMeta
			} else {
				value = append((*txmetadata.TransactionMetadata)(nil).Bytes(), txBytes...)
			}
			writeBatch[txid] = value
			st.recordsWritten++
			st.bytesWritten += int64(len(value))
			if len(writeBatch) >= auditWriteBatchSize {
				flushBatch()
			}
		}

		// Enqueue dependencies
		for i := 0; i < tx.NumInputs(); i++ {
			oid := tx.MustInputAt(byte(i))
			depTxid := oid.TransactionID()
			if depTxid.Slot() >= floor {
				queue = append(queue, depTxid)
			}
		}
		for i := 0; i < tx.NumEndorsements(); i++ {
			endID := tx.MustEndorsementAt(byte(i))
			if endID.Slot() >= floor {
				queue = append(queue, endID)
			}
		}
		if baseline, ok := tx.ExplicitBaseline(); ok && baseline.Slot() >= floor {
			queue = append(queue, baseline)
		}

		if st.visitedTotal%auditProgressInterval == 0 {
			glb.Infof("  ... visited %s, queue %d, slot %d", util.Th(st.visitedTotal), len(queue), txid.Slot())
		}
	}
	flushBatch()
	return visited
}

func recordMissing(st *auditStats, missing, referrer base.TransactionID) {
	st.missingDepsTotal++
	refs, exists := st.missingDeps[missing]
	if !exists {
		st.missingDeps[missing] = []base.TransactionID{}
		refs = st.missingDeps[missing]
	}
	if (referrer != base.TransactionID{}) && len(refs) < auditMaxMissingSamples {
		st.missingDeps[missing] = append(refs, referrer)
	}
}

// validateAll re-parses each visited tx with the library, runs full-context
// validation, and records timing stats. Producer-tx lookups for the input
// loader are served from a parsed-tx LRU to avoid re-parsing producers many
// times (once per consumer).
func validateAll(src txStoreIter, visited map[base.TransactionID]struct{}, st *auditStats) {
	cache, err := lru.New(auditDefaultLRUSize)
	glb.AssertNoError(err)

	loadTx := func(txid base.TransactionID) *transaction.Transaction {
		if v, ok := cache.Get(txid); ok {
			return v.(*transaction.Transaction)
		}
		txBytesWithMeta := src.GetTxBytesWithMetadata(&txid)
		if len(txBytesWithMeta) == 0 {
			return nil
		}
		txBytes, _, err := txmetadata.ParseTxMetadata(txBytesWithMeta)
		if err != nil {
			return nil
		}
		tx, err := transaction.Parse(txBytes)
		if err != nil {
			return nil
		}
		cache.Add(txid, tx)
		return tx
	}

	// Sort visited by (slot asc, tick asc) so the LRU stays warm for
	// descendant lookups: producers are validated before their consumers
	// and remain in cache while still warm.
	ordered := make([]base.TransactionID, 0, len(visited))
	for txid := range visited {
		ordered = append(ordered, txid)
	}
	sort.Slice(ordered, func(i, j int) bool {
		ti, tj := ordered[i].Timestamp(), ordered[j].Timestamp()
		if ti.Slot != tj.Slot {
			return ti.Slot < tj.Slot
		}
		return ti.Tick < tj.Tick
	})

	st.valTimes = make([]int64, 0, len(ordered))

	for n, txid := range ordered {
		st.valAttempted++

		tx := loadTx(txid)
		if tx == nil {
			st.valSkipped++
			continue
		}
		nUTXO := int64(tx.NumInputs() + tx.NumProducedOutputs())

		start := time.Now()
		err := tx.SetFullContextWithFetch(func(oid base.OutputID) ([]byte, bool) {
			producer := loadTx(oid.TransactionID())
			if producer == nil {
				return nil, false
			}
			if int(oid.Index()) >= producer.NumProducedOutputs() {
				return nil, false
			}
			return producer.MustOutputDataAt(oid.Index()), true
		})
		if err != nil {
			st.valSkipped++ // missing dep — already counted in Phase 2 missing list
			continue
		}
		if err := tx.ValidateFullContext(); err != nil {
			elapsed := time.Since(start).Nanoseconds()
			st.valTotalNs += elapsed
			st.valTimes = append(st.valTimes, elapsed)
			st.valUTXOs += nUTXO
			st.valFailed++
			st.valFailures = append(st.valFailures, validationFailure{txid: txid, err: err.Error()})
			continue
		}
		elapsed := time.Since(start).Nanoseconds()
		st.valTotalNs += elapsed
		st.valTimes = append(st.valTimes, elapsed)
		st.valUTXOs += nUTXO
		st.valSucceeded++

		if (n+1)%auditProgressInterval == 0 {
			glb.Infof("  validated %s / %s (slot %d)", util.Th(n+1), util.Th(len(ordered)), txid.Slot())
		}
	}
}

func collectOrphans(src txStoreIter, visited map[base.TransactionID]struct{}, floor uint32, st *auditStats) {
	src.Iterator(nil).IterateKeys(func(k []byte) bool {
		txid, err := base.TransactionIDFromBytes(k)
		if err != nil {
			return true
		}
		st.sourceTotalKeys++
		if txid.Slot() < floor {
			return true
		}
		if _, seen := visited[txid]; seen {
			return true
		}
		switch {
		case txid.IsBranchTransaction():
			st.orphanBranch++
		case txid.IsSequencerTransaction():
			st.orphanSeqNonBranch++
		default:
			st.orphanNonSeq++
		}
		st.orphanTotal++
		return true
	})
}

func printReport(st *auditStats) {
	ln := lines.New("  ")
	ln.Add("Past cone of slot %d (%d branches), floor = %d:", st.startSlot, st.branchesInStartSlot, st.floor)
	ln.Add("visited transactions   : %s", util.Th(st.visitedTotal))
	ln.Add("earliest reached slot  : %d", st.earliestReached)
	ln.Add("latest reached slot    : %d", st.latestReached)
	ln.Add("missing dependencies   : %s (unique: %s)", util.Th(st.missingDepsTotal), util.Th(len(st.missingDeps)))
	ln.Add("parse errors           : %s", util.Th(st.parseErrors))

	ln.Add("")
	ln.Add("Orphans in source (slots ≥ %d, not reachable from start):", st.floor)
	ln.Add("branch                 : %s", util.Th(st.orphanBranch))
	ln.Add("sequencer non-branch   : %s", util.Th(st.orphanSeqNonBranch))
	ln.Add("non-sequencer          : %s", util.Th(st.orphanNonSeq))
	ln.Add("total                  : %s   (source has %s keys)", util.Th(st.orphanTotal), util.Th(st.sourceTotalKeys))

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
		ln.Add("attempted              : %s", util.Th(st.valAttempted))
		ln.Add("succeeded              : %s", util.Th(st.valSucceeded))
		ln.Add("failed                 : %s", util.Th(st.valFailed))
		ln.Add("skipped (missing deps) : %s", util.Th(st.valSkipped))
		if len(st.valTimes) > 0 {
			meanNs := st.valTotalNs / int64(len(st.valTimes))
			p95Ns := percentileNs(st.valTimes, 0.95)
			maxNs := int64(0)
			for _, v := range st.valTimes {
				if v > maxNs {
					maxNs = v
				}
			}
			meanPerUTXONs := int64(0)
			if st.valUTXOs > 0 {
				meanPerUTXONs = st.valTotalNs / st.valUTXOs
			}
			tps := float64(len(st.valTimes)) / (float64(st.valTotalNs) / 1e9)
			utxoPS := float64(st.valUTXOs) / (float64(st.valTotalNs) / 1e9)
			// ms-denominated stats: validation per-tx is typically tens of µs,
			// so we show 4 decimal places to keep sub-millisecond resolution
			// readable.
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
		ln.Add("Missing dependencies (%d unique):", len(st.missingDeps))
		i := 0
		for missing, referrers := range st.missingDeps {
			if i >= 20 {
				ln.Add("... and %d more (truncated)", len(st.missingDeps)-i)
				break
			}
			refStr := ""
			if len(referrers) > 0 {
				refStr = " referenced by " + referrers[0].StringShort()
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
			if i >= 20 {
				ln.Add("... and %d more (truncated)", len(st.valFailures)-i)
				break
			}
			ln.Add("%s : %s", f.txid.StringShort(), f.err)
		}
	}

	glb.Infof("%s", ln.String())
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
