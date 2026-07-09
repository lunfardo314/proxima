package node_cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"math/bits"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/easyfl/tuples"
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

	// tag-along fee: at least the sequencer minimum, never above 1% of A (the mineLock cap).
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

	glb.Infof("proxi mine — fair-launch proof-of-signing-work miner")
	glb.Infof("   miner account: %s", walletData.Account.String())
	glb.Infof("   mine amount A: %s   payout: %s   tag-along fee: %s to %s",
		util.Th(a), util.Th(a-fee), util.Th(fee), tagAlongSeqID.String())
	glb.Infof("   workers: %d   difficulty floor E: %d   minimum pace P: %d", workers, e, p)

	mined := 0
	for count == 0 || mined < count {
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

		winBytes, attempts, found := mineParallel(tmpl, workers, k, time.Duration(retargetSec)*time.Second)
		if !found {
			glb.Infof("   no solution in %ds after %d attempts; re-fetching and re-adapting difficulty", retargetSec, attempts)
			continue
		}
		glb.Infof("   solved after %d attempts; submitting", attempts)
		if err = glb.SubmitAndDisplay(winBytes, oData.Data); err != nil {
			glb.Infof("   submit failed (mine chain likely advanced under us): %v", err)
			continue
		}
		txid, err := txbuildercore.TxIDFromBytes(winBytes)
		glb.AssertNoError(err)
		if !glb.NoWait() {
			glb.TrackTxInclusion(txid, time.Second)
		}
		mined++
	}
	glb.Infof("done: mined %d transit(s)", mined)
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
// reaches targetK trailing zero bits or maxDur elapses. Returns the winning full
// tx bytes on success.
func mineParallel(tmpl *mineTemplate, workers, targetK int, maxDur time.Duration) (winBytes []byte, attempts uint64, found bool) {
	var att uint64
	var foundFlag int32
	var mu sync.Mutex
	deadline := time.Now().Add(maxDur)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			m := tmpl.newWorker()
			n := seed
			var local uint64
			for {
				if local&0x3ff == 0 {
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
			atomic.AddUint64(&att, local)
		}(uint64(w))
	}
	wg.Wait()
	return winBytes, atomic.LoadUint64(&att), atomic.LoadInt32(&foundFlag) != 0
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
