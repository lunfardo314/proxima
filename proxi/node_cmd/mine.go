package node_cmd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	mathrand "math/rand"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/blake2b"
)

// `proxi node mine` is the fair-launch mining tool (see claude/launch_rationale.md).
// It repeatedly consumes the single mine chain UTXO, builds a valid transition
// (successor mine output + sig-locked payout + tag-along), searches a
// proof-of-signing-work nonce so the whole signed tx hashes to >= K(M) trailing
// zero bits, and submits it.
//
// The mine tx layout is constant across nonce attempts; only the nonce (in the
// open lock's unlock params) and the resulting signature change. So each target
// is compiled ONCE into two byte templates whose placeholder offsets are
// recorded (essence -> txID, full tx -> PoW hash); the hot loop only patches
// those ranges. Because the PoW hash covers the signature, every attempt costs
// one ed25519 sign — the work is CPU-egalitarian (pool/ASIC-hostile).
//
// SPECULATIVE MINING ON A TREE. Waiting for a submitted transit to become
// LRB-confirmed before starting the next one wastes most of the miner's time:
// confirmation takes many slots, mining one transit takes about one pace. So
// the miner does not wait — it extends the best branch it knows immediately.
//
// That branch comes from a tree of verified transits (mine_tree.go), fed by the
// node's mining transaction stream (mine_stream.go) and re-rooted by the LRB
// monitor. Every transit entering the tree — including this miner's own — is
// verified from its raw bytes against its predecessor (mine_verify.go), because
// the stream relays transits the node has not constraint-validated.
//
// The tree exists to make mining fair. Extending one's own unconfirmed transit
// is the right strategy, but if the only way to learn that someone else won a
// height is LRB confirmation, the producer of a transit is ahead of everyone
// else for longer than it takes to mine one — so whoever wins once wins
// forever. The stream collapses that lead to a gossip hop, and the tie-break
// (most proof of work, never first-seen) makes sure nothing prefers a transit
// merely for being ours. See claude/mining_tx_streaming.md.
//
// Everything the miner does against the node is retried: it is a long-running
// process and must survive node restarts, API timeouts and transient HTTP
// failures instead of aborting on the first error.
//
// After each mine tx becomes LRB-confirmed the miner optionally acts on the
// accumulated payouts, per --mode:
//   - consolidate (default): sweep all payout UTXOs into one sigLock output
//   - delegate: every C confirmed transits, delegate the accumulated balance
//     to a random alive sequencer
//   - stash: leave payouts untouched
//
// The follow-up consolidation/delegation tx is fire-and-forget and runs on the
// monitor goroutine, so it never costs the mining loop any time.

const (
	// defaultMaxDelegations is the advisory cap on how many delegations one
	// wallet keeps. It bounds permanent state, a cost every node carries while
	// the holder's own economics push the other way, so it cannot be derived
	// from what the miner sees. See claude/delegation_add_tokens.md.
	defaultMaxDelegations = 10

	// defaultDelegatePer is how many confirmed transits accumulate before the
	// miner acts on the payouts. Acting on every transit puts one transit's
	// payout into a delegation of its own: the target freezes it immediately and
	// for the full span, so it can never be topped up and the next transit has to
	// create yet another one - the cap is reached in ten transits and everything
	// after that is askstop churn. Accumulating first makes each delegation worth
	// its permanent state.
	defaultDelegatePer = 10

	modeConsolidate = "consolidate"
	modeDelegate    = "delegate"
	modeStash       = "stash"

	// required inflation cut (promille) for delegations the miner creates.
	mineDelegationCut = uint16(900)

	// how often the confirmation monitor polls the LRB mine chain tip.
	mineMonitorPeriod = 2 * time.Second

	// The node refuses transactions stamped more than a few slots ahead of its
	// wall clock and holds anything still in the future until the clock catches
	// up. Speculative mining stamps the successor a target pace above a
	// predecessor which may itself be unconfirmed and future-stamped, so the
	// miner keeps its own margin below that bound: a solved transaction is held
	// back until its slot is within mineMaxFutureSlots of the current slot.
	mineMaxFutureSlots = 4

	// Floor for the confirmation-stall timeout: if none of this miner's submitted
	// transits confirms for at least this long (and difficulty is low), the
	// speculative branch is presumed lost (dropped tag-along, node restart,
	// partition) and the miner re-anchors even though no competing transit is
	// visible. At higher difficulty the timeout scales up (see stallTimeout), so a
	// legitimately slow high-K transit is not abandoned mid-solve.
	mineConfirmationStall = 90 * time.Second
	// The stall timeout is at least this many expected solve-times (2^K/hashrate),
	// capped at mineStallMax. Generous: the pace-relieved difficulty makes a wedge
	// impossible (K falls with the gap), so a long stall only delays detecting a
	// genuinely dropped tx.
	mineStallSolveFactor = 3.0
	mineStallMax         = 10 * time.Minute

	// bounds of the exponential backoff between retries of a node call.
	mineRetryBase = 500 * time.Millisecond
	mineRetryMax  = 15 * time.Second
)

// mineStats accumulates run-wide totals for the periodic totals line.
// Guarded by miner.mu: the mining loop bumps the mined/attempt counters, the
// monitor goroutine bumps the confirmation-driven ones.
type mineStats struct {
	start          time.Time
	mined          int    // transits solved and submitted
	transits       int    // own transits seen confirmed in the LRB
	orphaned       int    // own transits dropped when a competing transit confirmed
	minted         uint64 // A * transits
	attempts       uint64 // cumulative PoW attempts across all transits
	consolidations int
	delegations    int
}

func initMineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mine",
		Short: "mine the fair-launch mine chain: build, solve and submit mine transitions in a loop",
		Args:  cobra.NoArgs,
		Run:   runMineCmd,
	}
	cmd.Flags().Int("workers", runtime.NumCPU(), "parallel mining workers")
	cmd.Flags().Int("count", 0, "number of transits to mine (0 = until exhausted or interrupted)")
	cmd.Flags().Int("refetch", 0, "seconds to mine one target before re-stamping it (0 = adaptive to the measured hashrate)")
	cmd.Flags().Uint64("fee", 0, "tag-along fee in motes (0 = configured/sequencer minimum; capped at 1% of A)")
	cmd.Flags().String("mode", modeDelegate, "post-confirmation mode: consolidate | delegate | stash")
	cmd.Flags().Int("per", defaultDelegatePer, "delegate mode: delegate the accumulated balance every C confirmed transits (C>=1)")
	cmd.Flags().Int("max-delegations", defaultMaxDelegations, "delegate mode: advisory cap on own delegations; at the cap the miner tops up an existing one instead of creating another")
	cmd.Flags().Bool("no-revocation-windows", false, "delegate mode: never top up inside a delegation's safe revocation window, so that window stays available to the owner as a way past a sequencer that refuses askstop")
	cmd.Flags().StringSlice("stream", nil, "extra node endpoints to subscribe to for mining transactions (in addition to api.endpoint); several make withholding by any single node ineffective")
	cmd.Flags().Bool("no-stream", false, "do not subscribe to the mining transaction stream (falls back to LRB-only detection, which is systematically slower than a competitor's own view)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runMineCmd(cmd *cobra.Command, _ []string) {
	workers, _ := cmd.Flags().GetInt("workers")
	if workers < 1 {
		workers = 1
	}
	count, _ := cmd.Flags().GetInt("count")
	refetchSec, _ := cmd.Flags().GetInt("refetch")
	feeFlag, _ := cmd.Flags().GetUint64("fee")
	mode, _ := cmd.Flags().GetString("mode")
	maxDelegations, _ := cmd.Flags().GetInt("max-delegations")
	noRevocationWindows, _ := cmd.Flags().GetBool("no-revocation-windows")
	perC, _ := cmd.Flags().GetInt("per")
	extraStreams, _ := cmd.Flags().GetStringSlice("stream")
	noStream, _ := cmd.Flags().GetBool("no-stream")

	glb.Assertf(mode == modeConsolidate || mode == modeDelegate || mode == modeStash,
		"invalid --mode %q: expected %s | %s | %s", mode, modeConsolidate, modeDelegate, modeStash)
	if perC < 1 {
		perC = 1
	}

	walletData := glb.GetWalletData()
	consts := glb.GetLedgerConstants()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	m := &miner{
		consts:               consts,
		lib:                  glb.GetTxLibrary(),
		c:                    glb.GetClient(),
		wallet:               walletData,
		holderID:             base.HolderIDFromED25519PrivateKey(walletData.PrivateKey),
		tagAlongSeqID:        *tagAlongSeqID,
		mode:                 mode,
		perC:                 perC,
		maxDelegations:       maxDelegations,
		useRevocationWindows: !noRevocationWindows,
		workers:              workers,
		window:               time.Duration(refetchSec) * time.Second,
	}
	m.st.start = time.Now()

	// tag-along fee for the mine tx: at least the sequencer minimum, never above
	// 1% of A (the mineLock cap). Follow-up consolidation/delegation txs use the
	// plain required fee (actionFee) — the 1% cap is a mineLock rule only.
	actionFee, err := retryCall("required tag-along fee", 0, func() (uint64, error) {
		return glb.GetRequiredTagAlongFee(m.tagAlongSeqID)
	})
	glb.AssertNoError(err)
	m.actionFee = actionFee

	// --fee is an offer on top of the sequencer's minimum, not a way under it:
	// a transit below the minimum is never picked up.
	m.fee = actionFee
	if feeFlag > m.fee {
		m.fee = feeFlag
	}
	// A grows with the slot, so the cap only ever rises: clamping against A now
	// keeps every transit this run builds inside the mineLock 1% rule.
	if feeCap := m.currentA() / 100; m.fee > feeCap {
		glb.Infof("tag-along fee %s exceeds the 1%% cap %s; clamping to the cap", util.Th(m.fee), util.Th(feeCap))
		m.fee = feeCap
		glb.Assertf(m.fee >= actionFee,
			"the 1%% mineLock fee cap %s is below the minimum tag-along fee %s required by sequencer %s: mining transits cannot be paid for",
			util.Th(feeCap), util.Th(actionFee), m.tagAlongSeqID.StringShort())
	}

	streamEndpoints := miningStreamEndpoints(noStream, extraStreams)
	m.banner(streamEndpoints)
	m.run(count, streamEndpoints)
}

// miningStreamEndpoints is the configured node plus any extras. Subscribing to
// more than one matters because a node cannot forge a transit — every one is
// verified locally — but it can withhold one, which silently restores the
// information asymmetry the stream exists to remove.
func miningStreamEndpoints(noStream bool, extra []string) []string {
	if noStream {
		return nil
	}
	ret := make([]string, 0, len(extra)+1)
	if own := viper.GetString("api.endpoint"); own != "" {
		ret = append(ret, own)
	}
	for _, e := range extra {
		if e = strings.TrimSpace(e); e != "" && !slices.Contains(ret, e) {
			ret = append(ret, e)
		}
	}
	return ret
}

func (m *miner) banner(streamEndpoints []string) {
	glb.Infof("")
	glb.Infof("================= PROXIMA BOOTSTRAP MINER =================")
	glb.Infof(" Proof-of-signing-work miner for the fair-launch mine chain.")
	glb.Infof(" Each transit mints a fixed reward A by finding a nonce whose")
	glb.Infof(" signed-tx hash ends in >= K trailing zero bits. The work")
	glb.Infof(" covers the signature, so every attempt costs one ed25519")
	glb.Infof(" sign — CPU-egalitarian, pool/ASIC-hostile. K does not depend")
	glb.Infof(" on the step length; the chain retargets K by one bit per")
	glb.Infof(" transit to hold the target pace.")
	glb.Infof("----------------------------------------------------------")
	glb.Infof(" miner account : %s", m.wallet.Account.String())
	glb.Infof(" mode          : %s%s", m.mode, delegateModeSuffix(m.mode, m.perC))
	if m.mode == modeDelegate {
		windows := "uses safe revocation windows"
		if !m.useRevocationWindows {
			windows = "leaves safe revocation windows to the owner"
		}
		glb.Infof(" delegations   : cap %d, %s", m.maxDelegations, windows)
	}
	a := m.currentA()
	glb.Infof(" reward A      : %s  (payout %s + tag-along %s)", util.Th(a), util.Th(a-m.fee), util.Th(m.fee))
	glb.Infof(" schedule      : %s flat until slot %d, then +%s per slot",
		util.Th(m.consts.MineAmountBase), m.consts.MineRampStartSlot, util.Th(m.consts.MineAmountPerSlot))
	glb.Infof(" tag-along seq : %s", m.tagAlongSeqID.String())
	glb.Infof(" workers       : %d   difficulty band: [%d, %d]", m.workers, m.consts.MineFloorDifficulty, m.consts.MineMaxDifficulty)
	glb.Infof(" pace          : min P %d, target %d slots/transit", m.consts.MineMinPace, m.consts.MineTargetPace)
	if len(streamEndpoints) == 0 {
		glb.Infof(" mining stream : OFF — competing transits are only seen once the LRB confirms them")
	} else {
		glb.Infof(" mining stream : %s", strings.Join(streamEndpoints, ", "))
	}
	glb.Infof("==========================================================")
}

func delegateModeSuffix(mode string, perC int) string {
	if mode != modeDelegate {
		return ""
	}
	return fmt.Sprintf(" (every %d confirmed transit(s), cut %d promille)", perC, mineDelegationCut)
}

// mineTip is the mine chain output the next transit is built on: either the
// LRB-confirmed tip, or the successor of a transaction this miner has just
// submitted and which nobody has confirmed yet (speculative).
type mineTip struct {
	oid         base.OutputID
	data        []byte
	ml          *txbuildercore.MineLockView
	cc          *txbuildercore.ChainConstraintView
	balance     uint64
	tagAlongFee uint64 // fee of the transit that produced this tip (0 for the confirmed root)
	speculative bool
}

func parseMineTip(lib *txbuildercore.Library[any], oid base.OutputID, data []byte, speculative bool) (*mineTip, error) {
	o, err := txbuildercore.OutputFromBytes(data)
	if err != nil {
		return nil, fmt.Errorf("mine tip: %w", err)
	}
	ml, err := lib.ParseMineLock(o.MustConstraintAt(txbuildercore.ConstraintIndexLock))
	if err != nil {
		return nil, err
	}
	cc, err := lib.ParseChainConstraint(o.MustConstraintAt(txbuildercore.ConstraintIndexChain))
	if err != nil {
		return nil, err
	}
	balance, err := txbuildercore.DecodeTokenBalance(data)
	if err != nil {
		return nil, err
	}
	return &mineTip{oid: oid, data: data, ml: ml, cc: cc, balance: balance, speculative: speculative}, nil
}

// miner holds the whole run: immutable configuration plus the state shared
// between the mining loop and the confirmation monitor.
type miner struct {
	consts               *txbuildercore.Constants
	lib                  *txbuildercore.Library[any]
	c                    *client.APIClient
	wallet               glb.WalletData
	holderID             base.HolderID
	tagAlongSeqID        base.ChainID
	fee                  uint64 // tag-along fee of the mine tx
	actionFee            uint64 // tag-along fee of consolidation/delegation txs
	mode                 string
	perC                 int
	maxDelegations       int
	useRevocationWindows bool
	workers              int
	window               time.Duration // fixed mining window; 0 = adaptive

	// abort is set whenever the tip being mined stops being the branch to
	// extend — by a streamed competing transit or by an LRB confirmation — and
	// is polled by the mining workers, so a round whose target is already dead
	// is dropped instead of running to its deadline. Only the loop clears it, at
	// the top of each round, so a signal can never be lost between rounds.
	abort      atomic.Bool
	difficulty atomic.Int64 // last K, for the totals line and the stall timeout
	hashrate   atomic.Int64 // last measured attempts/sec, for the stall timeout

	// tree is the shared view of the mine chain: the mining loop reads the tip
	// to extend from it, the stream feeds verified transits into it, and the LRB
	// monitor re-roots it. It carries its own lock.
	tree *mineTree

	mu            sync.Mutex
	st            mineStats
	delegateAccum int // confirmed transits since the last delegation
}

func (m *miner) run(count int, streamEndpoints []string) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	root, err := m.fetchConfirmedTip()
	if err != nil {
		glb.Infof("cannot fetch the mine chain output: %v", err)
		return
	}
	m.tree = newMineTree(root)
	glb.Infof("anchored on confirmed transit #%d %s", root.cc.TransitionCounter, root.oid.StringShort())

	// Start the stream before the first round: a transit that lands while we are
	// mining must abort the round, which is the whole point of subscribing.
	m.runStreams(ctx, streamEndpoints)
	go m.monitorConfirmations(ctx)

	hashrate := 0.0 // attempts/sec, measured across mining rounds; 0 = not yet known
	for count == 0 || m.minedCount() < count {
		m.abort.Store(false)
		tip := m.tree.takeBestForMining()

		predSlot := tip.oid.Timestamp().Slot
		succSlot := m.successorSlot(predSlot)
		// A depends on the slot the transit is stamped in, so the exhaustion
		// test has to be made against the successor slot, not against now.
		if a := m.consts.MineAmountAtSlot(succSlot); tip.ml.R < a {
			glb.Infof("mine chain is exhausted: remaining mintable %s < A %s", util.Th(tip.ml.R), util.Th(a))
			break
		}
		// K = max(B - (M - P), E): the full B at the minimum pace, one bit easier per
		// extra slot of gap. Stamping the earliest legal slot (successorSlot) targets
		// the highest K; when the clock forces a later stamp the gap grows and K drops.
		k := int(m.consts.MineRequiredK(tip.ml.B, uint64(succSlot-predSlot)))
		succB := m.consts.MineAdjustedB(tip.ml.B, predSlot, succSlot)
		m.difficulty.Store(int64(k))

		tmpl := m.buildTemplate(tip, succSlot, succB)

		window := m.window
		if window <= 0 {
			window = adaptiveRefetchWindow(k, hashrate)
		}
		glb.Infof("mining transit #%d%s: R=%s difficulty K=%d target slot %d (pace %d, successor B=%d) ...",
			tip.cc.TransitionCounter+1, m.branchSuffix(tip), util.Th(tip.ml.R), k, succSlot, succSlot-predSlot, succB)
		glb.Verbosef("   window %v (expected ~%s attempts at %s H/s)",
			window.Round(time.Second), util.Th(uint64(math.Ldexp(1, k))), util.Th(uint64(hashrate)))

		roundStart := time.Now()
		winBytes, attempts, found := m.mineParallel(tmpl, k, window)
		hashrate = updateHashrate(hashrate, attempts, time.Since(roundStart))
		m.hashrate.Store(int64(hashrate))
		if m.abort.Load() {
			continue // target superseded; the loop head picks the new best tip
		}
		if !found {
			// Re-stamping loses no expected work: every attempt is an independent
			// 2^-K trial. A later stamp also widens the retarget span, which eases
			// the successor difficulty when this miner is too slow for the target.
			glb.Infof("   no solution in %v after %s attempts (%s H/s); re-stamping",
				window.Round(time.Second), util.Th(attempts), util.Th(uint64(hashrate)))
			continue
		}
		txid, err := txbuildercore.TxIDFromBytes(winBytes)
		glb.AssertNoError(err) // pure local computation over bytes just built
		glb.Infof("   SOLVED transit #%d in %s attempts; submitting %s",
			tip.cc.TransitionCounter+1, util.Th(attempts), txid.StringShort())

		if !m.awaitStampWindow(succSlot) {
			continue // superseded while waiting for the clock
		}
		if !m.submit(winBytes, tip.data) {
			// the chain likely moved under us; re-anchor on what is confirmed
			if p, err := m.fetchConfirmedTip(); err == nil {
				m.tree.setRoot(p)
			}
			continue
		}
		// Our own transit goes through exactly the same verification and
		// tie-break as anyone else's: nothing here may prefer it merely for
		// being ours, since that is the bias this design exists to remove.
		m.acceptTransit(tip, winBytes, true)
	}
	m.drain()
}

// branchSuffix annotates the log line with how far ahead of the confirmed tip
// this target is.
func (m *miner) branchSuffix(tip *mineTip) string {
	if !tip.speculative {
		return ""
	}
	confirmed, _, _, _ := m.tree.stats()
	return fmt.Sprintf(" (speculative, +%d)", tip.cc.TransitionCounter-confirmed)
}

// nowSlot is the current ledger slot derived from the local clock. Ledger time
// is a pure function of wall-clock time and the genesis timestamp, so the miner
// computes it locally instead of asking the node on every round.
func (m *miner) nowSlot() uint32 {
	return m.consts.LedgerTimeFromClockTime(time.Now()).Slot
}

// currentA is the reward a transit stamped in the current slot would mint. Used
// for display and for the fee cap; a transit under construction takes A from its
// own successor slot instead.
func (m *miner) currentA() uint64 {
	return m.consts.MineAmountAtSlot(m.nowSlot())
}

// successorSlot stamps the next transit as early as mineLock allows — the
// minimum pace above the predecessor — or the current slot if that is later.
//
// Stamping at the MINIMUM slot targets the highest difficulty: under the pace-
// relieved K = max(B - (M - P), E) the earliest legal slot is the shortest gap
// M = P, so K = B. It is also what lets the retarget work. The retarget compares
// the single last gap to the target pace; stamping at the minimum makes that gap
// a real measurement: it comes out short (harden) while the miner keeps up, and
// stretches on its own (ease, lower K) once it cannot, because the wall clock
// then sets the stamp. Stamping at the target instead would land every transit
// on the hold branch and freeze the difficulty wherever it happened to be.
//
// Difficulty that tracks real hashrate is what keeps mining decided by work.
// When solve time falls far below the pace every miner sits solved and waiting
// for the earliest legal slot, and the winner is settled by network proximity
// instead. See claude/mining-bias.md.
func (m *miner) successorSlot(predSlot uint32) uint32 {
	succSlot := predSlot + uint32(m.consts.MineMinPace)
	if now := m.nowSlot(); now > succSlot {
		succSlot = now
	}
	return succSlot
}

// awaitStampWindow holds a solved transaction back until its slot is close
// enough to the wall clock for the node to accept it. Returns false if the
// target was superseded while waiting, in which case the transaction is
// discarded unsubmitted.
func (m *miner) awaitStampWindow(succSlot uint32) bool {
	for {
		if m.abort.Load() {
			return false
		}
		now := m.nowSlot()
		if succSlot <= now+mineMaxFutureSlots {
			return true
		}
		glb.Verbosef("   holding solved tx: slot %d is %d slots ahead of the clock", succSlot, succSlot-now)
		time.Sleep(m.consts.SlotDuration())
	}
}

// submit posts the solved transaction, retrying transport and node-side submit
// failures. A parse/validation rejection is deterministic — retrying it cannot
// help — so it fails immediately and the caller falls back to the confirmed tip.
// Uses the client directly rather than glb.SubmitAndDisplay because a retry loop
// must not dump the failing transaction on every transient error.
func (m *miner) submit(txBytes, consumedBytes []byte) bool {
	_, err := retryCall("submit mine tx", 5, func() (base.TransactionID, error) {
		txid, err := m.c.SubmitTransactionWithDetail(txBytes, client.WithConsumedUTXOs([][]byte{consumedBytes}))
		if err != nil && isSubmitRejection(err) {
			return txid, terminalError{err}
		}
		return txid, err
	})
	if err != nil {
		glb.Infof("   submit failed: %v", err)
		return false
	}
	m.mu.Lock()
	m.st.mined++
	m.mu.Unlock()
	return true
}

// isSubmitRejection tells a deterministic validation rejection from a transient
// failure. The submit endpoint reports the failing stage in the error text.
func isSubmitRejection(err error) bool {
	s := err.Error()
	return strings.Contains(s, "stage=parse") || strings.Contains(s, "stage=full")
}

func (m *miner) minedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.mined
}

// fetchConfirmedTip reads the mine chain tip from the LRB state, retrying until
// the node answers.
func (m *miner) fetchConfirmedTip() (*mineTip, error) {
	return retryCall("fetch mine chain tip", 0, func() (*mineTip, error) {
		oData, lrbid, err := m.c.GetChainOutputData(base.MineChainID)
		if err != nil {
			return nil, err
		}
		glb.Verbosef("   LRB %s", lrbid.StringShort())
		return parseMineTip(m.lib, oData.ID, oData.Data, false)
	})
}

// monitorConfirmations polls the LRB mine chain tip in the background, so the
// mining loop never blocks on confirmation. The stream is the fast path for
// learning about competing transits; this is the slow, authoritative one that
// settles which branch actually won and prunes the rest.
func (m *miner) monitorConfirmations(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(mineMonitorPeriod):
		}
		oData, _, err := m.c.GetChainOutputData(base.MineChainID)
		if err != nil {
			continue // transient: the next tick retries
		}
		tip, err := parseMineTip(m.lib, oData.ID, oData.Data, false)
		if err != nil {
			continue
		}
		stall := m.stallTimeout()
		if m.onConfirmedTip(tip) == tipNoChange && m.tree.stalledFor(stall) {
			// Nothing of ours has confirmed for a long time and no competitor
			// has taken the height either, so we are not losing races — our
			// submissions are not reaching the ledger. Drop the branch and
			// rebuild from what is actually confirmed.
			glb.Infof("   nothing confirmed for %v; discarding the speculative branch and re-anchoring",
				stall.Round(time.Second))
			m.tree.setRoot(tip)
			m.abort.Store(true)
		}
	}
}

// mineTipVerdict is what a confirmed tip means for this miner's branch.
type mineTipVerdict int

const (
	tipNoChange      mineTipVerdict = iota // nothing this miner needs to react to
	tipConfirmedOurs                       // own transit(s) confirmed; payouts are spendable
	tipReanchor                            // the branch being extended is dead
)

// onConfirmedTip re-roots the tree on a confirmed tip and runs the
// post-confirmation action when transits of ours settled.
func (m *miner) onConfirmedTip(tip *mineTip) mineTipVerdict {
	wasMiningOn := m.tree.bestTip().oid
	verdict, ownConfirmed := m.tree.onConfirmed(tip)

	switch verdict {
	case tipNoChange:
		return verdict

	case tipReanchor:
		glb.Infof("   transit #%d confirmed as %s — none of ours; re-anchoring",
			tip.cc.TransitionCounter, tip.oid.StringShort())

	case tipConfirmedOurs:
		m.mu.Lock()
		m.st.transits += ownConfirmed
		m.st.minted += m.consts.MineAmountAtSlot(tip.oid.Timestamp().Slot) * uint64(ownConfirmed)
		m.delegateAccum += ownConfirmed
		doDelegate := m.mode == modeDelegate && m.delegateAccum >= m.perC
		m.mu.Unlock()

		glb.Infof("   confirmed transit #%d %s (%d of ours)",
			tip.cc.TransitionCounter, tip.oid.StringShort(), ownConfirmed)
		m.postConfirmationAction(doDelegate)
	}

	// re-rooting may have dropped the branch the loop was extending
	if m.tree.bestTip().oid != wasMiningOn || m.tree.superseded() {
		m.abort.Store(true)
	}
	return verdict
}

// postConfirmationAction sweeps or delegates the confirmed payouts. It runs on
// the monitor goroutine and every step is best-effort: a failure only defers the
// action to the next confirmation, it never disturbs mining.
func (m *miner) postConfirmationAction(doDelegate bool) {
	outs, total, err := m.minerAccountSnapshot()
	if err != nil {
		glb.Infof("   cannot read the miner account: %v", err)
		return
	}
	switch {
	case m.mode == modeConsolidate:
		m.consolidateMinerAccount(outs)
	case doDelegate:
		if m.delegateMinerAccount(outs, total) {
			m.mu.Lock()
			m.delegateAccum = 0
			m.mu.Unlock()
		}
	}
	m.printTotals(total, len(outs))
}

// drain gives the last submitted transits a chance to confirm before the run
// ends, so the final totals are not misleadingly short.
func (m *miner) drain() {
	deadline := time.Now().Add(m.stallTimeout())
	for time.Now().Before(deadline) {
		if _, inFlight, _, _ := m.tree.stats(); inFlight == 0 {
			break
		}
		time.Sleep(mineMonitorPeriod)
	}
	_, _, _, orphaned := m.tree.stats()
	m.mu.Lock()
	defer m.mu.Unlock()
	glb.Infof("done: submitted %d transit(s), %d confirmed, %d orphaned in %s",
		m.st.mined, m.st.transits, orphaned, time.Since(m.st.start).Round(time.Second))
}

// minerAccountSnapshot returns the confirmed, spendable sigLock payouts of the
// miner account and their total balance.
func (m *miner) minerAccountSnapshot() ([]*ledger.OutputWithID, uint64, error) {
	type snapshot struct {
		outs  []*ledger.OutputWithID
		total uint64
	}
	s, err := retryCall("read miner account", 3, func() (snapshot, error) {
		outs, _, total, err := m.c.GetSpendableOutputs(m.wallet.Account, client.SpendableOutputsParams{
			TargetSlot: m.nowSlot(),
		})
		return snapshot{outs, total}, err
	})
	return s.outs, s.total, err
}

// consolidateMinerAccount sweeps all payout UTXOs into one sigLock output back
// to the miner (fire-and-forget). No-op with fewer than two outputs.
func (m *miner) consolidateMinerAccount(outs []*ledger.OutputWithID) {
	if len(outs) < 2 {
		return
	}
	outs = largestOutputs(outs)
	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(m.lib, m.consts, txbuildercore.CompactParams{
		Inputs:           compactInputs(outs),
		WalletPrivateKey: m.wallet.PrivateKey,
		TagAlongSeqID:    m.tagAlongSeqID,
		TagAlongFee:      m.actionFee,
		TargetSlot:       m.nowSlot(),
	})
	if err != nil {
		glb.Infof("   consolidation build failed: %v", err)
		return
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   consolidation submit failed: %v", err)
		return
	}
	m.mu.Lock()
	m.st.consolidations++
	m.mu.Unlock()
	glb.Infof("   consolidated %d payout UTXO(s) -> %s (submitted, not awaited)", len(consumed), txid.StringShort())
}

// largestOutputs caps a UTXO set to the attachment-cost budget, keeping the
// largest.
func largestOutputs(outs []*ledger.OutputWithID) []*ledger.OutputWithID {
	sort.Slice(outs, func(i, j int) bool {
		return outs[i].Output.TokenBalance() > outs[j].Output.TokenBalance()
	})
	if len(outs) > defaultMaxNumberOfInputs {
		outs = outs[:defaultMaxNumberOfInputs]
	}
	return outs
}

// delegateMinerAccount puts the confirmed payouts to work. Three steps, no
// threshold (claude/delegation_add_tokens.md):
//
//  1. a delegation the master can consume  -> add the payouts to it
//  2. otherwise, below the cap             -> create a new delegation
//  3. otherwise                            -> askstop one; the next pass takes step 1
//
// Returns true if a transaction was submitted, false if deferred, so the caller
// keeps accumulating. Step 3 returns true on the request alone: the payouts stay
// undelegated for one more cycle, which is also what stops it asking again
// before the sequencer has had a chance to answer.
func (m *miner) delegateMinerAccount(outs []*ledger.OutputWithID, total uint64) bool {
	if len(outs) == 0 || total <= m.actionFee {
		return false
	}
	outs = largestOutputs(outs)

	dels, err := m.listOwnDelegations()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return false
	}
	slot := m.nowSlot()

	if d := m.pickTopUpTarget(dels, slot); d != nil {
		return m.topUpDelegation(d, outs, total)
	}
	if len(dels) < m.maxDelegations {
		return m.createDelegation(outs)
	}
	d := m.pickAskstopTarget(dels, slot)
	if d == nil {
		glb.Infof("   delegation deferred: at the cap of %d, none consumable and none frozen", m.maxDelegations)
		return false
	}
	return m.askstopDelegation(d, outs)
}

// createDelegation delegates the accumulated payout balance to a random alive
// sequencer as a fresh delegation chain (fire-and-forget).
func (m *miner) createDelegation(outs []*ledger.OutputWithID) bool {
	sumIn := uint64(0)
	for _, o := range outs {
		sumIn += o.Output.TokenBalance()
	}
	amount := sumIn - m.actionFee

	minAmt, err := m.minDelegationAmount()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return false
	}
	if amount < minAmt {
		glb.Infof("   delegation deferred: balance %s < minimum inflatable %s (accumulating)",
			util.Th(amount), util.Th(minAmt))
		return false
	}
	seqID, err := m.chooseRandomAliveSequencer()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return false
	}

	txb := txbuildercore.New(0)
	consumed := make([][]byte, 0, len(outs))
	var maxInputTs base.LedgerTime
	for i, in := range outs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		maxInputTs = base.MaximumTime(maxInputTs, in.Timestamp())
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), txbuildercore.ConstraintIndexLock, 0); err != nil {
				glb.Infof("   delegation build failed: %v", err)
				return false
			}
		}
	}

	ts := m.consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs)

	delegationOut, err := m.lib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
		Amount:               amount,
		MasterID:             m.holderID,
		Target:               seqID,
		RequiredInflationCut: mineDelegationCut,
		StartSlot:            ts.Slot,
	})
	if err != nil {
		glb.Infof("   delegation build failed: %v", err)
		return false
	}
	delegationIdx := txb.ProduceOutput(delegationOut.Bytes())

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(m.lib, m.actionFee, m.tagAlongSeqID, m.holderID)
	if err != nil {
		glb.Infof("   delegation build failed: %v", err)
		return false
	}
	txb.ProduceOutput(tagAlongOut.Bytes())

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(m.wallet.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err) // pure local computation over bytes just built
	delegationOid, err := base.NewOutputID(txid, delegationIdx)
	glb.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)

	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   delegation submit failed: %v", err)
		return false
	}
	m.mu.Lock()
	m.st.delegations++
	m.mu.Unlock()
	glb.Infof("   delegated %s to sequencer %s, delegation ID %s (submitted, not awaited)",
		util.Th(amount), seqID.StringShort(), delegationID.StringShort())
	return true
}

// minDelegationAmount is the "minimum inflatable" floor for a fresh delegation
// output, projected over a wide slot horizon. Computed server-side via /eval so
// the wallet stays singleton-free (mirrors `proxi node dlg amount`).
func (m *miner) minDelegationAmount() (uint64, error) {
	slot := m.nowSlot()
	inflMin, err := retryCall("eval minimum inflatable", 3, func() (uint64, error) {
		return m.c.EvalU64(0, fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			m.consts.MinimumInflatableAmount0, 0, slot+10000))
	})
	if err != nil {
		return 0, err
	}
	return m.consts.MinimumInflatableAmount0 + inflMin, nil
}

// aliveSequencerSlots is how recent a sequencer's latest output must be for it
// to count as alive. A sequencer that is running produces a milestone most
// slots, so 2 slots is already generous; a wider window mostly admits ones that
// have just stopped.
const aliveSequencerSlots = 2

// chooseRandomAliveSequencer picks a uniformly-random sequencer whose latest
// output is within aliveSequencerSlots of now. If none qualifies it falls back
// to whichever produced most recently, rather than failing: delegating to a
// sequencer that may have stalled is recoverable (the delegation simply stays
// unfrozen and the next pass re-rolls the target), whereas failing here strands
// the payouts undelegated.
func (m *miner) chooseRandomAliveSequencer() (base.ChainID, error) {
	outs, err := retryCall("list sequencers", 3, func() (map[base.ChainID]ledger.OutputWithSequencerData, error) {
		o, _, err := m.c.GetAllSequencerOutputs()
		return o, err
	})
	if err != nil {
		return base.ChainID{}, err
	}
	nowSlot := m.nowSlot()
	alive := make([]base.ChainID, 0, len(outs))
	var newest base.ChainID
	var newestSlot uint32
	var haveNewest bool
	for id, out := range outs {
		slot := out.ID.Slot()
		if slot+aliveSequencerSlots >= nowSlot {
			alive = append(alive, id)
		}
		if !haveNewest || slot > newestSlot {
			newest, newestSlot, haveNewest = id, slot, true
		}
	}
	if len(alive) > 0 {
		return alive[mathrand.Intn(len(alive))], nil
	}
	if haveNewest {
		glb.Verbosef("no sequencer within %d slots; falling back to the most recent %s (slot %d, now %d)",
			aliveSequencerSlots, newest.StringShort(), newestSlot, nowSlot)
		return newest, nil
	}
	return base.ChainID{}, fmt.Errorf("no sequencer to delegate to")
}

// printTotals emits the run-wide totals line after a confirmed transit.
func (m *miner) printTotals(heldBalance uint64, heldCount int) {
	_, inFlight, tracked, orphaned := m.tree.stats()

	m.mu.Lock()
	defer m.mu.Unlock()
	up := time.Since(m.st.start)
	avg := uint64(0)
	if s := up.Seconds(); s > 0 {
		avg = uint64(float64(m.st.attempts) / s)
	}
	glb.Infof("   totals: confirmed %d (+%d in flight, %d orphaned, %d tracked) | minted %s | held %s in %d UTXO(s) | consol %d / deleg %d | K=%d | attempts %s | avg %s H/s | uptime %s",
		m.st.transits, inFlight, orphaned, tracked, util.Th(m.st.minted), util.Th(heldBalance), heldCount,
		m.st.consolidations, m.st.delegations, m.difficulty.Load(), util.Th(m.st.attempts), util.Th(avg), up.Round(time.Second))
}

// terminalError marks an error that retrying cannot fix.
type terminalError struct{ error }

// retryCall repeats f until it succeeds, backing off exponentially up to
// mineRetryMax between tries. attempts <= 0 means retry indefinitely: the miner
// is a long-running process that must ride out node restarts rather than abort
// on the first communication failure. A terminalError stops the loop at once.
func retryCall[T any](what string, attempts int, f func() (T, error)) (T, error) {
	var (
		zero    T
		lastErr error
	)
	d := mineRetryBase
	for i := 1; attempts <= 0 || i <= attempts; i++ {
		v, err := f()
		if err == nil {
			return v, nil
		}
		var term terminalError
		if errors.As(err, &term) {
			return zero, term.error
		}
		lastErr = err
		glb.Verbosef("   %s failed (attempt %d): %v; retrying in %v", what, i, err, d)
		time.Sleep(d)
		if d *= 2; d > mineRetryMax {
			d = mineRetryMax
		}
	}
	return zero, fmt.Errorf("%s: giving up after %d attempt(s): %w", what, attempts, lastErr)
}

// buildTemplate assembles one valid mine transition against the given tip and
// returns the compiled PoW template plus the successor output bytes (which
// become the tip of the speculative branch once the transaction is submitted).
// The successor (index 0) keeps the balance, mints A as inflation, decrements R
// by A and carries the retargeted B; A is read off the successor slot, which is
// what the constraint validates against; the payout (index 1) is sig-locked to the
// signer (mineLock requires payout holder == tx signer); the tag-along (index 2)
// pays the fee. The slot is baked in — only the nonce and signature vary.
func (m *miner) buildTemplate(tip *mineTip, succSlot uint32, succB uint64) *mineTemplate {
	a := m.consts.MineAmountAtSlot(succSlot)
	succLockBin, err := m.lib.NewMineLock(tip.ml.R-a, succB)
	glb.AssertNoError(err)
	succChainBin, err := m.lib.NewChainTransition(base.MineChainID, 0, tip.cc.OriginSlot,
		tip.cc.CumulativeChainInflation+a, 0, tip.cc.TransitionCounter+1, 0)
	glb.AssertNoError(err)
	sb := txbuildercore.NewOutputBuilder()
	sb.PutConstraint(txbuildercore.EncodeAmounts(tip.balance, a), txbuildercore.ConstraintIndexAmounts)
	sb.PutConstraint(succLockBin, txbuildercore.ConstraintIndexLock)
	sb.PutConstraint(succChainBin, txbuildercore.ConstraintIndexChain)
	succOutBytes := sb.Output().Bytes()

	payoutOut, err := txbuildercore.NewSigLockOutput(m.lib, a-m.fee, m.holderID)
	glb.AssertNoError(err)
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(m.lib, m.fee, m.tagAlongSeqID, m.holderID)
	glb.AssertNoError(err)

	txb := txbuildercore.New(0)
	predIdx := txb.ConsumeOutput(tip.data, tip.oid)
	txb.ProduceOutput(succOutBytes)
	txb.ProduceOutput(payoutOut.Bytes())
	txb.ProduceOutput(tagAlongOut.Bytes())
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	ts := base.T(succSlot, 1)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()

	return newMineTemplate(txb, predIdx, m.wallet.PrivateKey, ts.Bytes())
}

// mineTemplate holds the two pre-serialized buffers and the placeholder offsets
// discovered once for a fixed target. A worker clones the buffers and overwrites
// only the nonce (both) and signature (full) per attempt.
type mineTemplate struct {
	essence []byte // blake2b input for the txID (positions concatenated, signature skipped)
	full    []byte // full raw tx bytes (PoW blake2b input), placeholder signature spliced in

	nonceOffEss  int
	nonceOffFull int
	sigOffFull   int

	slotBytes []byte // 5-byte timestamp, overlaid onto txID[0:5] like TxIDFromTree
	outCount  byte   // produced-outputs count minus 1 (txID byte 5)
	priv      ed25519.PrivateKey
}

// newMineTemplate splices random nonce/signature sentinels into the assembled
// builder, records their offsets, and verifies the template reproduces the
// canonical TxBuilder output byte-for-byte.
func newMineTemplate(txb *txbuildercore.TxBuilder, predIdx byte, priv ed25519.PrivateKey, slotBytes []byte) *mineTemplate {
	nonceSentinel := randSentinel(8)
	sigSentinel := randSentinel(64)
	pub := priv.Public().(ed25519.PublicKey)

	// nonce lives in the open lock's unlock params (ignored by mineLock, part of
	// the essence so it perturbs txID -> signature -> tx hash).
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, nonceSentinel)
	sd := make([]byte, 0, 1+len(sigSentinel)+len(pub))
	sd = append(sd, base.SignatureTypeED25519)
	sd = append(sd, sigSentinel...)
	sd = append(sd, pub...)
	txb.TxData.SignatureData = sd

	ess := buildEssence(txb.ToTuple().AsTree())
	full := txb.Bytes()
	t := &mineTemplate{
		essence:      ess,
		full:         full,
		nonceOffEss:  mustIndexOnce(ess, nonceSentinel),
		nonceOffFull: mustIndexOnce(full, nonceSentinel),
		sigOffFull:   mustIndexOnce(full, sigSentinel),
		slotBytes:    slotBytes,
		outCount:     byte(txb.NumOutputs() - 1),
		priv:         priv,
	}
	verifyMineTemplate(t, txb, predIdx)
	return t
}

// mineWorker is a per-goroutine clone bound to one target. Its hot path patches
// only the nonce (both buffers) and signature (full buffer) per attempt.
type mineWorker struct {
	*mineTemplate
	essence []byte
	full    []byte
	n8      [8]byte
	txid    base.TransactionID
}

func (t *mineTemplate) newWorker() *mineWorker {
	return &mineWorker{
		mineTemplate: t,
		essence:      append([]byte(nil), t.essence...),
		full:         append([]byte(nil), t.full...),
	}
}

// attempt runs one nonce and returns the trailing-zero-bit count of the PoW hash.
// The whole valid tx bytes are in m.full afterwards.
func (m *mineWorker) attempt(nonce uint64) int {
	binary.BigEndian.PutUint64(m.n8[:], nonce)

	copy(m.essence[m.nonceOffEss:], m.n8[:])
	eh := blake2b.Sum256(m.essence)
	copy(m.txid[:], eh[:])
	copy(m.txid[0:base.LedgerTimeByteLength], m.slotBytes) // txID[0:5] = timestamp
	m.txid[base.LedgerTimeByteLength] = m.outCount         // txID[5] = numOutputs-1

	sig := ed25519.Sign(m.priv, m.txid[:])
	copy(m.full[m.nonceOffFull:], m.n8[:])
	copy(m.full[m.sigOffFull:], sig)
	ph := blake2b.Sum256(m.full)
	return trailingZeroBits(ph)
}

// verifyMineTemplate mines one attempt via the template and independently via
// the canonical TxBuilder (PutUnlockParams + SignED25519 + Bytes) with the same
// nonce, asserting the txID and full bytes match. Offsets are nonce-independent
// (fixed field widths), so one sample proves them.
func verifyMineTemplate(t *mineTemplate, txb *txbuildercore.TxBuilder, predIdx byte) {
	const nonce = uint64(0xA5A5A5A5A5A5A5A5)
	m := t.newWorker()
	m.attempt(nonce)
	myTxid := m.txid
	myFull := append([]byte(nil), m.full...)

	var n8 [8]byte
	binary.BigEndian.PutUint64(n8[:], nonce)
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, n8[:])
	txb.SignED25519(t.priv)
	canonFull := txb.Bytes()
	canonTxid, err := txbuildercore.TxIDFromBytes(canonFull)
	glb.AssertNoError(err)

	glb.Assertf(myTxid == canonTxid, "mine template txID mismatch vs TxBuilder")
	glb.Assertf(bytes.Equal(myFull, canonFull), "mine template full-tx bytes mismatch vs TxBuilder")
}

// Bounds and shape of the adaptive mining window. Re-stamping costs nothing
// statistically — every attempt is an independent 2^-K trial, so abandoning a
// search and re-stamping loses no expected work — it only trades log churn
// against target staleness.
//
// The upper bound also sets the ledger-time pace tail. At the equilibrium
// difficulty the raw window 2^K/hashrate exceeds the cap, so the cap is what a
// miner mines a gap-P (K=B) target for before re-stamping to a later slot, where
// pace-relief lowers K until it solves. Every target that no miner solves at the
// floor pace therefore lands one cap-width later, so the cap becomes the size of
// that jump. Kept near one target-pace so a miss costs about a target-pace of
// extra gap (re-stamp to ~P+2, relieving 2-3 bits — enough to solve) instead of
// the many-minute jump a large cap produces.
const (
	minRefetchWindow = 5 * time.Second
	maxRefetchWindow = 45 * time.Second
	// used for the first round, which measures the hashrate
	initialRefetchWindow = 5 * time.Second
	// window as a multiple of the mean solve time: the solve time is
	// exponentially distributed, so 2x means ~86% of targets land within one window
	refetchWindowFactor = 2.0
	// weight of a new measurement in the running hashrate estimate
	hashrateEWMAWeight = 0.3
)

// adaptiveRefetchWindow sizes the mining window from the difficulty and the
// measured hashrate: the mean solve time at K is 2^K/hashrate seconds.
func adaptiveRefetchWindow(k int, hashrate float64) time.Duration {
	if hashrate <= 0 {
		return initialRefetchWindow
	}
	// clamp in float seconds: at a high K the raw window overflows time.Duration
	secs := refetchWindowFactor * math.Ldexp(1, k) / hashrate
	switch {
	case secs <= minRefetchWindow.Seconds():
		return minRefetchWindow
	case secs >= maxRefetchWindow.Seconds():
		return maxRefetchWindow
	}
	return time.Duration(secs * float64(time.Second))
}

// stallTimeout is how long the miner waits for a confirmation before presuming
// the speculative branch lost. It must exceed the time to mine one transit at the
// current difficulty (2^K / hashrate), or a legitimately slow high-K transit would
// be abandoned mid-solve — the fixed-90s version deadlocked the chain when B
// overshot. Scales as mineStallSolveFactor solve-times, floored at
// mineConfirmationStall and capped at mineStallMax. K tracks the pace-relieved
// difficulty, so as a slow chain's gap grows and its effective K drops the
// timeout shrinks with it.
func (m *miner) stallTimeout() time.Duration {
	k := m.difficulty.Load()
	h := float64(m.hashrate.Load())
	if k <= 0 || h <= 0 {
		return mineConfirmationStall
	}
	secs := mineStallSolveFactor * math.Ldexp(1, int(k)) / h
	switch {
	case secs <= mineConfirmationStall.Seconds():
		return mineConfirmationStall
	case secs >= mineStallMax.Seconds():
		return mineStallMax
	}
	return time.Duration(secs * float64(time.Second))
}

// updateHashrate folds one round's measurement into the running estimate, so a
// single lucky or unlucky round does not swing the window.
func updateHashrate(prev float64, attempts uint64, elapsed time.Duration) float64 {
	if attempts == 0 || elapsed <= 0 {
		return prev
	}
	h := float64(attempts) / elapsed.Seconds()
	if prev <= 0 {
		return h
	}
	return (1-hashrateEWMAWeight)*prev + hashrateEWMAWeight*h
}

// mineParallel runs the configured workers against the template's target until
// one hash reaches targetK trailing zero bits, maxDur elapses, or the monitor
// aborts the round. Prints a live attempts/hashrate line and folds the attempt
// total into the stats. Returns the winning full tx bytes on success.
func (m *miner) mineParallel(tmpl *mineTemplate, targetK int, maxDur time.Duration) (winBytes []byte, attempts uint64, found bool) {
	var att uint64
	var foundFlag int32
	var mu sync.Mutex
	start := time.Now()
	deadline := start.Add(maxDur)

	// live progress ticker: reads the shared attempt counter every 2s.
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				n := atomic.LoadUint64(&att)
				el := time.Since(start).Seconds()
				hs := uint64(0)
				if el > 0 {
					hs = uint64(float64(n) / el)
				}
				fmt.Printf("\r   mining... %s attempts  %s H/s  %.0fs   ", util.Th(n), util.Th(hs), el)
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < m.workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			mw := tmpl.newWorker()
			n := seed
			var local, flushed uint64
			for {
				if local&0x3ff == 0 {
					atomic.AddUint64(&att, local-flushed) // publish progress for the ticker
					flushed = local
					if atomic.LoadInt32(&foundFlag) != 0 || m.abort.Load() || time.Now().After(deadline) {
						break
					}
				}
				n += uint64(m.workers) // disjoint nonce spaces per worker
				tz := mw.attempt(n)
				local++
				if tz >= targetK {
					if atomic.CompareAndSwapInt32(&foundFlag, 0, 1) {
						mu.Lock()
						winBytes = append([]byte(nil), mw.full...)
						mu.Unlock()
					}
					break
				}
			}
			atomic.AddUint64(&att, local-flushed)
		}(uint64(w))
	}
	wg.Wait()
	close(done)
	fmt.Printf("\r%70s\r", "") // clear the progress line

	total := atomic.LoadUint64(&att)
	m.mu.Lock()
	m.st.attempts += total
	m.mu.Unlock()
	return winBytes, total, atomic.LoadInt32(&foundFlag) != 0
}

// buildEssence reproduces HashEssence's blake2b input: the concatenation of each
// top-level tx position's serialized bytes, skipping the signature slot.
func buildEssence(tree *tuples.Tree) []byte {
	var ess []byte
	for i := byte(0); i < txbuildercore.TxTreeTupleNumElements; i++ {
		if i == txbuildercore.TxSignatureData {
			continue
		}
		d, err := tree.BytesAtPath([]byte{i})
		glb.AssertNoError(err)
		ess = append(ess, d...)
	}
	return ess
}

func mustIndexOnce(buf, sub []byte) int {
	n := bytes.Count(buf, sub)
	glb.Assertf(n == 1, "mine template placeholder must appear exactly once, found %d", n)
	return bytes.Index(buf, sub)
}

// randSentinel returns n random bytes used as a unique placeholder marker in the
// serialized tx (nonce or signature); crypto/rand makes a collision negligible.
func randSentinel(n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	glb.AssertNoError(err)
	return b
}

// trailingZeroBits counts zero bits at the least-significant end of the 256-bit
// hash — the same suffix-hashcash definition the mineLock PoW check enforces.
func trailingZeroBits(h [32]byte) int {
	n := 0
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] == 0 {
			n += 8
			continue
		}
		return n + bits.TrailingZeros8(h[i])
	}
	return n
}
