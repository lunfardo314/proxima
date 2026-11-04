package seq_cmd

import (
	"crypto/ed25519"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func initSeqInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init_genesis <amount> [<flags>]",
		Short: `creates a new sequencer chain origin output (wallet-side); does NOT touch proxima.yaml`,
		Long: `Creates a fresh sequencer chain whose origin output holds <amount> tokens.
The producing transaction is a regular wallet tx (no ` + "`s`" + ` bit, no endorsements);
the new chain reaches the tangle via its tag-along output.

This command is the WALLET-SIDE concern only:
  - submits the chain-origin transaction;
  - records the new chain ID in proxi.yaml (wallet.sequencer_id).

The NODE-SIDE configuration (proxima.yaml's sequencer section + controller
key file) is the operator's manual job — edit proxima.yaml directly, and
copy/symlink/encrypt the controller key file as you see fit.

Optional flags fall into two groups:

  sequencer-data flags (mutable; seed the chain's slot-5 milestone data —
  same set as 'proxi node seq set-params'):
    --name, --fee, --margin (--profit_cut), --greedy, --pace, --enforce_freeze_bounds

  delegation-params flags (immutable; embedded in the chain's sequencer
  constraint at slot 4):
    --epoch-slots, --max-frozen-epochs

Any absent flag uses its library default, except --fee and --margin, which
default to the ledger's seeds for every new sequencer (100000 motes and 50
promille). Absent --name produces a nameless sequencer.

The chain ID sets the phase of the sequencer's delegation epoch grid, and with it
when its delegations' safe-revocation windows open. The command tries the
timestamps of the next slot, each giving another chain ID, and takes the one whose
grid lies farthest from the grids of the existing sequencers, weighted by the
capital frozen on each, so unfreezes across the network do not pile up. It then
waits for that timestamp before submitting, about one slot at most. --no_grind
submits at the earliest timestamp instead.`,
		Args: cobra.ExactArgs(1),
		Run:  runSeqInitCmd,
	}

	c.Flags().String("name", "", "sequencer name (1-6 chars; absent => nameless)")
	c.Flags().Uint64("fee", seqdata.DefaultMinimumFee, "minimum tag-along fee")
	c.Flags().Uint16("margin", seqdata.DefaultProfitMarginPromille, "sequencer (inflation) cut in promille (0-1000)")
	c.Flags().Uint16("profit_cut", seqdata.DefaultProfitMarginPromille, "synonym of --margin")
	c.Flags().Bool("greedy", false, "greedy flag")
	c.Flags().Uint8("pace", 0, "pace value (ticks)")
	c.Flags().Bool("enforce_freeze_bounds", false, "enforce the coverage contribution upper bound when freezing delegations")
	c.Flags().Uint32("epoch-slots", 0, "delegation epoch slots (default: library default)")
	c.Flags().Uint8("max-frozen-epochs", 0, "max frozen epochs (default: library default)")
	c.Flags().Bool("no_grind", false, "do not choose the chain ID for its epoch grid; submit at the earliest timestamp")

	c.InitDefaultHelpCmd()
	return c
}

func runSeqInitCmd(cmd *cobra.Command, args []string) {
	walletData := glb.GetWalletData()
	glb.Infof("wallet account is: %s", walletData.Account.String())

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)
	glb.Infof("amount: %s", util.Th(amount))

	consts := glb.GetLedgerConstants()

	// Delegation params (immutable; embedded in the sequencer constraint).
	// Absent flags fall back to ledger-defined defaults provided by the node.
	// Default maxFrozenEpochs to the ledger's absolute maximum, not the softer
	// library default: a wider freeze window gives the freeze-epoch balancer more
	// buckets to spread the unfrozen coverage across (see
	// kb/archive/shipped/delegation_freeze_distribution.md).
	epochSlots := consts.DelegationEpochSlots
	maxFrozenEpochs := byte(consts.DelegationMaxFrozenEpochs)
	if cmd.Flags().Changed("epoch-slots") {
		v, _ := cmd.Flags().GetUint32("epoch-slots")
		epochSlots = v
	}
	if cmd.Flags().Changed("max-frozen-epochs") {
		v, _ := cmd.Flags().GetUint8("max-frozen-epochs")
		maxFrozenEpochs = v
	}

	// Initial sequencer data — same flag set as set-params. Pushed as an
	// inline-data constraint at slot 5 of the chain origin output. The minimum
	// tag-along fee and the cut are always seeded, so a fresh sequencer never
	// starts out serving tag-alongs or delegators for free; the remaining
	// absent flags stay unset.
	sd := *seqdata.NewWithDefaults()
	minimumFee, _ := cmd.Flags().GetUint64("fee")
	sd.SetMinimumFee(minimumFee)
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		glb.Assertf(len(v) <= 6, "name must be empty or 1-6 characters")
		sd.SetName(v)
	}
	if v, ok := changedCutFlag(cmd); ok {
		glb.Assertf(v <= 1000, "sequencer cut must be 0-1000 promille")
		sd.SetSeqProfitMarginPromille(v)
	}
	if cmd.Flags().Changed("greedy") {
		v, _ := cmd.Flags().GetBool("greedy")
		sd.SetGreedy(v)
	}
	if cmd.Flags().Changed("pace") {
		v, _ := cmd.Flags().GetUint8("pace")
		sd.SetPace(v)
	}
	if cmd.Flags().Changed("enforce_freeze_bounds") {
		v, _ := cmd.Flags().GetBool("enforce_freeze_bounds")
		sd.SetEnforceFreezeBounds(v)
	}

	// Resolve the target once up front: MustGetTarget logs the resolved address,
	// and the prior code called it twice (here + inside makeSequencerChainOrigin),
	// producing two identical "wallet account (default as a target): …" lines.
	target := glb.MustGetTarget()
	_, isSig := target.(ledger.SigLock)
	glb.Assertf(isSig, "sequencer controller must be a wallet address (sigLock), got %s: proxi does not produce chainLock outputs", target.Name())

	noGrind, _ := cmd.Flags().GetBool("no_grind")
	cid, txid := makeSequencerChainOrigin(target, amount, epochSlots, maxFrozenEpochs, &sd, !noGrind)
	glb.Infof("new sequencer chain id is %s", cid.String())
	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}

	updateWalletConfig(cid)
	glb.Infof("\nproxi.yaml updated: wallet.sequencer_id = %s", cid.StringHex())
	glb.Infof("Next step (manual): edit proxima.yaml's `sequencer` section on the node — set")
	glb.Infof("  chain_id, controller_key_file, enable: true, and any operational flags you want.")
}

// makeSequencerChainOrigin composes and submits a sequencer transaction whose
// first produced output is the new sequencer chain origin. The sequencer
// constraint at slot 4 carries (epochSlots, maxFrozenEpochs); if initialSeqData
// is non-nil, its encoded JSON is pushed at slot 5 as inline milestone data.
//
// The tx has NO endorsements — _noChainPredecessorCase in def/sequencer.easyfl
// explicitly forbids them. The chain origin is naturally pulled into the tangle
// via its tag-along output (consumed by the tag-along sequencer).
//
// On submit failure SubmitAndDisplay has already printed the error and the
// failing-tx pretty-form; this helper exits(1) directly so the CLI doesn't
// print the error a second time.
func makeSequencerChainOrigin(
	target ledger.Lock,
	onChainAmount uint64,
	epochSlots uint32,
	maxFrozenEpochs byte,
	initialSeqData *seqdata.SequencerData,
	grind bool,
) (chainID base.ChainID, txid base.TransactionID) {
	walletData := glb.GetWalletData()
	clnt := glb.GetClient()
	consts := glb.GetLedgerConstants()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	glb.Infof("Creating new sequencer chain origin:")
	glb.Infof("   on-chain balance: %s", util.Th(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.Th(feeAmount), tagAlongSeqID.String())
	glb.Infof("   source account: %s", walletData.Account.String())
	glb.Infof("   total cost: %s", util.Th(onChainAmount+feeAmount))
	glb.Infof("   chain controller: %s", target)
	glb.Infof("   sequencer params: epochSlots=%d, maxFrozenEpochs=%d", epochSlots, maxFrozenEpochs)
	if initialSeqData != nil {
		glb.Infof("   initial sequencer data: %s", string(initialSeqData.Bytes()))
	} else {
		glb.Infof("   initial sequencer data: (none)")
	}

	if !glb.YesNoPrompt("proceed?:", true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	inps, lrbid, totalInputs, err := clnt.GetTransferableOutputs(walletData.Account)
	glb.AssertNoError(err)
	if onChainAmount == 0 {
		onChainAmount = totalInputs - feeAmount
	}
	glb.Assertf(totalInputs >= onChainAmount+feeAmount, "not enough source balance %s", util.Th(totalInputs))
	glb.PrintLRB(lrbid)

	picked := uint64(0)
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
		if picked < onChainAmount+feeAmount {
			picked += o.Output.TokenBalance()
			return true
		}
		return false
	})

	// Wallet-derived "now" — pace-enforced against the latest input.
	ts := glb.GetLedgerTimeNow()
	for _, in := range inps {
		ts = base.MaximumTime(ts, in.Timestamp().AddTicks(int(consts.TransactionPace)))
	}
	ts = originTimestamp(ts)

	var others []gridWeight
	if grind {
		others = existingGrids(epochSlots)
	}
	var txBytes []byte
	var consumed [][]byte
	var chainOutIdx byte
	var bestScore uint64
	for i, t := 0, ts; i < base.TicksPerSlot; i, t = i+1, originTimestamp(t.AddTicks(1)) {
		b, id, idx, cons, err := composeSequencerChainOriginTx(
			walletData.PrivateKey, inps, target, onChainAmount,
			*tagAlongSeqID, feeAmount, t,
			epochSlots, maxFrozenEpochs, initialSeqData,
		)
		glb.AssertNoError(err)
		cid := base.MakeOriginChainID(base.MustNewOutputID(id, idx))
		score := gridCloseness(consts.EpochOffsetSlotsDirect(cid, epochSlots), others, epochSlots)
		if i == 0 || score < bestScore {
			txBytes, txid, chainOutIdx, consumed, bestScore, ts = b, id, idx, cons, score, t
		}
		if len(others) == 0 {
			break
		}
	}
	if len(others) > 0 {
		cid := base.MakeOriginChainID(base.MustNewOutputID(txid, chainOutIdx))
		glb.Infof("epoch grid offset %d of %d, nearest of %d existing grids %d slots away",
			consts.EpochOffsetSlotsDirect(cid, epochSlots), epochSlots, len(others), nearestGrid(consts.EpochOffsetSlotsDirect(cid, epochSlots), others, epochSlots))
	}
	for glb.GetLedgerTimeNow().Before(ts) {
		time.Sleep(200 * time.Millisecond)
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		// SubmitAndDisplay already printed the failure detail.
		os.Exit(1)
	}

	chainOid := base.MustNewOutputID(txid, chainOutIdx)
	chainID = base.MakeOriginChainID(chainOid)
	return chainID, txid
}

// originTimestamp is the earliest timestamp from ts on that a sequencer chain origin may carry.
func originTimestamp(ts base.LedgerTime) base.LedgerTime {
	if ts.IsSlotBoundary() {
		// Sequencer origin cannot be a branch (tick==0) — see
		// _noChainPredecessorCase.
		ts = ts.AddTicks(1)
	}
	// From library upgrade 2 on, an origin is neither in the pre-branch consolidation zone (it
	// has no chain predecessor of the same slot) nor before the post-branch consolidation tick.
	slotConsts := glb.GetLedgerConstantsAt(ts.Slot)
	if slotConsts.RequiresSameSlotChainPredecessor(ts) {
		ts = base.T(ts.Slot+1, 0)
		slotConsts = glb.GetLedgerConstantsAt(ts.Slot)
	}
	if ts.Tick < slotConsts.PostBranchConsolidationTicks {
		ts = base.T(ts.Slot, slotConsts.PostBranchConsolidationTicks)
	}
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	return ts
}

// gridWeight is the epoch grid offset of an existing sequencer and the capital frozen on it.
type gridWeight struct {
	offset uint32
	weight uint64
}

// existingGrids returns the grids of the sequencers on the node, active or not, each
// weighted by its frozen coverage, at least 1. Empty when they cannot be fetched.
func existingGrids(epochSlots uint32) []gridWeight {
	active, inactive, err := glb.FetchSequencerCandidates()
	if err != nil {
		glb.Infof("warning: cannot fetch the sequencers, the epoch grid is not chosen: %v", err)
		return nil
	}
	consts := glb.GetLedgerConstants()
	ret := make([]gridWeight, 0, len(active)+len(inactive))
	for _, c := range append(active, inactive...) {
		ret = append(ret, gridWeight{offset: consts.EpochOffsetSlotsDirect(c.ID, epochSlots), weight: max(c.FrozenCoverage, 1)})
	}
	return ret
}

// gridDistance is the distance between two grid offsets on the epoch circle.
func gridDistance(a, b, epochSlots uint32) uint32 {
	d := max(a, b) - min(a, b)
	return min(d, epochSlots-d)
}

// gridCloseness scores a grid offset against the existing grids: each adds its weight
// times how far inside half an epoch it lies. Lower is better; 0 is no grid that close.
func gridCloseness(offset uint32, others []gridWeight, epochSlots uint32) uint64 {
	var ret uint64
	for _, o := range others {
		if d := gridDistance(offset, o.offset, epochSlots); d < epochSlots/2 {
			ret += o.weight * uint64(epochSlots/2-d)
		}
	}
	return ret
}

// nearestGrid is the distance from offset to the closest existing grid.
func nearestGrid(offset uint32, others []gridWeight, epochSlots uint32) uint32 {
	ret := epochSlots
	for _, o := range others {
		ret = min(ret, gridDistance(offset, o.offset, epochSlots))
	}
	return ret
}

// composeSequencerChainOriginTx is the pure wasm-wallet compose helper for the
// sequencer-chain-origin tx. The produced tx is a REGULAR WALLET TX (no `s` bit,
// no SequencerData slot) that emits a chain-origin output carrying:
//   - the 2-arg sequencer constraint at slot 4 (with epochSlots, maxFrozenEpochs);
//   - optional sequencer milestone data at slot 5 (when initialSeqData != nil).
//
// The easyfl `sequencer` constraint skips the milestone-index check when the
// sibling chain constraint is an origin, so SetSequencerData is intentionally NOT
// called. No endorsements either — the origin reaches the tangle via its
// tag-along output.
func composeSequencerChainOriginTx(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	target ledger.Lock,
	onChainAmount uint64,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	ts base.LedgerTime,
	epochSlots uint32,
	maxFrozenEpochs byte,
	initialSeqData *seqdata.SequencerData,
) (txBytes []byte, txid base.TransactionID, chainOutIdx byte, consumed [][]byte, err error) {
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	txb := txbuildercore.New(0)

	consumedTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		consumedTotal += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, 0, nil, err
			}
		}
	}

	// Chain-origin output: target lock + chainOrigin at slot 3 + sequencer
	// constraint at slot 4 + optional sequencer milestone data at slot 5.
	baseChainOut, err := glb.BuildLockOutput(lib, onChainAmount, target)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder, err := txbuildercore.OutputBuilderFromBytes(baseChainOut.Bytes())
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBin, err := lib.NewChainOrigin(ts.Slot)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder.MustPushConstraint(chainOriginBin)
	// chain origin: coverageDelta starts at 0 (origins are exempt from the
	// strict-increase rule; the first milestone sets the real value).
	seqBin, err := lib.NewSequencerConstraintBytecode(0)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder.MustPushConstraint(seqBin)
	if initialSeqData != nil {
		chainOriginBuilder.MustPushConstraint(engine.InlineDataBytecode(initialSeqData.Bytes()))
	}
	chainOutIdx = txb.ProduceOutput(chainOriginBuilder.Output().Bytes())

	if tagAlongFee > 0 {
		tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(tagAlongOut.Bytes())
	}
	if consumedTotal > onChainAmount+tagAlongFee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, consumedTotal-onChainAmount-tagAlongFee, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(remainderOut.Bytes())
	}

	glb.SetTimestamp(txb, ts)
	// No SetSequencerData call: the tx is a regular wallet tx producing a chain-origin
	// output. The easyfl `sequencer` constraint skips the milestone-index check when the
	// chain constraint is an origin (selfChainPredInputIndex == 0x). No endorsements
	// either — the origin is naturally pulled into the tangle via its tag-along output.
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	return txBytes, txid, chainOutIdx, consumed, nil
}

func updateWalletConfig(chainId base.ChainID) {
	data, err := os.ReadFile("proxi.yaml")
	glb.AssertNoError(err)

	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	if wallet, ok := config["wallet"].(map[interface{}]interface{}); ok {
		wallet["sequencer_id"] = chainId.StringHex()
	}

	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)
	err = os.WriteFile("proxi.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}
