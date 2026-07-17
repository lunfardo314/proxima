package tests

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// TestBranchConservationUnderGCLoad tries to reproduce, in-process and under the race detector,
// the branch mutation-set non-conservation observed on the dominating testnet sequencer
// (created != deleted + slotInflation). The suspected mechanism is a concurrency race: the
// memDAG GC's ConvertToDetached mutates vertices while branch attachers build and clean
// overlapping past cones, so Mutations() sees an incomplete DEL set
// (claude/pastcone_consistency.md §4.3, §5.4).
//
// The ingredient every other multi-sequencer test misses is memDAG GC: they all pass
// OptionDisableMemDAGGC (startPruner=false), so the concurrent detach path is never exercised
// under load. This test enables GC (startPruner=true) and drives several sequencers producing
// competing branches every slot under heavy tag-along spam, long enough for GC's wall-clock/
// ledger TTL to fire repeatedly against live past cones.
//
// Signal: a non-conservation trips the commitBranch guard, which calls GracefulShutdown ->
// Stop(), setting env.IsShuttingDown(). We poll it and fail loudly with the logged reason
// (grep the captured log for "mutation set not conserved", which reports the imbalance and
// dumps the branch's mutation set). Run under -race:
//
//	go test -race -run TestBranchConservationUnderGCLoad ./tests/ -timeout 45m -v
func TestBranchConservationUnderGCLoad(t *testing.T) {
	runBranchConservationStress(t, false)
}

// TestBranchConservationUnderAggressiveGC is the escalation: it shrinks the memDAG size cap and
// TTLs (memdag.SetGCTuningForTesting) so the size-backstop FORCE-detach path fires in-process.
// ConvertToDetachedForced bypasses the active-attacher guard — it detaches a vertex proven idle
// only by wall-clock TTL, not by attachment state — which is the remaining un-exercised suspect
// after the guarded path came back clean over ~8000 branches. On the testnet the dominating node
// reaches this path by growing past the 50000-vertex cap under sustained spam; in-process the
// memDAG stays at a few hundred vertices, so we lower the cap to reach it.
func TestBranchConservationUnderAggressiveGC(t *testing.T) {
	runBranchConservationStress(t, true)
}

func runBranchConservationStress(t *testing.T, aggressiveGC bool) {
	if testing.Short() {
		t.Skip("stress/repro test; skipped in -short")
	}
	// GC tuning must be set BEFORE workflow.Start (inside initMultiSequencerTest) so the GC loop
	// never reads these package vars concurrently with the write — that would be a spurious race on
	// the tuning vars themselves, not the bug under investigation. Restored after the run stops.
	if aggressiveGC {
		// cap 600 vertices with 5 sequencers forces overCap continuously; wall TTL 4 / ledger TTL 2
		// slots make almost every non-tip vertex a force-detach victim while live. The cap is scaled
		// to the per-slot working set: at sequencer pace 3 the milestone/past-cone volume is ~4x the
		// pace-12 rate, so the historical cap of 150 could no longer make progress under the size
		// backstop. A larger realistic cap (the non-aggressive GCLoad variant) already passes.
		restore := memdag.SetGCTuningForTesting(600, 4, 2)
		defer restore()
	}

	const (
		nSequencers = 4 // in addition to bootstrap => 5 sequencers competing for branches every slot
		// effectively unbounded: the spamming timeout drives shutdown, not the slot budget
		maxSlots    = 1_000_000
		batchSize   = 20
		sendAmount  = 100_000_000
		pace        = 5    // ticks between chained transfers and between spam batches (*batchSize)
		startPruner = true // memDAG GC ENABLED — the ingredient the other multi-seq tests disable
	)
	// run duration overridable via PROXIMA_CONSERV_SECS (default 120s) — short value for a wiring smoke.
	spammingTimeout := 120 * time.Second
	if s := os.Getenv("PROXIMA_CONSERV_SECS"); s != "" {
		n, err := strconv.Atoi(s)
		require.NoError(t, err)
		spammingTimeout = time.Duration(n) * time.Second
	}
	testData := initMultiSequencerTest(t, nSequencers, startPruner)

	rdr := multistate.MakeSugared(testData.wrk.HeaviestStateForLatestTimeSlot())
	require.EqualValues(t, initBalance*nSequencers, int(rdr.BalanceOf(testData.addrAux.ControllerID())))

	targetPrivKey := testutil.GetTestingPrivateKey(10000)
	targetAddr := ledger.SigLockFromED25519PrivateKey(targetPrivKey)

	// tag-along to ALL sequencers (round-robin) so every slot has competing branches over
	// overlapping past cones — the multi-sequencer cone shape the crash arithmetic pointed at.
	tagAlongSeqIDs := []base.ChainID{testData.bootstrapChainID}
	for _, o := range testData.chainOrigins {
		tagAlongSeqIDs = append(tagAlongSeqIDs, o.ChainID)
	}

	ctx, cancelSpam := context.WithTimeout(context.Background(), spammingTimeout)
	defer cancelSpam()
	par := &spammerParams{
		t:             t,
		privateKey:    testData.privKeyFaucet,
		remainder:     testData.faucetOutput,
		tagAlongSeqID: tagAlongSeqIDs,
		target:        targetAddr,
		pace:          pace,
		batchSize:     batchSize,
		sendAmount:    sendAmount,
		tagAlongFee:   tagAlongFee,
		spammedTxIDs:  make([]base.TransactionID, 0),
	}
	go testData.spamTransfers(par, ctx)
	testData.startSequencersWithTimeout(maxSlots, spammingTimeout+30*time.Second)

	// Poll for the conservation guard firing. GracefulShutdown() -> Stop() flips IsShuttingDown;
	// catch it promptly and fail with the reason rather than waiting out the whole timeout.
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick.C:
			if testData.env.IsShuttingDown() {
				t.Fatal("node shut down during the run — branch mutation-set non-conservation reproduced " +
					"(see the log above for \"mutation set not conserved\")")
			}
		}
	}

	// drain in-flight attachments, then confirm the node never tripped a fatal invariant
	time.Sleep(5 * time.Second)
	require.False(t, testData.env.IsShuttingDown(),
		"node shut down during the run — a fatal invariant (likely branch non-conservation) fired; see log")

	testData.stopAndWait(3 * time.Second)
	t.Logf("%s", testData.wrk.Info())
	t.Logf("spammed %d batches", par.numSpammedBatches)
}
