package node_cmd

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	mathrand "math/rand"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/vrf"
	"github.com/spf13/cobra"
)

// `proxi node mine` is the fair-launch mining tool. It repeatedly consumes the single mine chain UTXO, builds a valid transition
// (successor mine output + sig-locked payout + tag-along), searches a nonce
// whose ECVRF output (RFC 9381) under the wallet key, over predecessor ID ||
// slot || nonce, ends in >= K(M) trailing zero bits, and submits it with the
// proof in the mine output's unlock parameters.
//
// An attempt is one VRF output: a hash-to-curve plus one variable-base scalar
// multiplication under the wallet key, nothing else. The transaction is built
// once per target and touched only for the winner, which gets the completed
// proof and the signature. The VRF output is unique per (key, message), so
// there is no free variable to grind more cheaply, and the key must be present
// for every attempt: the work is key-bound and cannot be pooled or delegated
// without handing over the wallet key. It is not GPU- or ASIC-resistant.
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
// merely for being ours. See kb/archive/shipped/mining_tx_streaming.md.
//
// Everything the miner does against the node is retried: it is a long-running
// process and must survive node restarts, API timeouts and transient HTTP
// failures instead of aborting on the first error.
//
// Mining leaves one payout UTXO per confirmed transit and the miner never
// touches it again: putting payouts to work is the wallet's job, done by
// `proxi node consolidate` running on the same profile (kb/consolidate.md).

const (
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
	start    time.Time
	mined    int    // transits solved and submitted
	transits int    // own transits seen confirmed in the LRB
	orphaned int    // own transits dropped when a competing transit confirmed
	minted   uint64 // A * transits
	attempts uint64 // cumulative PoW attempts across all transits
}

func initMineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mine",
		Short: "mine the fair-launch mine chain: build, solve and submit mine transitions in a loop",
		Args:  cobra.NoArgs,
		Run:   runMineCmd,
	}
	cmd.Flags().Int("workers", runtime.NumCPU(), "parallel mining workers")
	cmd.Flags().Float64("max-hashrate-khs", 0, "cap on the total hashrate over all workers, in KH/s (0 = unlimited)")
	cmd.Flags().Uint64("nonce-start", 0, "first nonce of every round (0 = a fresh random start per round, so several processes mining under one key search disjoint nonce ranges)")
	cmd.Flags().Int("count", 0, "number of transits to mine (0 = until exhausted or interrupted)")
	cmd.Flags().Int("refetch", 0, "seconds to mine one target before re-stamping it (0 = adaptive to the measured hashrate); a target is re-stamped in any case once the clock leaves its slot")
	// the miner no longer consolidates; the flag is accepted so that start
	// scripts written for the earlier miner keep working
	cmd.Flags().Bool("disable_consolidation", false, "no effect: the miner only mines, run 'proxi node consolidate' to put the payouts to work")
	_ = cmd.Flags().MarkHidden("disable_consolidation")
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
	maxHashrateKHs, _ := cmd.Flags().GetFloat64("max-hashrate-khs")
	glb.Assertf(maxHashrateKHs >= 0, "--max-hashrate-khs must not be negative")
	count, _ := cmd.Flags().GetInt("count")
	refetchSec, _ := cmd.Flags().GetInt("refetch")
	nonceStart, _ := cmd.Flags().GetUint64("nonce-start")
	extraStreams, _ := cmd.Flags().GetStringSlice("stream")
	noStream, _ := cmd.Flags().GetBool("no-stream")
	if cmd.Flags().Changed("disable_consolidation") {
		glb.Infof("--disable_consolidation has no effect: the miner only mines; run 'proxi node consolidate' on this profile to put the payouts to work")
	}

	walletData := glb.GetWalletData()
	consts := glb.GetLedgerConstants()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	prover, err := vrf.NewProver(walletData.PrivateKey)
	glb.AssertNoError(err)

	m := &miner{
		consts:        consts,
		lib:           glb.GetTxLibrary(),
		c:             glb.GetClient(),
		wallet:        walletData,
		holderID:      base.HolderIDFromED25519PrivateKey(walletData.PrivateKey),
		prover:        prover,
		tagAlongSeqID: *tagAlongSeqID,
		workers:       workers,
		maxHashrate:   maxHashrateKHs * 1000,
		nonceStart:    nonceStart,
		window:        time.Duration(refetchSec) * time.Second,
	}
	m.st.start = time.Now()

	// the tag-along fee of a mine transit is fixed by the ledger; a sequencer
	// asking more than that never picks a transit up
	m.fee = consts.MineTagAlongFee
	requiredFee, err := retryCall("required tag-along fee", 0, func() (uint64, error) {
		return glb.GetRequiredTagAlongFee(m.tagAlongSeqID)
	})
	glb.AssertNoError(err)
	glb.Assertf(requiredFee <= m.fee,
		"sequencer %s requires a tag-along fee of %s, above the fixed mine transit fee %s: it would never take a transit",
		m.tagAlongSeqID.StringShort(), util.Th(requiredFee), util.Th(m.fee))

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
	if own := glb.NodeAPIURL(); own != "" {
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
	glb.Infof(" VRF-bound proof-of-work miner for the fair-launch mine chain.")
	glb.Infof(" Each transit mints a fixed reward A by finding a nonce whose")
	glb.Infof(" VRF output under the wallet key ends in >= K trailing zero")
	glb.Infof(" bits. Every attempt needs the key, so the work cannot be")
	glb.Infof(" pooled or delegated. It is not GPU- or ASIC-resistant.")
	glb.Infof(" K does not depend on the step length; the chain retargets")
	glb.Infof(" K by one bit per transit to hold the pace.")
	glb.Infof("----------------------------------------------------------")
	glb.Infof(" miner account : %s", m.wallet.Account.String())
	glb.Infof(" payouts       : left on sigLock outputs as mined; run 'proxi node consolidate' on this profile to put them to work")
	a := m.currentA()
	glb.Infof(" reward A      : %s  (payout %s + tag-along %s)", util.Th(a), util.Th(a-m.fee), util.Th(m.fee))
	glb.Infof(" schedule      : %s flat until slot %d, then +%s per slot",
		util.Th(m.consts.MineAmountBase), m.consts.MineRampStartSlot, util.Th(m.consts.MineAmountPerSlot))
	glb.Infof(" tag-along seq : %s", m.tagAlongSeqID.String())
	glb.Infof(" workers       : %d   difficulty band: [%d, %d]", m.workers, m.consts.MineFloorDifficulty, m.consts.MineMaxDifficulty)
	if m.maxHashrate > 0 {
		glb.Infof(" max hashrate  : %s KH/s", strconv.FormatFloat(m.maxHashrate/1000, 'f', -1, 64))
	}
	if m.nonceStart == 0 {
		glb.Infof(" nonce start   : random per round")
	} else {
		glb.Infof(" nonce start   : %d (fixed)", m.nonceStart)
	}
	glb.Infof(" pace          : one step per slot (min P %d); a bit harder after %d full slots, a bit easier per empty slot",
		m.consts.MineMinPace, m.consts.MineHardenAfter)
	glb.Infof(" settlement    : sequencers settle a slot's transits from tick %d; a round ends there", m.consts.MineSettlementTick())
	if len(streamEndpoints) == 0 {
		glb.Infof(" mining stream : OFF — competing transits are only seen once the LRB confirms them")
	} else {
		glb.Infof(" mining stream : %s", strings.Join(streamEndpoints, ", "))
	}
	glb.Infof("==========================================================")
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
	vrfOutput   []byte // VRF output of the transit that produced this tip (nil for the confirmed root)
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
	consts        *txbuildercore.Constants
	prover        *vrf.Prover // the wallet key, expanded once for the hot loop
	lib           *txbuildercore.Library[any]
	c             *client.APIClient
	wallet        glb.WalletData
	holderID      base.HolderID
	tagAlongSeqID base.ChainID
	fee           uint64 // tag-along fee of the mine tx, fixed by the ledger
	workers       int
	maxHashrate   float64       // cap on attempts/sec over all workers; 0 = unlimited
	nonceStart    uint64        // first nonce of every round; 0 = random per round
	window        time.Duration // fixed mining window; 0 = adaptive

	// abort is set whenever the tip being mined stops being the branch to
	// extend — by a streamed competing transit or by an LRB confirmation — and
	// is polled by the mining workers, so a round whose target is already dead
	// is dropped instead of running to its deadline. Only the loop clears it, at
	// the top of each round, so a signal can never be lost between rounds.
	abort      atomic.Bool
	contested  atomic.Bool  // the loop is grinding a contested slot and reads the tree itself
	difficulty atomic.Int64 // last K, for the totals line and the stall timeout
	hashrate   atomic.Int64 // last measured attempts/sec, for the stall timeout

	// tree is the shared view of the mine chain: the mining loop reads the tip
	// to extend from it, the stream feeds verified transits into it, and the LRB
	// monitor re-roots it. It carries its own lock.
	tree *mineTree

	mu sync.Mutex
	st mineStats
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
		succB, succC := m.consts.MineRetarget(tip.ml.B, tip.ml.C, predSlot, succSlot)
		m.difficulty.Store(int64(k))

		txb, predIdx := m.buildTransit(tip, succSlot, succB, succC)

		window := m.window
		if window <= 0 {
			window = adaptiveRefetchWindow(k, hashrate)
		}
		// A target is settled at its slot's settlement tick: from then on no
		// sequencer takes a transit for it, so the round ends there and the next
		// one stamps the next slot, one bit easier.
		untilSettled := time.Until(m.settlementTime(succSlot))
		if untilSettled <= 0 {
			continue
		}
		if untilSettled < window {
			window = untilSettled
		}
		glb.Infof("mining transit #%d%s: R=%s difficulty K=%d target slot %d (pace %d, successor B=%d, full slots %d) ...",
			tip.cc.TransitionCounter+1, m.branchSuffix(tip), util.Th(tip.ml.R), k, succSlot, succSlot-predSlot, succB, succC)
		glb.Verbosef("   window %v (expected ~%s attempts at %s H/s)",
			window.Round(time.Second), util.Th(uint64(math.Ldexp(1, k))), util.Th(uint64(hashrate)))

		roundStart := time.Now()
		proof, nonce, attempts, found := m.mineParallel(tip.oid, succSlot, k, window, nil)
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
		txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, txbuildercore.MineUnlockParams(proof, nonce))
		txb.SignED25519(m.wallet.PrivateKey)
		winBytes := txb.Bytes()
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
		m.grindContested(tip, succSlot, k, succB, succC, &hashrate)
	}
	m.drain()
}

// grindContested keeps the round open on a contested slot
// (kb/mine_conflict_rule.md): while a competitor's transit on the same
// predecessor outranks this miner's best and the slot's settlement is still
// ahead, the same target is searched for a solution with a smaller VRF output,
// and each improvement is submitted. The two own transits conflict on purpose;
// the canonical winner rule picks one. Stops once the own best is the best
// known, at the settlement tick, or when the monitor re-anchors.
func (m *miner) grindContested(tip *mineTip, succSlot uint32, k int, succB, succC uint64, hashrate *float64) {
	m.contested.Store(true)
	defer m.contested.Store(false)
	for {
		beat, own, ok := m.tree.bestOnParent(tip.oid)
		if !ok || own {
			return
		}
		until := time.Until(m.settlementTime(succSlot))
		if until <= 0 {
			return
		}
		m.abort.Store(false)
		txb, predIdx := m.buildTransit(tip, succSlot, succB, succC)
		glb.Infof("   slot %d is contested; searching for a smaller VRF output for %v more ...", succSlot, until.Round(time.Second))
		roundStart := time.Now()
		proof, nonce, attempts, found := m.mineParallel(tip.oid, succSlot, k, until, beat)
		*hashrate = updateHashrate(*hashrate, attempts, time.Since(roundStart))
		m.hashrate.Store(int64(*hashrate))
		if m.abort.Load() {
			return
		}
		if !found {
			continue
		}
		txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, txbuildercore.MineUnlockParams(proof, nonce))
		txb.SignED25519(m.wallet.PrivateKey)
		winBytes := txb.Bytes()
		txid, err := txbuildercore.TxIDFromBytes(winBytes)
		glb.AssertNoError(err)
		glb.Infof("   IMPROVED transit #%d in %s attempts; submitting %s", tip.cc.TransitionCounter+1, util.Th(attempts), txid.StringShort())
		if !m.submit(winBytes, tip.data) {
			return
		}
		m.acceptTransit(tip, winBytes, true)
	}
}

// settlementTime is the wall-clock moment the sequencers start settling the
// slot's mine transits; a solution found later reaches none of them in time.
func (m *miner) settlementTime(slot uint32) time.Time {
	return m.consts.ClockTime(base.T(slot, m.consts.MineSettlementTick()))
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
// for display; a transit under construction takes A from its own successor slot
// instead.
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
// instead. See kb/archive/shipped/mining-bias.md.
func (m *miner) successorSlot(predSlot uint32) uint32 {
	succSlot := predSlot + uint32(m.consts.MineMinPace)
	now := m.nowSlot()
	if now > succSlot {
		succSlot = now
	}
	// past the settlement tick no sequencer takes a transit for this slot any more
	if succSlot == now && time.Now().After(m.settlementTime(now)) {
		succSlot = now + 1
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

// onConfirmedTip re-roots the tree on a confirmed tip and accounts for any
// transits of ours that settled. What to do with the payouts is the treasury
// loop's business, not this goroutine's.
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
		m.mu.Unlock()

		glb.Infof("   confirmed transit #%d %s (%d of ours)",
			tip.cc.TransitionCounter, tip.oid.StringShort(), ownConfirmed)
		m.printTotals()
	}

	// re-rooting may have dropped the branch the loop was extending
	if m.tree.bestTip().oid != wasMiningOn || m.tree.superseded() {
		m.abort.Store(true)
	}
	return verdict
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

// printTotals emits the run-wide totals line after a confirmed transit.
func (m *miner) printTotals() {
	_, inFlight, tracked, orphaned := m.tree.stats()

	m.mu.Lock()
	defer m.mu.Unlock()
	up := time.Since(m.st.start)
	avg := uint64(0)
	if s := up.Seconds(); s > 0 {
		avg = uint64(float64(m.st.attempts) / s)
	}
	glb.Infof("   totals: confirmed %d (+%d in flight, %d orphaned, %d tracked) | minted %s | K=%d | attempts %s | avg %s H/s | uptime %s",
		m.st.transits, inFlight, orphaned, tracked, util.Th(m.st.minted), m.difficulty.Load(), util.Th(m.st.attempts), util.Th(avg), up.Round(time.Second))
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

// buildTransit assembles one valid mine transition against the given tip,
// complete except for the lock unlock parameters (proof || nonce) and the
// signature, which the winning attempt supplies. The successor (index 0) keeps
// the balance, mints A as inflation, decrements R by A and carries the
// retargeted B and C; A is read off the successor slot, which is what the constraint
// validates against; the payout (index 1) is sig-locked to the signer (mineLock
// requires payout holder == tx signer); the tag-along (index 2) pays the fee.
func (m *miner) buildTransit(tip *mineTip, succSlot uint32, succB, succC uint64) (*txbuildercore.TxBuilder, byte) {
	a := m.consts.MineAmountAtSlot(succSlot)
	succLockBin, err := m.lib.NewMineLock(tip.ml.R-a, succB, succC)
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
	txb.SetTimestamp(base.T(succSlot, 1))
	txb.ComputeInputCommitment()
	return txb, predIdx
}

// mineWorker is one goroutine's view of a target: the shared prover and the
// fixed part of the VRF message. attempt computes the VRF output for one nonce
// and returns its trailing-zero-bit count plus what completes the proof.
type mineWorker struct {
	prover *vrf.Prover
	pred   base.OutputID
	slot   uint32
}

func (w *mineWorker) attempt(n uint64) (int, []byte, [txbuildercore.MineNonceLen]byte, *vrf.ProofState) {
	var nonce [txbuildercore.MineNonceLen]byte
	binary.BigEndian.PutUint64(nonce[:], n)
	beta, st, err := w.prover.Output(txbuildercore.MineVRFMessage(w.pred, w.slot, nonce))
	glb.AssertNoError(err)
	return trailingZeroBits(beta), beta, nonce, st

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

// mineParallel runs the configured workers against one target until a VRF
// output reaches targetK trailing zero bits and, when beat is given, is
// smaller than it, or maxDur elapses, or the monitor aborts the round. Prints a
// live attempts/hashrate line and folds the attempt total into the stats. On
// success returns the completed proof and its nonce.
func (m *miner) mineParallel(pred base.OutputID, succSlot uint32, targetK int, maxDur time.Duration, beat []byte) (proof []byte, nonce [txbuildercore.MineNonceLen]byte, attempts uint64, found bool) {
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

	// Workers step through disjoint nonce residues from a common start. A random
	// start per round keeps separate processes mining under the same key from
	// retrying each other's nonces: the VRF output is fixed by key, predecessor,
	// slot and nonce, so the same nonce is the same attempt wherever it runs.
	base := m.nonceStart
	if base == 0 {
		base = mathrand.Uint64()
	}
	// Under a hashrate cap every worker is paced against its own share of it,
	// at the point where it checks the stop conditions anyway. The batch between
	// two checks is then sized to ~100ms of capped work, so that a worker never
	// sleeps long and keeps reacting to an abort.
	perWorker := m.maxHashrate / float64(m.workers)
	batch := uint64(1024)
	if perWorker > 0 {
		batch = min(batch, max(1, uint64(perWorker/10)))
	}
	var wg sync.WaitGroup
	for w := 0; w < m.workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			mw := &mineWorker{prover: m.prover, pred: pred, slot: succSlot}
			n := seed
			var local, flushed uint64
			for {
				if local%batch == 0 {
					atomic.AddUint64(&att, local-flushed) // publish progress for the ticker
					flushed = local
					if atomic.LoadInt32(&foundFlag) != 0 || m.abort.Load() || time.Now().After(deadline) {
						break
					}
					if perWorker > 0 {
						// wait for the moment this many attempts are due, never past the deadline
						due := start.Add(time.Duration(float64(local) / perWorker * float64(time.Second)))
						time.Sleep(min(time.Until(due), time.Until(deadline)))
					}
				}
				n += uint64(m.workers) // disjoint nonce spaces per worker
				tz, beta, nc, st := mw.attempt(n)
				local++
				if tz >= targetK && (beat == nil || bytes.Compare(beta, beat) < 0) {
					if atomic.CompareAndSwapInt32(&foundFlag, 0, 1) {
						pi, err := m.prover.ProofFor(st)
						glb.AssertNoError(err)
						mu.Lock()
						proof, nonce = pi, nc
						mu.Unlock()
					}
					break
				}
			}
			atomic.AddUint64(&att, local-flushed)
		}(base + uint64(w))
	}
	wg.Wait()
	close(done)
	fmt.Printf("\r%70s\r", "") // clear the progress line

	total := atomic.LoadUint64(&att)
	m.mu.Lock()
	m.st.attempts += total
	m.mu.Unlock()
	return proof, nonce, total, atomic.LoadInt32(&foundFlag) != 0
}

// trailingZeroBits counts zero bits at the least-significant end of the VRF
// output — the same suffix-hashcash definition the mineLock PoW check enforces.
func trailingZeroBits(h []byte) int {
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
