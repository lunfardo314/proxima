package node_cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/bits"
	mathrand "math/rand"
	"runtime"
	"sort"
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
	"golang.org/x/crypto/blake2b"
)

// `proxi node mine` is the fair-launch mining tool (see claude/fairlaunch.md).
// It repeatedly consumes the single mine chain UTXO, builds a valid transition
// (successor mine output + sig-locked payout + tag-along), searches a
// proof-of-signing-work nonce so the whole signed tx hashes to >= K(M) trailing
// zero bits, submits it and waits for inclusion — then repeats against the
// advanced chain.
//
// The mine tx layout is constant across nonce attempts; only the nonce (in the
// open lock's unlock params) and the resulting signature change. So each target
// is compiled ONCE into two byte templates whose placeholder offsets are
// recorded (essence -> txID, full tx -> PoW hash); the hot loop only patches
// those ranges. Because the PoW hash covers the signature, every attempt costs
// one ed25519 sign — the work is CPU-egalitarian (pool/ASIC-hostile).
//
// After each mine tx becomes LRB-confirmed the miner optionally acts on the
// accumulated payouts, per --mode:
//   - consolidate (default): sweep all payout UTXOs into one sigLock output
//   - delegate: every C confirmed transits, delegate the accumulated balance
//     to a random alive sequencer
//   - stash: leave payouts untouched
//
// The follow-up consolidation/delegation tx is fire-and-forget (submitted, not
// awaited); the next transit builds only on the confirmed mine chain output.

const (
	modeConsolidate = "consolidate"
	modeDelegate    = "delegate"
	modeStash       = "stash"

	// safety bound on how long a solved mine tx is awaited before assuming it
	// was superseded (another miner won transit N) and re-fetching the chain.
	mineInclusionTimeout = 90 * time.Second

	// required inflation cut (promille) for delegations the miner creates.
	mineDelegationCut = uint16(900)
)

// mineStats accumulates run-wide totals for the periodic totals line.
type mineStats struct {
	start          time.Time
	transits       int    // confirmed mine transits
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
	cmd.Flags().Uint32("pace", 0, "fixed pace M in slots (0 = adaptive: target the current ledger slot)")
	cmd.Flags().Int("retarget", 10, "seconds to mine a fixed target before re-fetching and re-adapting difficulty")
	cmd.Flags().Uint64("fee", 0, "tag-along fee in motes (0 = configured/sequencer minimum; capped at 1% of A)")
	cmd.Flags().String("mode", modeConsolidate, "post-confirmation mode: consolidate | delegate | stash")
	cmd.Flags().Int("per", 1, "delegate mode: delegate the balance every C confirmed transits (C>=1)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runMineCmd(cmd *cobra.Command, _ []string) {
	workers, _ := cmd.Flags().GetInt("workers")
	if workers < 1 {
		workers = 1
	}
	count, _ := cmd.Flags().GetInt("count")
	fixedPace, _ := cmd.Flags().GetUint32("pace")
	retargetSec, _ := cmd.Flags().GetInt("retarget")
	feeFlag, _ := cmd.Flags().GetUint64("fee")
	mode, _ := cmd.Flags().GetString("mode")
	perC, _ := cmd.Flags().GetInt("per")

	glb.Assertf(mode == modeConsolidate || mode == modeDelegate || mode == modeStash,
		"invalid --mode %q: expected %s | %s | %s", mode, modeConsolidate, modeDelegate, modeStash)
	if perC < 1 {
		perC = 1
	}

	walletData := glb.GetWalletData()
	minerPriv := walletData.PrivateKey
	minerHolderID := base.HolderIDFromED25519PrivateKey(minerPriv)
	consts := glb.GetLedgerConstants()
	lib := glb.GetTxLibrary()
	c := glb.GetClient()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	a := consts.MineAmount
	e := int(consts.MineFloorDifficulty)
	p := uint32(consts.MineMinPace)

	// tag-along fee for the mine tx: at least the sequencer minimum, never above
	// 1% of A (the mineLock cap). Follow-up consolidation/delegation txs use the
	// plain required fee (mineActionFee) — the 1% cap is a mineLock rule only.
	fee := feeFlag
	if fee == 0 {
		fee = glb.GetTagAlongFee()
		seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
		glb.AssertNoError(err)
		if seqMinFee > fee {
			fee = seqMinFee
		}
	}
	if feeCap := a / 100; fee > feeCap {
		glb.Infof("tag-along fee %s exceeds the 1%% cap %s; clamping to the cap", util.Th(fee), util.Th(feeCap))
		fee = feeCap
	}
	actionFee, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	// banner
	glb.Infof("")
	glb.Infof("================= PROXIMA BOOTSTRAP MINER =================")
	glb.Infof(" Proof-of-signing-work miner for the fair-launch mine chain.")
	glb.Infof(" Each transit mints a fixed reward A by finding a nonce whose")
	glb.Infof(" signed-tx hash ends in >= K trailing zero bits. The work")
	glb.Infof(" covers the signature, so every attempt costs one ed25519")
	glb.Infof(" sign — CPU-egalitarian, pool/ASIC-hostile.")
	glb.Infof("----------------------------------------------------------")
	glb.Infof(" miner account : %s", walletData.Account.String())
	glb.Infof(" mode          : %s%s", mode, delegateModeSuffix(mode, perC))
	glb.Infof(" reward A      : %s  (payout %s + tag-along %s)", util.Th(a), util.Th(a-fee), util.Th(fee))
	glb.Infof(" tag-along seq : %s", tagAlongSeqID.String())
	glb.Infof(" workers       : %d   difficulty floor E: %d   min pace P: %d", workers, e, p)
	glb.Infof("==========================================================")

	st := &mineStats{start: time.Now()}
	delegateAccum := 0 // confirmed transits since the last delegation

	for count == 0 || st.transits < count {
		oData, lrbid, err := c.GetChainOutputData(base.MineChainID)
		if err != nil {
			glb.Infof("cannot fetch mine chain output: %v", err)
			return
		}
		predOut, err := txbuildercore.OutputFromBytes(oData.Data)
		glb.AssertNoError(err)
		predML, err := lib.ParseMineLock(predOut.MustConstraintAt(txbuildercore.ConstraintIndexLock))
		glb.AssertNoError(err)
		if predML.R < a {
			glb.Infof("mine chain is exhausted: remaining mintable %s < A %s", util.Th(predML.R), util.Th(a))
			return
		}
		predCC, err := lib.ParseChainConstraint(predOut.MustConstraintAt(txbuildercore.ConstraintIndexChain))
		glb.AssertNoError(err)
		predBalance, err := txbuildercore.DecodeTokenBalance(oData.Data)
		glb.AssertNoError(err)
		predSlot := oData.ID.Timestamp().Slot

		// pace M: fixed (flag) or adaptive — target the current ledger slot, which
		// yields the lowest currently-available difficulty (a stalled chain ages
		// into an easier target). Never below the minimum pace P.
		m := fixedPace
		if m == 0 {
			if nowSlot := glb.GetLedgerTimeNow().Slot; nowSlot > predSlot+p {
				m = nowSlot - predSlot
			} else {
				m = p
			}
		}
		if m < p {
			m = p
		}
		succSlot := predSlot + m
		k := int(predML.B) - int(m-p) // K(M) = max(B - (M - P), E)
		if k < e {
			k = e
		}

		tmpl := buildMineTemplate(lib, minerPriv, minerHolderID, *tagAlongSeqID,
			oData.Data, oData.ID, predML, predCC, predBalance, predSlot, succSlot, a, fee)

		glb.PrintLRB(&lrbid)
		glb.Infof("mining transit #%d: R=%s B=%d pace M=%d difficulty K=%d target slot %d ...",
			predCC.TransitionCounter+1, util.Th(predML.R), predML.B, m, k, succSlot)

		winBytes, attempts, found := mineParallel(tmpl, workers, k, time.Duration(retargetSec)*time.Second, st)
		if !found {
			glb.Infof("   no solution in %ds after %s attempts; re-fetching and re-adapting difficulty",
				retargetSec, util.Th(attempts))
			continue
		}
		txid, err := txbuildercore.TxIDFromBytes(winBytes)
		glb.AssertNoError(err)
		glb.Infof("   SOLVED transit #%d in %s attempts; submitting %s",
			predCC.TransitionCounter+1, util.Th(attempts), txid.StringShort())
		if err = glb.SubmitAndDisplay(winBytes, oData.Data); err != nil {
			glb.Infof("   submit failed (mine chain likely advanced under us): %v", err)
			continue
		}
		if !waitMineInclusion(txid) {
			continue // superseded — re-fetch and mine the next transit
		}

		st.transits++
		st.minted += a
		delegateAccum++

		// snapshot the miner account (confirmed spendable payouts) once — drives
		// both the totals line and the post-confirmation action.
		snapOuts, snapTotal := minerAccountSnapshot(walletData.Account)

		switch mode {
		case modeConsolidate:
			consolidateMinerAccount(walletData, *tagAlongSeqID, actionFee, snapOuts, st)
		case modeDelegate:
			if delegateAccum >= perC {
				if delegateMinerAccount(walletData, consts, *tagAlongSeqID, actionFee, snapOuts, snapTotal, st) {
					delegateAccum = 0
				}
			}
		case modeStash:
		}

		st.printTotals(snapTotal, len(snapOuts), k)
	}
	glb.Infof("done: mined %d transit(s) in %s", st.transits, time.Since(st.start).Round(time.Second))
}

func delegateModeSuffix(mode string, perC int) string {
	if mode != modeDelegate {
		return ""
	}
	return fmt.Sprintf(" (every %d confirmed transit(s), cut %d promille)", perC, mineDelegationCut)
}

// waitMineInclusion polls the LRB for the solved mine tx until it reaches the
// target inclusion depth (success) or the safety timeout elapses (assume
// superseded). Prints a compact live line.
func waitMineInclusion(txid base.TransactionID) bool {
	c := glb.GetClient()
	depth := glb.GetTargetInclusionDepth()
	start := time.Now()
	for {
		lrbid, foundAtDepth, err := c.CheckTransactionIDInLRB(txid, depth)
		glb.AssertNoError(err)
		el := int(time.Since(start).Seconds())
		if foundAtDepth >= depth {
			fmt.Printf("\r   confirmed %s at depth %d (%ds)                                  \n",
				txid.StringShort(), foundAtDepth, el)
			return true
		}
		d := foundAtDepth
		if d < 0 {
			d = 0
		}
		fmt.Printf("\r   waiting for confirmation of %s ... %ds (LRB %s, depth %d/%d)   ",
			txid.StringShort(), el, lrbid.StringShort(), d, depth)
		if time.Since(start) > mineInclusionTimeout {
			fmt.Printf("\n   not confirmed within %s; re-fetching chain (likely superseded)\n", mineInclusionTimeout)
			return false
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

// minerAccountSnapshot returns the confirmed, spendable sigLock payouts of the
// miner account and their total balance.
func minerAccountSnapshot(account ledger.Controller) ([]*ledger.OutputWithID, uint64) {
	outs, _, total, err := glb.GetClient().GetSpendableOutputs(account, client.SpendableOutputsParams{
		TargetSlot: glb.GetLedgerTimeNow().Slot,
	})
	glb.AssertNoError(err)
	return outs, total
}

// consolidateMinerAccount sweeps all payout UTXOs into one sigLock output back
// to the miner (fire-and-forget). No-op with fewer than two outputs.
func consolidateMinerAccount(
	walletData glb.WalletData,
	tagAlongSeqID base.ChainID,
	fee uint64,
	outs []*ledger.OutputWithID,
	st *mineStats,
) {
	if len(outs) < 2 {
		return
	}
	// cap to the attachment-cost budget; keep the largest.
	sort.Slice(outs, func(i, j int) bool {
		return outs[i].Output.TokenBalance() > outs[j].Output.TokenBalance()
	})
	if len(outs) > defaultMaxNumberOfInputs {
		outs = outs[:defaultMaxNumberOfInputs]
	}
	txBytes, txid, consumed, err := makeClaimingCompactTransaction(
		walletData.PrivateKey, outs, tagAlongSeqID, fee, glb.GetLedgerTimeNow().Slot)
	if err != nil {
		glb.Infof("   consolidation build failed: %v", err)
		return
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   consolidation submit failed: %v", err)
		return
	}
	st.consolidations++
	glb.Infof("   consolidated %d payout UTXO(s) -> %s (submitted, not awaited)", len(consumed), txid.StringShort())
}

// delegateMinerAccount delegates the accumulated payout balance to a random
// alive sequencer (fire-and-forget). Returns true if a delegation was
// submitted; false if deferred (nothing spendable, below the inflatable
// minimum, or no alive sequencer) so the caller keeps accumulating.
func delegateMinerAccount(
	walletData glb.WalletData,
	consts *txbuildercore.Constants,
	tagAlongSeqID base.ChainID,
	fee uint64,
	outs []*ledger.OutputWithID,
	total uint64,
	st *mineStats,
) bool {
	if len(outs) == 0 || total <= fee {
		return false
	}
	// cap to the attachment-cost budget; keep the largest.
	sort.Slice(outs, func(i, j int) bool {
		return outs[i].Output.TokenBalance() > outs[j].Output.TokenBalance()
	})
	if len(outs) > defaultMaxNumberOfInputs {
		outs = outs[:defaultMaxNumberOfInputs]
	}
	sumIn := uint64(0)
	for _, o := range outs {
		sumIn += o.Output.TokenBalance()
	}
	amount := sumIn - fee

	c := glb.GetClient()
	minAmt := minDelegationAmount(consts, c)
	if amount < minAmt {
		glb.Infof("   delegation deferred: balance %s < minimum inflatable %s (accumulating)",
			util.Th(amount), util.Th(minAmt))
		return false
	}
	seqID, err := chooseRandomAliveSequencer()
	if err != nil {
		glb.Infof("   delegation deferred: %v", err)
		return false
	}
	ti, err := c.GetSequencerTargetInfo(seqID)
	if err != nil {
		glb.Infof("   delegation deferred: cannot get target info for %s: %v", seqID.StringShort(), err)
		return false
	}

	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
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
			glb.AssertNoError(txb.PutUnlockReference(byte(i), txbuildercore.ConstraintIndexLock, 0))
		}
	}

	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs)

	delegationOut, err := lib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
		Amount:                amount,
		MasterID:              walletHolderID,
		Target:                seqID,
		MaxFrozenEpochs:       0, // 0 = target's maximum
		RequiredInflationCut:  mineDelegationCut,
		StartSlot:             ts.Slot,
		EpochSlots:            ti.EpochDurationSlots,
		TargetMaxFrozenEpochs: byte(ti.MaxFrozenEpochs),
	})
	glb.AssertNoError(err)
	delegationIdx := txb.ProduceOutput(delegationOut.Bytes())

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, fee, tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)
	delegationOid, err := base.NewOutputID(txid, delegationIdx)
	glb.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)

	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   delegation submit failed: %v", err)
		return false
	}
	st.delegations++
	glb.Infof("   delegated %s to sequencer %s, delegation ID %s (submitted, not awaited)",
		util.Th(amount), seqID.StringShort(), delegationID.StringShort())
	return true
}

// minDelegationAmount is the "minimum inflatable" floor for a fresh delegation
// output, projected over a wide slot horizon. Computed server-side via /eval so
// the wallet stays singleton-free (mirrors `proxi node dlg amount`).
func minDelegationAmount(consts *txbuildercore.Constants, c *client.APIClient) uint64 {
	slot := glb.GetLedgerTimeNow().Slot
	inflMin, err := c.EvalU64(0,
		fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			consts.MinimumInflatableAmount0, 0, slot+10000))
	glb.AssertNoError(err)
	return consts.MinimumInflatableAmount0 + inflMin
}

// chooseRandomAliveSequencer picks a uniformly-random sequencer whose latest
// output is recent (within 6 slots of now).
func chooseRandomAliveSequencer() (base.ChainID, error) {
	outs, _, err := glb.GetClient().GetAllSequencerOutputs()
	if err != nil {
		return base.ChainID{}, err
	}
	nowSlot := glb.GetLedgerTimeNow().Slot
	alive := make([]base.ChainID, 0, len(outs))
	for id, out := range outs {
		if out.ID.Slot()+6 >= nowSlot {
			alive = append(alive, id)
		}
	}
	if len(alive) == 0 {
		return base.ChainID{}, fmt.Errorf("no alive sequencer to delegate to")
	}
	return alive[mathrand.Intn(len(alive))], nil
}

// printTotals emits the run-wide totals line after a confirmed transit.
func (st *mineStats) printTotals(heldBalance uint64, heldCount, difficulty int) {
	up := time.Since(st.start)
	avg := uint64(0)
	if s := up.Seconds(); s > 0 {
		avg = uint64(float64(st.attempts) / s)
	}
	glb.Infof("   totals: transits %d | minted %s | held %s in %d UTXO(s) | consol %d / deleg %d | K=%d | attempts %s | avg %s H/s | uptime %s",
		st.transits, util.Th(st.minted), util.Th(heldBalance), heldCount,
		st.consolidations, st.delegations, difficulty, util.Th(st.attempts), util.Th(avg), up.Round(time.Second))
}

// buildMineTemplate assembles one valid mine transition against the current mine
// chain output and returns the compiled PoW template for it. The successor
// (index 0) keeps the balance, mints A as inflation, decrements R by A, carries
// B and rolls the slot ring; the payout (index 1) is sig-locked to the signer
// (mineLock requires payout holder == tx signer); the tag-along (index 2) pays
// the fee. The slot is baked in — only the nonce and signature vary.
func buildMineTemplate(
	lib *txbuildercore.Library[any],
	minerPriv ed25519.PrivateKey,
	minerHolderID base.HolderID,
	tagAlongSeqID base.ChainID,
	predBytes []byte,
	predOID base.OutputID,
	predML *txbuildercore.MineLockView,
	predCC *txbuildercore.ChainConstraintView,
	predBalance uint64,
	predSlot, succSlot uint32,
	a, fee uint64,
) *mineTemplate {
	succLockBin, err := lib.NewMineLock(predML.R-a, predML.B, predSlot, predML.S1, predML.S2)
	glb.AssertNoError(err)
	succChainBin, err := lib.NewChainTransition(base.MineChainID, 0, predCC.OriginSlot,
		predCC.CumulativeChainInflation+a, 0, predCC.TransitionCounter+1, 0)
	glb.AssertNoError(err)
	sb := txbuildercore.NewOutputBuilder()
	sb.PutConstraint(txbuildercore.EncodeAmounts(predBalance, a), txbuildercore.ConstraintIndexAmounts)
	sb.PutConstraint(succLockBin, txbuildercore.ConstraintIndexLock)
	sb.PutConstraint(succChainBin, txbuildercore.ConstraintIndexChain)

	payoutOut, err := txbuildercore.NewSigLockOutput(lib, a-fee, minerHolderID)
	glb.AssertNoError(err)
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, fee, tagAlongSeqID, minerHolderID)
	glb.AssertNoError(err)

	txb := txbuildercore.New(0)
	predIdx := txb.ConsumeOutput(predBytes, predOID)
	txb.ProduceOutput(sb.Output().Bytes())
	txb.ProduceOutput(payoutOut.Bytes())
	txb.ProduceOutput(tagAlongOut.Bytes())
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	ts := base.T(succSlot, 1)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()

	return newMineTemplate(txb, predIdx, minerPriv, ts.Bytes())
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

// mineParallel runs `workers` miners against the template's target until one hash
// reaches targetK trailing zero bits or maxDur elapses. Prints a live
// attempts/hashrate line and folds the attempt total into st. Returns the
// winning full tx bytes on success.
func mineParallel(tmpl *mineTemplate, workers, targetK int, maxDur time.Duration, st *mineStats) (winBytes []byte, attempts uint64, found bool) {
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
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			m := tmpl.newWorker()
			n := seed
			var local, flushed uint64
			for {
				if local&0x3ff == 0 {
					atomic.AddUint64(&att, local-flushed) // publish progress for the ticker
					flushed = local
					if atomic.LoadInt32(&foundFlag) != 0 || time.Now().After(deadline) {
						break
					}
				}
				n += uint64(workers) // disjoint nonce spaces per worker
				tz := m.attempt(n)
				local++
				if tz >= targetK {
					if atomic.CompareAndSwapInt32(&foundFlag, 0, 1) {
						mu.Lock()
						winBytes = append([]byte(nil), m.full...)
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
	st.attempts += total
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
