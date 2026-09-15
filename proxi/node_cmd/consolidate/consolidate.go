// Package consolidate implements `proxi node consolidate`: a permanent process
// on the wallet profile that periodically sweeps the outputs scattered over the
// account and puts what is above a configured minimum back into consensus, as
// a transfer to a sequencer or as a delegation, or at least folds them into a
// single output. Spec: kb/consolidate.md.
//
// The consolidator assumes nothing about what fills the account. Miners are the
// motivating case — every mined transit leaves a payout output behind, and a
// miner written by anyone else never touches it again — but the process only
// ever sees the account, so it works for any wallet.
//
// Every tick reads the account, builds at most one transaction, submits it and
// logs what it did. It never waits for inclusion: consumed outputs stay in the
// node's account snapshot until the transaction settles, so the next ticks
// stand still while any of them is still reported, which is what keeps the
// process from double-spending its own inputs.
package consolidate

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const (
	defaultMinimumBalancePROX = 100
	defaultMaxInputs          = 30
	defaultCompactAt          = 10
	defaultMaxDelegations     = 10

	// how often the account is re-read. A transaction takes several slots to
	// settle, so there is nothing to gain from polling faster.
	tickPeriod = 10 * time.Second

	// how long to wait for a submitted transaction before presuming it dropped
	// (never picked up by the tag-along sequencer, or orphaned) and rebuilding
	// from a fresh snapshot.
	pendingTimeout = 3 * time.Minute

	// how recent a sequencer's latest known milestone must be for it to be
	// handed tokens: a transfer to a sequencer that has stopped would sit
	// unclaimed in a tag-along output until the wallet reclaims it.
	activeSequencerSlots = 3

	// SendToOwn is the send_to_sequencer value naming the wallet's own sequencer.
	SendToOwn = "own"
	// DelegateRandom is the autodelegate value drawing a target on every action.
	DelegateRandom = "random"

	// bounds of the exponential backoff between retries of a node call
	retryBase = 500 * time.Millisecond
	retryMax  = 15 * time.Second
)

func Init() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "consolidate",
		Short: "run permanently: periodically consolidate the wallet's scattered outputs and put what is above the minimum back into consensus",
		Long: `Runs until interrupted. Every 10 seconds it reads the wallet account and, when
there is enough to act on, builds one transaction that consumes up to
max_inputs of the smallest plain sigLock outputs and reclaimable tag-along
outputs. What is above the configured minimum balance is sent to a sequencer
(send_to_sequencer), delegated (autodelegate) or, when neither is configured,
folded into a single output back to the wallet.

Configured in the 'consolidate' section of the wallet profile; every flag
below overrides the profile key of the same name. See kb/consolidate.md.`,
		Args: cobra.NoArgs,
		Run:  run,
	}
	cmd.Flags().Uint64("minimum-balance-prox", defaultMinimumBalancePROX, "balance always kept in the wallet on plain sigLock outputs, in PROX (not motes)")
	cmd.Flags().Int("max-inputs", defaultMaxInputs, "most outputs one consolidating transaction consumes (2-256)")
	cmd.Flags().Int("compact-at", defaultCompactAt, "compact as soon as this many consolidatable outputs have piled up, even with nothing above the minimum to move")
	cmd.Flags().String("send-to-sequencer", "", "'own' sends everything above the minimum to wallet.sequencer_id, a sequencer ID sends it to that sequencer, empty disables")
	cmd.Flags().String("autodelegate", "", "when sending is disabled: 'random' delegates to a sequencer drawn on every action, a sequencer ID delegates to that one, empty disables")
	cmd.Flags().Int("max-delegations", defaultMaxDelegations, "advisory cap on own delegations; at the cap an existing one is topped up")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// config is the effective configuration: the wallet profile's 'consolidate'
// section with the flags of the same names overriding it.
type config struct {
	minimum        uint64 // motes always left on the wallet's sigLock outputs
	maxInputs      int
	compactAt      int
	sendOwn        bool
	sendTo         *base.ChainID // nil = sending disabled
	delegateRandom bool
	delegateTo     *base.ChainID // nil and !delegateRandom = delegation disabled
	maxDelegations int
	cut            uint16 // delegator cut required of a delegation target
}

func (c *config) sendEnabled() bool     { return c.sendTo != nil }
func (c *config) delegateEnabled() bool { return c.delegateRandom || c.delegateTo != nil }

// consolidator holds the run: immutable configuration and node handles, the
// tag-along target and fee as last resolved, plus the outputs consumed by the
// last submitted transaction.
type consolidator struct {
	cfg      config
	consts   *txbuildercore.Constants
	lib      *txbuildercore.Library[any]
	c        *client.APIClient
	wallet   glb.WalletData
	holderID base.HolderID
	// floor is the storage deposit of the sigLock output the wallet keeps: the
	// smallest such output the ledger accepts, so nothing below it is ever built.
	floor uint64

	// The tag-along target and fee are resolved before every transaction, not
	// once: a permanent process outlives a sequencer's activity and a fee
	// setting, and a transaction built on stale values is never picked up.
	tagAlongRandom   bool // draw the target among the active sequencers each time
	tagAlongSeqID    base.ChainID
	tagAlongFee      uint64 // fee of the wallet's own compaction and delegation transactions
	tagAlongDeferred bool   // logged once while no target is usable

	pending      []base.OutputID
	pendingSince time.Time
}

func run(cmd *cobra.Command, _ []string) {
	walletData := glb.GetWalletData()
	consts := glb.GetLedgerConstants()
	cfg := readConfig(cmd, consts)

	k := &consolidator{
		cfg:            cfg,
		consts:         consts,
		lib:            glb.GetTxLibrary(),
		c:              glb.GetClient(),
		wallet:         walletData,
		holderID:       base.HolderIDFromED25519PrivateKey(walletData.PrivateKey),
		tagAlongRandom: viper.GetString("tag_along.sequencer_id") == glb.TagAlongSequencerRandom,
	}
	if !k.tagAlongRandom {
		seqID := glb.GetTagAlongSequencerID() // verified on the ledger
		glb.Assertf(seqID != nil, "tag-along sequencer not specified")
		k.tagAlongSeqID = *seqID
	}
	floor, err := retry("storage deposit of a sigLock output", 0, k.sigLockFloor)
	glb.AssertNoError(err)
	k.floor = floor
	glb.Assertf(cfg.minimum >= floor, "minimum_balance_prox must be at least the storage deposit of a sigLock output, %s motes", util.Th(floor))

	k.checkTargets()
	k.banner()
	for {
		k.tick()
		time.Sleep(tickPeriod)
	}
}

// readConfig resolves each setting from its flag when given, else from the
// profile key. An unparsable sequencer ID disables its mode with a warning
// rather than failing, as the spec asks, but never silently.
func readConfig(cmd *cobra.Command, consts *txbuildercore.Constants) config {
	cfg := config{
		minimum:        uint64Setting(cmd, "minimum-balance-prox", "consolidate.minimum_balance_prox") * consts.SmallestAmountsPerBaseToken,
		maxInputs:      intSetting(cmd, "max-inputs", "consolidate.max_inputs"),
		compactAt:      intSetting(cmd, "compact-at", "consolidate.compact_at"),
		maxDelegations: intSetting(cmd, "max-delegations", "consolidate.max_delegations"),
		cut:            glb.GetMinimumDelegatorCut(),
	}
	glb.Assertf(2 <= cfg.maxInputs && cfg.maxInputs <= 256, "max_inputs must be 2-256, got %d", cfg.maxInputs)
	glb.Assertf(cfg.compactAt >= 2, "compact_at must be >= 2: compacting fewer than two outputs achieves nothing")
	glb.Assertf(cfg.minimum > 0, "minimum_balance_prox must be positive")

	switch v := strings.TrimSpace(stringSetting(cmd, "send-to-sequencer", "consolidate.send_to_sequencer")); v {
	case "":
	case SendToOwn:
		// wallet.sequencer_id is read directly: glb.GetOwnSequencerID falls back
		// to the default sequencer, and 'own' must never mean somebody else's
		ownStr := viper.GetString("wallet.sequencer_id")
		if ownStr == "" {
			glb.Infof("WARNING: send_to_sequencer is '%s' but wallet.sequencer_id is not set: sending to a sequencer is disabled", SendToOwn)
			break
		}
		own, err := base.ChainIDFromHexString(ownStr)
		if err != nil {
			glb.Infof("WARNING: wallet.sequencer_id '%s' is not a chain ID (%v): sending to a sequencer is disabled", ownStr, err)
			break
		}
		cfg.sendOwn, cfg.sendTo = true, &own
	default:
		id, err := base.ChainIDFromHexString(v)
		if err != nil {
			glb.Infof("WARNING: send_to_sequencer '%s' is neither '%s' nor a sequencer ID (%v): sending to a sequencer is disabled", v, SendToOwn, err)
			break
		}
		cfg.sendTo = &id
	}

	switch v := strings.TrimSpace(stringSetting(cmd, "autodelegate", "consolidate.autodelegate")); v {
	case "":
	case DelegateRandom:
		cfg.delegateRandom = true
	default:
		id, err := base.ChainIDFromHexString(v)
		if err != nil {
			glb.Infof("WARNING: autodelegate '%s' is neither '%s' nor a sequencer ID (%v): delegation is disabled", v, DelegateRandom, err)
			break
		}
		cfg.delegateTo = &id
	}
	if cfg.sendEnabled() && cfg.delegateEnabled() {
		glb.Infof("note: autodelegate is ignored while send_to_sequencer is set")
	}
	return cfg
}

func stringSetting(cmd *cobra.Command, flag, key string) string {
	if cmd.Flags().Changed(flag) {
		v, _ := cmd.Flags().GetString(flag)
		return v
	}
	return viper.GetString(key)
}

func intSetting(cmd *cobra.Command, flag, key string) int {
	if cmd.Flags().Changed(flag) || !viper.IsSet(key) {
		v, _ := cmd.Flags().GetInt(flag)
		return v
	}
	return viper.GetInt(key)
}

func uint64Setting(cmd *cobra.Command, flag, key string) uint64 {
	if cmd.Flags().Changed(flag) || !viper.IsSet(key) {
		v, _ := cmd.Flags().GetUint64(flag)
		return v
	}
	return viper.GetUint64(key)
}

// checkTargets verifies at startup that a configured explicit target exists
// on the ledger and is a sequencer chain, like the tag-along target. Whether
// the own sequencer is controlled by the wallet is checked before every
// transaction instead, since control can change hands while the process runs.
func (k *consolidator) checkTargets() {
	for _, id := range []*base.ChainID{k.cfg.sendTo, k.cfg.delegateTo} {
		if id == nil {
			continue
		}
		o, err := retry("read sequencer "+id.StringShort(), 3, func() (*ledger.OutputDataWithID, error) {
			o, _, err := k.c.GetChainOutputData(*id)
			return o, err
		})
		glb.Assertf(err == nil, "cannot resolve sequencer %s: %v", id.String(), err)
		glb.Assertf(o.ID.IsSequencerTransaction(), "%s is not a sequencer chain (chain output %s)", id.StringShort(), o.ID.StringShort())
	}
}

func (k *consolidator) banner() {
	glb.Infof("")
	glb.Infof("================= PROXIMA WALLET CONSOLIDATOR =================")
	glb.Infof(" account          : %s", k.wallet.Account.String())
	glb.Infof(" minimum balance  : %s (kept on sigLock outputs)", util.Th(k.cfg.minimum))
	glb.Infof(" acts when        : consolidatable total >= %s, or >= %d consolidatable outputs",
		util.Th(2*k.cfg.minimum), k.cfg.compactAt)
	glb.Infof(" inputs per tx    : up to %d, smallest first", k.cfg.maxInputs)
	switch {
	case k.cfg.sendOwn:
		glb.Infof(" above minimum    : sent to own sequencer %s (control verified before each transfer)", k.cfg.sendTo.String())
	case k.cfg.sendEnabled():
		glb.Infof(" above minimum    : sent to sequencer %s", k.cfg.sendTo.String())
	case k.cfg.delegateRandom:
		glb.Infof(" above minimum    : delegated to a random active sequencer leaving >= %d promille, cap %d delegations", k.cfg.cut, k.cfg.maxDelegations)
	case k.cfg.delegateTo != nil:
		glb.Infof(" above minimum    : delegated to sequencer %s (must leave >= %d promille), cap %d delegations", k.cfg.delegateTo.String(), k.cfg.cut, k.cfg.maxDelegations)
	default:
		glb.Infof(" above minimum    : stays in the wallet, compacted into one output")
	}
	if k.tagAlongRandom {
		glb.Infof(" tag-along        : a random active sequencer, drawn before each transaction")
	} else {
		glb.Infof(" tag-along        : sequencer %s, fee read before each transaction", k.tagAlongSeqID.StringShort())
	}
	glb.Infof(" storage floor    : %s (smallest sigLock output the wallet keeps)", util.Th(k.floor))
	glb.Infof(" tick             : every %v", tickPeriod)
	glb.Infof("===============================================================")
}

// tick is one pass: read, decide, act at most once.
func (k *consolidator) tick() {
	outs, err := k.consolidatable()
	if err != nil {
		glb.Infof("cannot read the wallet account: %v; retrying next tick", err)
		return
	}
	if len(k.pending) > 0 {
		switch {
		case !anyPresent(outs, k.pending):
			glb.Infof("   the last transaction settled")
			k.pending = nil
		case time.Since(k.pendingSince) > pendingTimeout:
			glb.Infof("   the last transaction has not settled in %v; presumed dropped, rebuilding from a fresh snapshot", pendingTimeout)
			k.pending = nil
		default:
			return
		}
	}
	p := planConsolidation(outs, k.cfg.minimum, k.cfg.maxInputs, k.cfg.compactAt, k.consts.SmallestAmountsPerBaseToken, k.floor)
	if p == nil {
		glb.Verbosef("   %d consolidatable output(s) holding %s: nothing to do", len(outs), util.Th(sumBalance(outs)))
		return
	}
	if !k.refreshTagAlong() {
		return
	}
	glb.Verbosef("   %d consolidatable output(s) holding %s; consuming %d holding %s: keep %s, move %s",
		len(outs), util.Th(sumBalance(outs)), len(p.inputs), util.Th(p.consumed), util.Th(p.kept), util.Th(p.moved))

	var consumed []base.OutputID
	switch {
	case p.moved > 0 && k.cfg.sendEnabled():
		consumed = k.sendToSequencer(p)
	case p.moved > 0 && k.cfg.delegateEnabled():
		consumed = k.delegate(p)
	}
	if consumed == nil {
		consumed = k.compact(p)
	}
	if len(consumed) > 0 {
		k.pending, k.pendingSince = consumed, time.Now()
	}
}

// consolidatable is what the process may sweep: the wallet's plain sigLock
// outputs and the tag-along outputs it sent whose window has passed. Both are
// filtered through the shared spendable classifier at the current slot, as
// `proxi node compact` does; everything else it would sweep (sendWithDeadline
// reclaims and accepts) is a one-off decision left to that command.
func (k *consolidator) consolidatable() ([]*ledger.OutputWithID, error) {
	slot := k.nowSlot()
	outs, err := retry("read the wallet account", 3, func() ([]*ledger.OutputWithID, error) {
		o, _, _, err := k.c.GetSpendableOutputs(k.wallet.Account, client.SpendableOutputsParams{
			IncludeConditionalLocks: true,
			TargetSlot:              slot,
		})
		return o, err
	})
	if err != nil {
		return nil, err
	}
	ret := make([]*ledger.OutputWithID, 0, len(outs))
	for _, o := range outs {
		cls, err := txbuildercore.ClassifySpendable(k.lib, o.Output.Bytes(), o.ID.Slot(), k.holderID, slot, k.consts.TagAlongSlots)
		if err != nil || cls != txbuildercore.SpendSimple {
			glb.Verbosef("   skipping %s: class %d, %v", o.ID.StringShort(), cls, err)
			continue
		}
		kind, err := k.lib.ClassifyLock(o.Output.Bytes(), k.holderID)
		if err != nil || (kind != txbuildercore.LockKindSig && kind != txbuildercore.LockKindTagAlongSender) {
			glb.Verbosef("   skipping %s: lock kind %d, %v", o.ID.StringShort(), kind, err)
			continue
		}
		ret = append(ret, o)
	}
	return ret, nil
}

// plan is one tick's decision: which outputs to consume and how their total
// splits between what the wallet keeps and what moves out.
type plan struct {
	inputs   []*ledger.OutputWithID // smallest first
	consumed uint64                 // total of inputs
	kept     uint64                 // returns to the wallet on one sigLock output
	moved    uint64                 // above the minimum, before any fee
}

// planConsolidation applies the rules of kb/consolidate.md to the
// consolidatable set. It acts when the total reaches twice the minimum (there
// is at least a minimum's worth to move) or when at least compactAt outputs
// have piled up (worth folding whatever they hold); in the second case alone
// nothing moves. The minimum is a property of the whole account, so outputs
// left unconsumed count toward it. A movable amount under one base token is
// not worth sending and stays; a remainder under floor, the storage deposit of
// the output that keeps it, cannot be an output and is folded into what
// moves. Returns nil when the transaction would do nothing: a single input
// going straight back to the wallet, or dust that cannot yet form one output.
func planConsolidation(outs []*ledger.OutputWithID, minimum uint64, maxInputs, compactAt int, oneBaseToken, floor uint64) *plan {
	total := sumBalance(outs)
	if total < 2*minimum && len(outs) < compactAt {
		return nil
	}
	sort.SliceStable(outs, func(i, j int) bool {
		return outs[i].Output.TokenBalance() < outs[j].Output.TokenBalance()
	})
	inputs := outs
	if len(inputs) > maxInputs {
		inputs = inputs[:maxInputs]
	}
	consumed := sumBalance(inputs)
	unconsumed := total - consumed

	kept := uint64(0)
	if minimum > unconsumed {
		kept = min(minimum-unconsumed, consumed)
	}
	moved := consumed - kept
	if total < 2*minimum {
		kept, moved = consumed, 0
	}
	if moved > 0 && moved < oneBaseToken {
		kept, moved = consumed, 0
	}
	if kept > 0 && kept < floor {
		if moved == 0 {
			return nil
		}
		kept, moved = 0, consumed
	}
	if moved == 0 && len(inputs) < 2 {
		return nil
	}
	return &plan{inputs: inputs, consumed: consumed, kept: kept, moved: moved}
}

// sendToSequencer moves everything above the minimum to the configured
// sequencer as one tag-along output, which the sequencer takes as it takes any
// fee, so no separate fee output is built; proxi never produces a chainLock
// (see glb.BuildTransferOutput). Returns the consumed IDs, or nil when the
// transfer was not made this tick and the outputs should be compacted instead.
func (k *consolidator) sendToSequencer(p *plan) []base.OutputID {
	target := *k.cfg.sendTo
	if k.cfg.sendOwn {
		if err := k.ownSequencerControlled(target); err != nil {
			glb.Infof("   sending refused: %v", err)
			return nil
		}
	}
	if active, err := k.sequencerActive(target); err != nil {
		glb.Infof("   sending deferred: %v", err)
		return nil
	} else if !active {
		glb.Infof("   sending deferred: sequencer %s has no milestone in the last %d slots", target.StringShort(), activeSequencerSlots)
		return nil
	}
	minFee, err := retry("minimum fee of sequencer "+target.StringShort(), 3, func() (uint64, error) {
		return glb.GetRequiredTagAlongFee(target)
	})
	if err != nil {
		glb.Infof("   sending deferred: %v", err)
		return nil
	}
	if p.moved < minFee {
		glb.Infof("   sending deferred: %s is below the minimum %s sequencer %s takes", util.Th(p.moved), util.Th(minFee), target.StringShort())
		return nil
	}

	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		glb.Infof("   transfer build failed: %v", err)
		return nil
	}
	out, err := txbuildercore.NewTagAlongOutput(k.lib, p.moved, target, k.holderID)
	if err != nil {
		glb.Infof("   transfer build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(out.Bytes())
	if err = k.produceKept(txb, p.kept); err != nil {
		glb.Infof("   transfer build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = glb.SubmitAndDisplay(txb.Bytes(), consumed...); err != nil {
		glb.Infof("   transfer submit failed: %v", err)
		return nil
	}
	glb.Infof("   consolidated %d output(s) holding %s: sent %s to sequencer %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), util.Th(p.moved), target.StringShort(), util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// ownSequencerControlled reads the sequencer's chain output and checks that
// its lock is a plain sigLock held by this wallet: the same read `proxi node
// seq info` shows as the controller lock. Tokens sent to a sequencer end up in
// its chain output, and only its controller can withdraw them.
func (k *consolidator) ownSequencerControlled(seqID base.ChainID) error {
	o, err := retry("read own sequencer", 3, func() (*ledger.OutputDataWithID, error) {
		o, _, err := k.c.GetChainOutputData(seqID)
		return o, err
	})
	if err != nil {
		return fmt.Errorf("cannot read own sequencer %s: %w", seqID.StringShort(), err)
	}
	if !o.ID.IsSequencerTransaction() {
		return fmt.Errorf("wallet.sequencer_id %s is not a sequencer chain", seqID.StringShort())
	}
	kind, err := k.lib.ClassifyLock(o.Data, k.holderID)
	if err != nil {
		return fmt.Errorf("cannot read the lock of sequencer %s: %w", seqID.StringShort(), err)
	}
	if kind != txbuildercore.LockKindSig {
		return fmt.Errorf("sequencer %s (wallet.sequencer_id) is not controlled by this wallet's key", seqID.StringShort())
	}
	return nil
}

// sequencerActive reports whether the latest milestone the node knows of the
// sequencer lies within activeSequencerSlots of now.
func (k *consolidator) sequencerActive(seqID base.ChainID) (bool, error) {
	active, err := k.activeSequencers()
	if err != nil {
		return false, err
	}
	_, ok := active[seqID]
	return ok, nil
}

// activeSequencers is the set of sequencers whose latest milestone known to
// the node lies within activeSequencerSlots of now. Judged in ledger time,
// like the 'random' tag-along target, so it does not depend on how long the
// milestone sat in the node's tippool.
func (k *consolidator) activeSequencers() (map[base.ChainID]struct{}, error) {
	known, err := retry("known sequencer milestones", 3, k.c.GetLastKnownSequencerData)
	if err != nil {
		return nil, err
	}
	nowSlot := k.nowSlot()
	ret := make(map[base.ChainID]struct{}, len(known))
	for seqIDStr, d := range known {
		seqID, err := base.ChainIDFromHexString(seqIDStr)
		if err != nil {
			return nil, fmt.Errorf("cannot parse sequencer ID '%s' reported by the node: %w", seqIDStr, err)
		}
		txid, err := base.TransactionIDFromHexString(d.LatestMilestoneTxID)
		if err != nil {
			return nil, fmt.Errorf("cannot parse latest milestone '%s' of sequencer %s reported by the node: %w",
				d.LatestMilestoneTxID, seqID.StringShort(), err)
		}
		if txid.Slot()+activeSequencerSlots >= nowSlot {
			ret[seqID] = struct{}{}
		}
	}
	return ret, nil
}

// sigLockFloor is the storage deposit of the wallet's sigLock output, sized
// with the widest amount encoding so it holds for any amount.
func (k *consolidator) sigLockFloor() (uint64, error) {
	probe, err := txbuildercore.NewSigLockOutput(k.lib, 1<<62, k.holderID)
	if err != nil {
		return 0, err
	}
	return glb.MinStorageDeposit(probe)
}

// refreshTagAlong resolves the tag-along target and its fee for the
// transaction about to be built: a random target is drawn among the sequencers
// active now, a configured one must be active now. Returns false when no
// target is usable this tick, logging that once until one is again.
func (k *consolidator) refreshTagAlong() bool {
	active, err := k.activeSequencers()
	if err != nil {
		return k.deferTagAlong(err.Error())
	}
	if k.tagAlongRandom {
		ids := make([]base.ChainID, 0, len(active))
		for id := range active {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return k.deferTagAlong(fmt.Sprintf("no sequencer has a milestone in the last %d slots", activeSequencerSlots))
		}
		k.tagAlongSeqID = ids[rand.Intn(len(ids))]
	} else if _, ok := active[k.tagAlongSeqID]; !ok {
		return k.deferTagAlong(fmt.Sprintf("tag-along sequencer %s has no milestone in the last %d slots", k.tagAlongSeqID.StringShort(), activeSequencerSlots))
	}
	fee, err := retry("required tag-along fee", 3, func() (uint64, error) {
		return glb.GetRequiredTagAlongFee(k.tagAlongSeqID)
	})
	if err != nil {
		return k.deferTagAlong(err.Error())
	}
	k.tagAlongFee = fee
	if k.tagAlongDeferred {
		glb.Infof("   tag-along usable again: sequencer %s, fee %s", k.tagAlongSeqID.StringShort(), util.Th(fee))
		k.tagAlongDeferred = false
	}
	return true
}

func (k *consolidator) deferTagAlong(reason string) bool {
	if !k.tagAlongDeferred {
		glb.Infof("   deferred until a tag-along target is usable: %s", reason)
		k.tagAlongDeferred = true
	}
	return false
}

// compact folds the consumed set into one sigLock output back to the wallet,
// minus the tag-along fee: `proxi node compact` without the prompt and the
// inclusion wait. A single input going straight back achieves nothing.
func (k *consolidator) compact(p *plan) []base.OutputID {
	if len(p.inputs) < 2 {
		glb.Verbosef("   nothing to compact: one consolidatable output")
		return nil
	}
	if p.consumed < k.tagAlongFee+k.floor {
		glb.Verbosef("   nothing to compact: %s does not cover the tag-along fee %s plus the storage deposit %s of the output",
			util.Th(p.consumed), util.Th(k.tagAlongFee), util.Th(k.floor))
		return nil
	}
	inputs := make([]txbuildercore.CompactInput, len(p.inputs))
	for i, o := range p.inputs {
		inputs[i] = txbuildercore.CompactInput{OutputBytes: o.Output.Bytes(), ID: o.ID}
	}
	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(k.lib, k.consts, txbuildercore.CompactParams{
		Inputs:           inputs,
		WalletPrivateKey: k.wallet.PrivateKey,
		TagAlongSeqID:    k.tagAlongSeqID,
		TagAlongFee:      k.tagAlongFee,
		TargetSlot:       k.nowSlot(),
	})
	if err != nil {
		glb.Infof("   compaction build failed: %v", err)
		return nil
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		glb.Infof("   compaction submit failed: %v", err)
		return nil
	}
	glb.Infof("   compacted %d output(s) holding %s into one -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), txid.StringShort())
	return outputIDs(p.inputs)
}

// consumeInputs adds the outputs as inputs from index first on: the first
// carries the signature unlock, the rest reference it. On a tag-along input
// the reference is inert and the ledger falls back to the signer check, which
// the wallet satisfies as sender. Returns the raw bytes for submit-time
// validation and the newest input timestamp.
func consumeInputs(txb *txbuildercore.TxBuilder, outs []*ledger.OutputWithID, first byte) ([][]byte, base.LedgerTime, error) {
	consumed := make([][]byte, 0, len(outs))
	newest := base.NilLedgerTime
	for i, o := range outs {
		b := o.Output.Bytes()
		txb.ConsumeOutput(b, o.ID)
		consumed = append(consumed, b)
		newest = base.MaximumTime(newest, o.Timestamp())
		idx := first + byte(i)
		if i == 0 {
			txb.PutSignatureUnlock(idx)
			continue
		}
		if err := txb.PutUnlockReference(idx, txbuildercore.ConstraintIndexLock, first); err != nil {
			return nil, newest, err
		}
	}
	return consumed, newest, nil
}

// produceKept appends the single sigLock output the wallet keeps; nothing
// when there is nothing to keep. An amount under the storage floor is refused
// here rather than at submit, where the same transaction would be rebuilt and
// rejected every tick.
func (k *consolidator) produceKept(txb *txbuildercore.TxBuilder, kept uint64) error {
	if kept == 0 {
		return nil
	}
	if kept < k.floor {
		return fmt.Errorf("%s to keep is below the storage deposit %s of a sigLock output", util.Th(kept), util.Th(k.floor))
	}
	out, err := txbuildercore.NewSigLockOutput(k.lib, kept, k.holderID)
	if err != nil {
		return err
	}
	txb.ProduceOutput(out.Bytes())
	return nil
}

// timestamp is now, pushed past the newest input by the transaction pace so a
// just-received output can be consumed, and off the slot boundary, which is
// reserved for branches.
func (k *consolidator) timestamp(newestInput base.LedgerTime) base.LedgerTime {
	ts := base.MaximumTime(
		k.consts.LedgerTimeFromClockTime(time.Now()),
		newestInput.AddTicks(int(k.consts.TransactionPace)),
	)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}

// finish stamps, commits and signs.
func (k *consolidator) finish(txb *txbuildercore.TxBuilder, ts base.LedgerTime) base.TransactionID {
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(k.wallet.PrivateKey)
	txid, err := txbuildercore.TxIDFromBytes(txb.Bytes())
	glb.AssertNoError(err) // pure local computation over bytes just built
	return txid
}

// nowSlot is the current ledger slot from the local clock: ledger time is a
// pure function of wall-clock time and the genesis timestamp.
func (k *consolidator) nowSlot() uint32 {
	return k.consts.LedgerTimeFromClockTime(time.Now()).Slot
}

func sumBalance(outs []*ledger.OutputWithID) uint64 {
	ret := uint64(0)
	for _, o := range outs {
		ret += o.Output.TokenBalance()
	}
	return ret
}

func outputIDs(outs []*ledger.OutputWithID) []base.OutputID {
	ret := make([]base.OutputID, len(outs))
	for i, o := range outs {
		ret[i] = o.ID
	}
	return ret
}

// anyPresent reports whether any of the ids is still in the snapshot, which is
// how the loop tells an unsettled transaction from a settled one.
func anyPresent(outs []*ledger.OutputWithID, ids []base.OutputID) bool {
	present := make(map[base.OutputID]struct{}, len(outs))
	for _, o := range outs {
		present[o.ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := present[id]; ok {
			return true
		}
	}
	return false
}

// retry repeats f until it succeeds, backing off exponentially up to retryMax
// between tries. attempts <= 0 retries indefinitely: a wallet process must ride
// out node restarts and API timeouts rather than abort on the first failure.
func retry[T any](what string, attempts int, f func() (T, error)) (T, error) {
	var (
		zero    T
		lastErr error
	)
	d := retryBase
	for i := 1; attempts <= 0 || i <= attempts; i++ {
		v, err := f()
		if err == nil {
			return v, nil
		}
		lastErr = err
		glb.Verbosef("   %s failed (attempt %d): %v; retrying in %v", what, i, err, d)
		time.Sleep(d)
		if d *= 2; d > retryMax {
			d = retryMax
		}
	}
	return zero, fmt.Errorf("%s: giving up after %d attempt(s): %w", what, attempts, lastErr)
}
