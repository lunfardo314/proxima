// Package consolidator is the wallet consolidator: a permanent process on a
// wallet key that periodically sweeps the outputs scattered over the account
// and puts what is above a configured minimum back into consensus, as a
// transfer to a sequencer or as a delegation, or at least folds them into a
// single output. Spec: kb/consolidate.md.
//
// It runs inside whatever process holds the key. `proxi node consolidate`
// runs it alone, `proxi node mine` runs it beside the miner, and a wallet of
// any other kind can run it the same way: the configuration comes in the
// start call, the node is reached through an API client, and every line it
// says goes to the writer the caller supplies. Nothing here reads a profile
// or a flag, and nothing depends on the ledger singleton.
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
package consolidator

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
)

const (
	// Sized for the take 1 mine payouts, 94 PROX each and about four times as
	// frequent as before: the balance trigger fires every three or four
	// payouts, and what it moves is then at least the 100 PROX a top-up request
	// must carry (threshold less the minimum kept).
	DefaultThresholdPROX      = 300
	DefaultMinimumBalancePROX = 100
	DefaultMaxInputs          = 30
	DefaultCompactAt          = 10
	DefaultTargetDelegations  = 5
	DefaultTargetSizePROX     = 10_000
	// how often a tick that takes no action still reports what it sees, so a
	// quiet wallet shows the process is alive and how far it is from acting.
	DefaultStatusPeriod = 5 * time.Minute

	// how often the account is re-read. A transaction takes several slots to
	// settle, so there is nothing to gain from polling faster.
	tickPeriod = 10 * time.Second

	// how long to wait for a submitted transaction before presuming it dropped
	// (never picked up by the tag-along sequencer, or orphaned) and rebuilding
	// from a fresh snapshot.
	pendingTimeout = 3 * time.Minute

	// bounds of the exponential backoff between retries of a node call
	retryBase = 500 * time.Millisecond
	retryMax  = 15 * time.Second
)

// Config is the effective configuration of a run, amounts in motes. The
// caller resolves it from wherever its settings live (a profile, flags, a
// form) and Validate checks what the rules require of it.
type Config struct {
	Threshold uint64 // the consolidatable balance must exceed this to act
	Minimum   uint64 // always left on the wallet's sigLock outputs
	MaxInputs int
	CompactAt int
	SendOwn   bool          // SendTo is the wallet's own sequencer: control is verified before each transfer
	SendTo    *base.ChainID // nil = sending disabled
	// DelegateRandom draws a target by the delegation rating on every action;
	// DelegateTo pins one. Neither = delegation disabled.
	DelegateRandom bool
	DelegateTo     *base.ChainID
	// the delegation set is driven by two numbers: how many delegations to
	// build up to, and how large one is grown before the next is started
	TargetDelegations int
	TargetSize        uint64
	StatusPeriod      time.Duration // between status lines of idle ticks; 0 = none
	// TagAlongSeqID is the sequencer the wallet's own transactions tag along
	// with; nil draws an active one by the tag-along rating before each
	// transaction. TagAlongFee is the wallet's preferred fee, paid when it is
	// above the sequencer's declared minimum.
	TagAlongSeqID *base.ChainID
	TagAlongFee   uint64
}

func (c *Config) sendEnabled() bool     { return c.SendTo != nil }
func (c *Config) delegateEnabled() bool { return c.DelegateRandom || c.DelegateTo != nil }

// Validate is the rules' precondition on the numbers; a value outside them
// would make the loop build nothing or build nonsense every tick.
func (c *Config) Validate() error {
	switch {
	case c.TargetDelegations < 1:
		return errors.New("target_delegations must be at least 1")
	case c.TargetSize == 0:
		return errors.New("target_delegation_prox must be positive")
	case c.MaxInputs < 2 || c.MaxInputs > 256:
		return fmt.Errorf("max_inputs must be 2-256, got %d", c.MaxInputs)
	case c.CompactAt < 2:
		return errors.New("compact_at must be >= 2: compacting fewer than two outputs achieves nothing")
	case c.Minimum == 0:
		return errors.New("minimum_balance_prox must be positive")
	case c.Threshold < c.Minimum:
		return errors.New("threshold_prox must be at least minimum_balance_prox")
	case c.StatusPeriod < 0:
		return errors.New("status_period must not be negative")
	}
	return nil
}

// Environment is what the run needs from its host: the node, the ledger as
// the wallet sees it, the key, and where to write.
type Environment struct {
	Client     *client.APIClient
	Library    *txbuildercore.Library[any]
	Constants  *txbuildercore.Constants
	PrivateKey ed25519.PrivateKey
	// Log receives every line the consolidator says, one Write per line, each
	// ending in a newline, so a host can prefix or route them. nil is stdout.
	Log     io.Writer
	Verbose bool
}

// Consolidator holds a run: immutable configuration and node handles, the
// tag-along target and fee as last resolved, plus the outputs consumed by the
// last submitted transaction.
type Consolidator struct {
	cfg        Config
	consts     *txbuildercore.Constants
	lib        *txbuildercore.Library[any]
	c          *client.APIClient
	privateKey ed25519.PrivateKey
	account    ledger.SigLock
	holderID   base.HolderID
	log        io.Writer
	verbose    bool
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
	lastStatus   time.Time
}

// New checks the configuration and resolves from the node what the run needs
// up front: the storage floor and the existence of any pinned target. It
// returns an error rather than starting a loop that could never act, so a
// host that embeds the consolidator can carry on without it.
func New(cfg Config, env Environment) (*Consolidator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if env.Log == nil {
		env.Log = os.Stdout
	}
	k := &Consolidator{
		cfg:            cfg,
		consts:         env.Constants,
		lib:            env.Library,
		c:              env.Client,
		privateKey:     env.PrivateKey,
		account:        ledger.SigLockFromED25519PrivateKey(env.PrivateKey),
		holderID:       base.HolderIDFromED25519PrivateKey(env.PrivateKey),
		log:            env.Log,
		verbose:        env.Verbose,
		tagAlongRandom: cfg.TagAlongSeqID == nil,
	}
	if cfg.TagAlongSeqID != nil {
		k.tagAlongSeqID = *cfg.TagAlongSeqID
	}
	floor, err := retry(k.verbosef, "storage deposit of a sigLock output", 5, k.sigLockFloor)
	if err != nil {
		return nil, err
	}
	k.floor = floor
	if cfg.Minimum < floor {
		return nil, fmt.Errorf("minimum_balance_prox must be at least the storage deposit of a sigLock output, %s motes", util.Th(floor))
	}
	if err = k.checkTargets(); err != nil {
		return nil, err
	}
	return k, nil
}

// Run prints the banner and loops until ctx is done.
func (k *Consolidator) Run(ctx context.Context) {
	k.banner()
	for {
		k.tick()
		select {
		case <-ctx.Done():
			return
		case <-time.After(tickPeriod):
		}
	}
}

// Every message of the running loop carries the local time: the process is
// permanent and its output is read long after the fact. The banner, printed
// once at start, stays bare.
func (k *Consolidator) logf(format string, args ...any) {
	k.linef(time.Now().Format(time.DateTime)+" "+format, args...)
}

func (k *Consolidator) verbosef(format string, args ...any) {
	if k.verbose {
		k.logf(format, args...)
	}
}

// linef writes one line in one call, the unit a host may prefix.
func (k *Consolidator) linef(format string, args ...any) {
	_, _ = fmt.Fprintf(k.log, format+"\n", args...)
}

// submit posts the transaction with its consumed outputs, so the node runs
// full validation before accepting it, and on a refusal shows the failing
// transaction: the next tick rebuilds it from a fresh snapshot anyway, so the
// display is the only trace of what went wrong.
func (k *Consolidator) submit(txBytes []byte, consumed ...[]byte) error {
	_, err := k.c.SubmitTransactionWithDetail(txBytes, client.WithConsumedUTXOs(consumed))
	if err != nil {
		k.linef("---------- failing tx --------\n%s", transaction.TxDisplay(k.lib, txBytes, consumed...))
	}
	return err
}

// requiredTagAlongFee is the fee a transaction tagging along with seqID must
// carry: the sequencer's declared minimum, or the wallet's preferred fee when
// that is larger. The sequencer must be readable; a transaction built on a
// guessed fee is one the target silently never picks up.
func (k *Consolidator) requiredTagAlongFee(seqID base.ChainID) (uint64, error) {
	info, err := k.c.GetSequencerTargetInfo(seqID)
	if err != nil {
		return 0, fmt.Errorf("cannot read the minimum tag-along fee of sequencer %s: %w", seqID.StringShort(), err)
	}
	return max(info.MinimumFee, k.cfg.TagAlongFee), nil
}

// checkTargets verifies that a configured explicit target exists on the
// ledger and is a sequencer chain, like the tag-along target. Whether the own
// sequencer is controlled by the wallet is checked before every transaction
// instead, since control can change hands while the process runs.
func (k *Consolidator) checkTargets() error {
	for _, id := range []*base.ChainID{k.cfg.SendTo, k.cfg.DelegateTo} {
		if id == nil {
			continue
		}
		o, err := retry(k.verbosef, "read sequencer "+id.StringShort(), 3, func() (*ledger.OutputDataWithID, error) {
			o, _, err := k.c.GetChainOutputData(*id)
			return o, err
		})
		if err != nil {
			return fmt.Errorf("cannot resolve sequencer %s: %w", id.String(), err)
		}
		if !o.ID.IsSequencerTransaction() {
			return fmt.Errorf("%s is not a sequencer chain (chain output %s)", id.StringShort(), o.ID.StringShort())
		}
	}
	return nil
}

func (k *Consolidator) banner() {
	k.linef("")
	k.linef("================= PROXIMA WALLET CONSOLIDATOR =================")
	k.linef(" account          : %s", k.account.String())
	k.linef(" minimum balance  : %s (kept on sigLock outputs)", util.Th(k.cfg.Minimum))
	k.linef(" acts when        : consolidatable total > %s over >= 2 outputs, or >= %d consolidatable outputs",
		util.Th(k.cfg.Threshold), k.cfg.CompactAt)
	k.linef(" inputs per tx    : up to %d, smallest first", k.cfg.MaxInputs)
	switch {
	case k.cfg.SendOwn:
		k.linef(" above minimum    : sent to own sequencer %s (control verified before each transfer)", k.cfg.SendTo.String())
	case k.cfg.sendEnabled():
		k.linef(" above minimum    : sent to sequencer %s", k.cfg.SendTo.String())
	case k.cfg.DelegateRandom:
		k.linef(" above minimum    : delegated to an active sequencer drawn by rating (share left, balance, frozen-to-balance ratio), at the cut it leaves")
		k.linef(" delegations      : grown to %s each, up to %d of them; more than that are folded together, stale ones re-delegated",
			util.Th(k.cfg.TargetSize), k.cfg.TargetDelegations)
	case k.cfg.DelegateTo != nil:
		k.linef(" above minimum    : delegated to sequencer %s at the cut it leaves", k.cfg.DelegateTo.String())
		k.linef(" delegations      : grown to %s each, up to %d of them; more than that are folded together, stale ones re-delegated",
			util.Th(k.cfg.TargetSize), k.cfg.TargetDelegations)
	default:
		k.linef(" above minimum    : stays in the wallet, compacted into one output")
	}
	if k.tagAlongRandom {
		k.linef(" tag-along        : a random active sequencer, drawn before each transaction")
	} else {
		k.linef(" tag-along        : sequencer %s, fee read before each transaction", k.tagAlongSeqID.StringShort())
	}
	k.linef(" storage floor    : %s (smallest sigLock output the wallet keeps)", util.Th(k.floor))
	if k.cfg.StatusPeriod > 0 {
		k.linef(" tick             : every %v; a status line every %v while idle", tickPeriod, k.cfg.StatusPeriod)
	} else {
		k.linef(" tick             : every %v; no status line while idle", tickPeriod)
	}
	k.linef("===============================================================")
}

// tick is one pass: read, decide, act at most once. A pass that ends without
// an action reports its view of the account once per status period.
func (k *Consolidator) tick() {
	outs, err := k.consolidatable()
	if err != nil {
		k.logf("cannot read the wallet account: %v; retrying next tick", err)
		return
	}
	var dels []*ownDelegation
	if k.cfg.delegateEnabled() {
		if dels, err = k.listOwnDelegations(); err != nil {
			k.logf("cannot read the wallet's delegations: %v; retrying next tick", err)
			return
		}
	}
	var p *plan
	acted := false
	defer func() {
		if acted {
			k.lastStatus = time.Now()
		} else if k.cfg.StatusPeriod > 0 && time.Since(k.lastStatus) >= k.cfg.StatusPeriod {
			k.status(outs, dels, p)
			k.lastStatus = time.Now()
		}
	}()
	if len(k.pending) > 0 {
		switch {
		case !anyPresent(outs, k.pending) && !anyDelegationPresent(dels, k.pending):
			k.logf("the last transaction settled")
			k.pending = nil
		case time.Since(k.pendingSince) > pendingTimeout:
			k.logf("the last transaction has not settled in %v; presumed dropped, rebuilding from a fresh snapshot", pendingTimeout)
			k.pending = nil
		default:
			return
		}
	}
	// The delegation set is tidied before anything is swept: each tidying
	// action fixes one state (one delegation fewer, one re-delegated), so a
	// wallet that always has something to sweep cannot starve it.
	if k.cfg.delegateEnabled() && len(dels) > 0 {
		if !k.refreshTagAlong() {
			return
		}
		if consumed := k.manageDelegations(dels); len(consumed) > 0 {
			k.pending, k.pendingSince, acted = consumed, time.Now(), true
			return
		}
	}
	p = planConsolidation(outs, k.cfg.Threshold, k.cfg.Minimum, k.cfg.MaxInputs, k.cfg.CompactAt, k.consts.SmallestAmountsPerBaseToken, k.floor)
	if p == nil {
		return
	}
	if !k.refreshTagAlong() {
		return
	}
	k.verbosef("%d consolidatable output(s) holding %s; consuming %d holding %s: keep %s, move %s",
		len(outs), util.Th(sumBalance(outs)), len(p.inputs), util.Th(p.consumed), util.Th(p.kept), util.Th(p.moved))

	var consumed []base.OutputID
	switch {
	case p.moved > 0 && k.cfg.sendEnabled():
		consumed = k.sendToSequencer(p)
	case p.moved > 0 && k.cfg.delegateEnabled():
		consumed = k.delegate(p, dels)
	}
	if consumed == nil {
		consumed = k.compact(p)
	}
	if len(consumed) > 0 {
		k.pending, k.pendingSince, acted = consumed, time.Now(), true
	}
}

// status is the periodic line of a tick that took no action: what the
// wallet holds, the rule that would trigger an action, and why none was taken.
func (k *Consolidator) status(outs []*ledger.OutputWithID, dels []*ownDelegation, p *plan) {
	msg := fmt.Sprintf("%d consolidatable output(s) holding %s", len(outs), util.Th(sumBalance(outs)))
	if k.cfg.delegateEnabled() {
		total := uint64(0)
		for _, d := range dels {
			total += d.balance
		}
		msg += fmt.Sprintf(", %d delegation(s) holding %s", len(dels), util.Th(total))
	}
	switch {
	case len(k.pending) > 0:
		msg += fmt.Sprintf("; waiting %v for the last transaction to settle", time.Since(k.pendingSince).Round(time.Second))
	case p == nil:
		msg += fmt.Sprintf("; acts when the total exceeds %s over >= 2 outputs, or at >= %d outputs: no action",
			util.Th(k.cfg.Threshold), k.cfg.CompactAt)
	default:
		msg += "; action deferred, see above"
	}
	k.logf("%s", msg)
}

// consolidatable is what the process may sweep: the wallet's plain sigLock
// outputs and the tag-along outputs it sent whose window has passed, the
// sequencer requests among them (askstop, withdraw, set-params) included:
// their ensure constraints bind only while the target can consume them. Both
// are filtered through the
// shared spendable classifier at the current slot, as `proxi node compact`
// does; everything else it would sweep (sendWithDeadline reclaims and accepts)
// is a one-off decision left to that command. A tag-along nobody reclaims
// becomes anybody's after the reclaim window, so this pass is what keeps the
// wallet's own requests from being taken by a stranger.
func (k *Consolidator) consolidatable() ([]*ledger.OutputWithID, error) {
	slot := k.nowSlot()
	outs, err := retry(k.verbosef, "read the wallet account", 3, func() ([]*ledger.OutputWithID, error) {
		o, _, _, err := k.c.GetSpendableOutputs(k.account, client.SpendableOutputsParams{
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
			k.verbosef("skipping %s: class %d, %v", o.ID.StringShort(), cls, err)
			continue
		}
		kind, err := k.lib.ClassifyLock(o.Output.Bytes(), k.holderID)
		if err != nil || (kind != txbuildercore.LockKindSig && kind != txbuildercore.LockKindTagAlongSender) {
			k.verbosef("skipping %s: lock kind %d, %v", o.ID.StringShort(), kind, err)
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
// consolidatable set. It acts when the total exceeds the threshold over at
// least two outputs (enough has accumulated, and it is scattered: one large
// output is left alone) or when at least compactAt outputs have piled up
// (worth folding whatever they hold); in the second case alone nothing moves. The minimum is a property of the whole account, so outputs
// left unconsumed count toward it. A movable amount under one base token is
// not worth sending and stays; a remainder under floor, the storage deposit of
// the output that keeps it, cannot be an output and is folded into what
// moves. Returns nil when there is nothing to do, or only dust that cannot
// yet form one output.
func planConsolidation(outs []*ledger.OutputWithID, threshold, minimum uint64, maxInputs, compactAt int, oneBaseToken, floor uint64) *plan {
	total := sumBalance(outs)
	aboveThreshold := total > threshold && len(outs) >= 2
	if !aboveThreshold && len(outs) < compactAt {
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
	if !aboveThreshold {
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
	return &plan{inputs: inputs, consumed: consumed, kept: kept, moved: moved}
}

// sendToSequencer moves everything above the minimum to the configured
// sequencer as one tag-along output, which the sequencer takes as it takes any
// fee, so no separate fee output is built; a wallet never produces a
// chainLock output. Returns the consumed IDs, or nil when the
// transfer was not made this tick and the outputs should be compacted instead.
func (k *Consolidator) sendToSequencer(p *plan) []base.OutputID {
	target := *k.cfg.SendTo
	if k.cfg.SendOwn {
		if err := k.ownSequencerControlled(target); err != nil {
			k.logf("sending refused: %v", err)
			return nil
		}
	}
	if active, err := k.sequencerActive(target); err != nil {
		k.logf("sending deferred: %v", err)
		return nil
	} else if !active {
		k.logf("sending deferred: sequencer %s has no settled milestone in the last %d slots", target.StringShort(), txbuildercore.ActiveSequencerSlots)
		return nil
	}
	minFee, err := retry(k.verbosef, "minimum fee of sequencer "+target.StringShort(), 3, func() (uint64, error) {
		return k.requiredTagAlongFee(target)
	})
	if err != nil {
		k.logf("sending deferred: %v", err)
		return nil
	}
	if p.moved < minFee {
		k.logf("sending deferred: %s is below the minimum %s sequencer %s takes", util.Th(p.moved), util.Th(minFee), target.StringShort())
		return nil
	}

	txb := txbuildercore.New(0)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		k.logf("transfer build failed: %v", err)
		return nil
	}
	out, err := txbuildercore.NewTagAlongOutput(k.lib, p.moved, target, k.holderID)
	if err != nil {
		k.logf("transfer build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(out.Bytes())
	if err = k.produceKept(txb, p.kept); err != nil {
		k.logf("transfer build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = k.submit(txb.Bytes(), consumed...); err != nil {
		k.logf("transfer submit failed: %v", err)
		return nil
	}
	k.logf("consolidated %d output(s) holding %s: sent %s to sequencer %s, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Th(p.consumed), util.Th(p.moved), target.StringShort(), util.Th(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// ownSequencerControlled reads the sequencer's chain output and checks that
// its lock is a plain sigLock held by this wallet: the same read `proxi node
// seq info` shows as the controller lock. Tokens sent to a sequencer end up in
// its chain output, and only its controller can withdraw them.
func (k *Consolidator) ownSequencerControlled(seqID base.ChainID) error {
	o, err := retry(k.verbosef, "read own sequencer", 3, func() (*ledger.OutputDataWithID, error) {
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

// sequencerActive reports whether the sequencer is active in the LRB state.
func (k *Consolidator) sequencerActive(seqID base.ChainID) (bool, error) {
	active, err := k.activeSequencers()
	if err != nil {
		return false, err
	}
	_, ok := active[seqID]
	return ok, nil
}

// activeSequencers is the sequencers active in the LRB state, as rating
// candidates (kb/sequencer_rating.md): one read of the node's sequencer list
// serves the send, tag-along and delegation modes alike.
func (k *Consolidator) activeSequencers() (map[base.ChainID]txbuildercore.SequencerCandidate, error) {
	type listing struct {
		outs  map[base.ChainID]client.SequencerOutput
		lrbID *base.TransactionID
	}
	l, err := retry(k.verbosef, "list sequencers", 3, func() (listing, error) {
		outs, lrbID, err := k.c.GetAllSequencerOutputs()
		return listing{outs, lrbID}, err
	})
	if err != nil {
		return nil, err
	}
	active, _ := client.SequencerCandidates(l.outs, l.lrbID)
	ret := make(map[base.ChainID]txbuildercore.SequencerCandidate, len(active))
	for _, c := range active {
		ret[c.ID] = c
	}
	return ret, nil
}

// sigLockFloor is the storage deposit of the wallet's sigLock output, sized
// with the widest amount encoding so it holds for any amount.
func (k *Consolidator) sigLockFloor() (uint64, error) {
	probe, err := txbuildercore.NewSigLockOutput(k.lib, 1<<62, k.holderID)
	if err != nil {
		return 0, err
	}
	return k.c.MinStorageDeposit(probe)
}

// refreshTagAlong resolves the tag-along target and its fee for the
// transaction about to be built: a random target is drawn by the tag-along
// rating among the sequencers active now, a configured one must be active
// now. Returns false when no target is usable this tick, logging that once
// until one is again.
func (k *Consolidator) refreshTagAlong() bool {
	active, err := k.activeSequencers()
	if err != nil {
		return k.deferTagAlong(err.Error())
	}
	if k.tagAlongRandom {
		if len(active) == 0 {
			return k.deferTagAlong(fmt.Sprintf("no sequencer has a settled milestone in the last %d slots", txbuildercore.ActiveSequencerSlots))
		}
		rated := txbuildercore.RateSequencers(candidateList(active), txbuildercore.TagAlongCriteria)
		k.tagAlongSeqID = txbuildercore.DrawSequencer(rated, rand.Intn).ID
	} else if _, ok := active[k.tagAlongSeqID]; !ok {
		return k.deferTagAlong(fmt.Sprintf("tag-along sequencer %s has no settled milestone in the last %d slots", k.tagAlongSeqID.StringShort(), txbuildercore.ActiveSequencerSlots))
	}
	fee, err := retry(k.verbosef, "required tag-along fee", 3, func() (uint64, error) {
		return k.requiredTagAlongFee(k.tagAlongSeqID)
	})
	if err != nil {
		return k.deferTagAlong(err.Error())
	}
	k.tagAlongFee = fee
	if k.tagAlongDeferred {
		k.logf("tag-along usable again: sequencer %s, fee %s", k.tagAlongSeqID.StringShort(), util.Th(fee))
		k.tagAlongDeferred = false
	}
	return true
}

// candidateList is the map's values; RateSequencers orders them.
func candidateList(m map[base.ChainID]txbuildercore.SequencerCandidate) []txbuildercore.SequencerCandidate {
	ret := make([]txbuildercore.SequencerCandidate, 0, len(m))
	for _, c := range m {
		ret = append(ret, c)
	}
	return ret
}

func (k *Consolidator) deferTagAlong(reason string) bool {
	if !k.tagAlongDeferred {
		k.logf("deferred until a tag-along target is usable: %s", reason)
		k.tagAlongDeferred = true
	}
	return false
}

// compact folds the consumed set into one sigLock output back to the wallet,
// minus the tag-along fee: `proxi node compact` without the prompt and the
// inclusion wait.
func (k *Consolidator) compact(p *plan) []base.OutputID {
	if p.consumed < k.tagAlongFee+k.floor {
		k.verbosef("nothing to compact: %s does not cover the tag-along fee %s plus the storage deposit %s of the output",
			util.Th(p.consumed), util.Th(k.tagAlongFee), util.Th(k.floor))
		return nil
	}
	inputs := make([]txbuildercore.CompactInput, len(p.inputs))
	for i, o := range p.inputs {
		inputs[i] = txbuildercore.CompactInput{OutputBytes: o.Output.Bytes(), ID: o.ID}
	}
	txBytes, txid, consumed, err := txbuildercore.MakeCompactTransaction(k.lib, k.consts, txbuildercore.CompactParams{
		Inputs:           inputs,
		WalletPrivateKey: k.privateKey,
		TagAlongSeqID:    k.tagAlongSeqID,
		TagAlongFee:      k.tagAlongFee,
		TargetSlot:       k.nowSlot(),
	})
	if err != nil {
		k.logf("compaction build failed: %v", err)
		return nil
	}
	if err = k.submit(txBytes, consumed...); err != nil {
		k.logf("compaction submit failed: %v", err)
		return nil
	}
	k.logf("compacted %d output(s) holding %s into one -> %s (submitted, not awaited)",
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
func (k *Consolidator) produceKept(txb *txbuildercore.TxBuilder, kept uint64) error {
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
func (k *Consolidator) timestamp(newestInput base.LedgerTime) base.LedgerTime {
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
func (k *Consolidator) finish(txb *txbuildercore.TxBuilder, ts base.LedgerTime) base.TransactionID {
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(k.privateKey)
	txid, err := txbuildercore.TxIDFromBytes(txb.Bytes())
	util.AssertNoError(err) // pure local computation over bytes just built
	return txid
}

// nowSlot is the current ledger slot from the local clock: ledger time is a
// pure function of wall-clock time and the genesis timestamp.
func (k *Consolidator) nowSlot() uint32 {
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

// anyDelegationPresent is anyPresent over the wallet's delegation outputs,
// for a transaction that consumed delegations rather than plain outputs.
func anyDelegationPresent(dels []*ownDelegation, ids []base.OutputID) bool {
	present := make(map[base.OutputID]struct{}, len(dels))
	for _, d := range dels {
		present[d.oid] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := present[id]; ok {
			return true
		}
	}
	return false
}

// retry repeats f until it succeeds, backing off exponentially up to retryMax
// between tries, reporting each failure through log. attempts <= 0 retries
// indefinitely: a wallet process must ride out node restarts and API timeouts
// rather than abort on the first failure.
func retry[T any](log func(string, ...any), what string, attempts int, f func() (T, error)) (T, error) {
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
		log("%s failed (attempt %d): %v; retrying in %v", what, i, err, d)
		time.Sleep(d)
		if d *= 2; d > retryMax {
			d = retryMax
		}
	}
	return zero, fmt.Errorf("%s: giving up after %d attempt(s): %w", what, attempts, lastErr)
}
